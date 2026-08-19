package cli_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductionRuntimeExecutesExternalToolWithOptionalDefaults(t *testing.T) {
	t.Setenv("R42_EXTERNAL_HELPER", "1")
	directory := t.TempDir()
	program, err := json.Marshal(os.Args[0])
	require.NoError(t, err)
	source := fmt.Sprintf(`
external_tool "lookup" {
  description = "Look up evidence"
  program = [%s, "-test.run=TestExternalToolHelperProcess", "--"]
  input_type = object({ query = string, limit = optional(number, 5) })
  output_type = object({ answer = string })
}
research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
	tool_ids = [external_tool.lookup.id]
}
`, program)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(source), 0o600))
	opener := &externalCallingOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Contains(t, opener.result, `"accepted":true`)
	assert.Contains(t, opener.result, "limit=5")
}

func TestProductionRuntimeRecordsFailedExternalToolInDebugLog(t *testing.T) {
	t.Setenv("R42_EXTERNAL_HELPER", "1")
	t.Setenv("R42_EXTERNAL_FAIL", "1")
	directory := t.TempDir()
	program, err := json.Marshal(os.Args[0])
	require.NoError(t, err)
	source := fmt.Sprintf(`
external_tool "lookup" {
  description = "Look up evidence"
  program = [%s, "-test.run=TestExternalToolHelperProcess", "--"]
  input_type = object({ query = string })
  output_type = object({ answer = string })
}
research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
	tool_ids = [external_tool.lookup.id]
}
`, program)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(source), 0o600))
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: &externalCallingOpener{}})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1, Debug: true})

	require.Error(t, err)
	runs, readErr := os.ReadDir(filepath.Join(directory, ".r42", "runs"))
	require.NoError(t, readErr)
	require.Len(t, runs, 1)
	events, readErr := os.ReadFile(filepath.Join(directory, ".r42", "runs", runs[0].Name(), "events.jsonl"))
	require.NoError(t, readErr)
	assert.Contains(t, string(events), `"tool_name":"tool_external_tool_lookup_`)
	assert.Contains(t, string(events), `"tool_address":"external_tool.lookup"`)
	assert.Contains(t, string(events), `"query":"facts"`)
	assert.Contains(t, string(events), "failed stdout")
	assert.Contains(t, string(events), "complete failed stderr")
	assert.Contains(t, string(events), "running external tool")
}

func TestProductionRuntimeExternalToolQuotaConsumesOnlySuccessfulCalls(t *testing.T) {
	t.Setenv("R42_EXTERNAL_HELPER", "1")
	directory := t.TempDir()
	program, err := json.Marshal(os.Args[0])
	require.NoError(t, err)
	source := fmt.Sprintf(`
external_tool "lookup" {
  description = "Look up evidence"
  program = [%s, "-test.run=TestExternalToolHelperProcess", "--"]
  input_type = object({ query = string })
  output_type = object({ answer = string })
}
research "static" "source" {
  model         = "test-model"
  system_prompt = "Collect evidence."
  tool_ids      = [external_tool.lookup.id]
  tool_call_quota = {
    (external_tool.lookup.id) = 1
  }
}
`, program)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(source), 0o600))
	opener := &externalQuotaOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	require.Len(t, opener.results, 5)
	assert.Contains(t, opener.description, "r42 per-session call quota: at most 1 accepted calls")
	assert.Contains(t, opener.description, "this quota will not reset during this session")
	assert.Contains(t, opener.description, "do not call this tool again")
	assert.Contains(t, opener.results[0], `"accepted":false`)
	assert.Contains(t, opener.results[1], `"accepted":false`)
	assert.Contains(t, opener.errors[2].Error(), "exit status 7")
	assert.Contains(t, opener.results[3], `"accepted":true`)
	quotaErr := opener.errors[4]
	require.Error(t, quotaErr)
	assert.Contains(t, quotaErr.Error(), "per-session call quota exhausted (limit 1 successful calls)")
	assert.Contains(t, quotaErr.Error(), "do not call this tool again")
}

//nolint:paralleltest // Helper process owns stdin/stdout and may terminate itself.
func TestExternalToolHelperProcess(t *testing.T) {
	if os.Getenv("R42_EXTERNAL_HELPER") != "1" {
		return
	}
	var input struct {
		Query string  `json:"query"`
		Limit float64 `json:"limit"`
	}
	if err := json.NewDecoder(bufio.NewReader(os.Stdin)).Decode(&input); err != nil {
		os.Exit(2)
	}
	if os.Getenv("R42_EXTERNAL_FAIL") == "1" || input.Query == "error" {
		_, _ = fmt.Fprint(os.Stdout, "failed stdout")
		_, _ = fmt.Fprint(os.Stderr, "complete failed stderr")
		os.Exit(7)
	}
	if input.Query == "reject" {
		_, _ = fmt.Fprint(os.Stdout, `{"accepted":false,"issues":[{"code":"retry","message":"try again"}]}`)
		os.Exit(0)
	}
	_, _ = fmt.Fprintf(os.Stdout, `{"accepted":true,"output":{"answer":"%s limit=%.0f"}}`, input.Query, input.Limit)
	os.Exit(0)
}

type externalCallingOpener struct{ result string }

type externalQuotaOpener struct {
	description string
	results     []string
	errors      []error
}

func (o *externalQuotaOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	return &externalQuotaSession{config: config, opener: o}, nil
}

type externalQuotaSession struct {
	config copilot.SessionConfig
	opener *externalQuotaOpener
}

func (s *externalQuotaSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	if handled, err := handleDefaultWorkflowProtocol(s.config); handled {
		return &sdk.SessionEvent{}, err
	}
	for _, tool := range s.config.Tools {
		if !strings.HasPrefix(tool.Name, "tool_external_tool_lookup_") {
			continue
		}
		s.opener.description = tool.Description
		for _, arguments := range []map[string]any{
			{},
			{"query": "reject"},
			{"query": "error"},
			{"query": "facts"},
			{"query": "facts"},
		} {
			result, err := tool.Handler(sdk.ToolInvocation{Arguments: arguments})
			s.opener.results = append(s.opener.results, result.TextResultForLLM)
			s.opener.errors = append(s.opener.errors, err)
		}
		return &sdk.SessionEvent{}, nil
	}
	return nil, assert.AnError
}

func (*externalQuotaSession) Close(context.Context) error { return nil }

func (o *externalCallingOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	return &externalCallingSession{config: config, opener: o}, nil
}

type externalCallingSession struct {
	config copilot.SessionConfig
	opener *externalCallingOpener
}

func (s *externalCallingSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	if handled, err := handleDefaultWorkflowProtocol(s.config); handled {
		return &sdk.SessionEvent{}, err
	}
	for _, tool := range s.config.Tools {
		if strings.HasPrefix(tool.Name, "tool_external_tool_lookup_") {
			result, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"query": "facts"}})
			s.opener.result = result.TextResultForLLM
			return &sdk.SessionEvent{}, err
		}
	}
	return nil, assert.AnError
}

func (*externalCallingSession) Close(context.Context) error { return nil }
