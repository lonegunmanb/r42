package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Azure/golden"
	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestProductionRuntimePlansAndAppliesGoldenVariablesWithoutStartingCopilot(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42"), []byte(`
variable "topic" { type = string }
output "summary" { value = "topic=${var.topic}" }
`), 0o600))
	runtime := cli.NewRuntime()
	planned, err := runtime.Plan(t.Context(), directory, []golden.CliFlagAssignedVariables{
		golden.NewCliFlagAssignedVariable("topic", `"markets"`),
	})
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "saved.r42plan")
	_, err = plan.Save(path, planned)
	require.NoError(t, err)
	planned, err = plan.Load(path)
	require.NoError(t, err)

	result, err := runtime.Apply(context.Background(), planned, cli.ApplyOptions{Parallelism: 2})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("topic=markets"), result.Outputs["summary"])
	assert.Empty(t, result.Warnings)
}

func TestProductionRuntimeUsesPlanKnownFunctionOutputWithoutReevaluation(t *testing.T) {
	t.Setenv("R42_TEST_PLAN_OUTPUT", "planned-value")
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42"), []byte(`
output "summary" { value = env("R42_TEST_PLAN_OUTPUT") }
`), 0o600))
	runtime := cli.NewRuntime()
	planned, err := runtime.Plan(t.Context(), directory, nil)
	require.NoError(t, err)
	encoded, err := plan.Marshal(planned)
	require.NoError(t, err)
	planned, err = plan.Unmarshal(encoded)
	require.NoError(t, err)

	result, err := runtime.Apply(t.Context(), planned, cli.ApplyOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("planned-value"), result.Outputs["summary"])
}

func TestProductionRuntimeRunsOneResearchSessionAndPublishesArtifacts(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42"), []byte(`
research "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
	artifact "report" {
	  type = "file"
	  path = "report.md"
	}
}
output "report_path" { value = research.source.artifacts.report.path }
`), 0o600))
	opener := &fakeSessionOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := runtime.Plan(t.Context(), directory, nil)
	require.NoError(t, err)
	encoded, err := plan.Marshal(planned)
	require.NoError(t, err)
	planned, err = plan.Unmarshal(encoded)
	require.NoError(t, err)

	result, err := runtime.Apply(t.Context(), planned, cli.ApplyOptions{Parallelism: 1})

	require.NoError(t, err)
	require.Len(t, opener.configs, 1)
	assert.Equal(t, "test-model", opener.configs[0].Model)
	assert.Contains(t, opener.configs[0].SystemPrompt, "Collect evidence.")
	assert.True(t, strings.HasPrefix(opener.configs[0].SystemPrompt, "You are executing"))
	assert.Equal(t, 1, opener.session.sendCalls)
	assert.Equal(t, 1, opener.session.closeCalls)
	path := result.Outputs["report_path"].AsString()
	assert.True(t, filepath.IsAbs(path))
	assert.Equal(t, "report.md", filepath.Base(path))
}

func TestProductionRuntimeExecutesTerminalGoTool(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42"), []byte(`
go_tool "finish" {
  description = "Finish research"
  source = <<-GO
    import "context"
    type Input struct { Summary string }
    type Output string
    func Invoke(ctx context.Context, input Input) (ToolResponse[Output], error) {
      _ = ctx
      output := Output(input.Summary)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }
  GO
}
research "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  terminate_tool = go_tool.finish
}
output "summary" { value = research.source.result }
`), 0o600))
	opener := &toolCallingOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := runtime.Plan(t.Context(), directory, nil)
	require.NoError(t, err)
	encoded, err := plan.Marshal(planned)
	require.NoError(t, err)
	planned, err = plan.Unmarshal(encoded)
	require.NoError(t, err)

	result, err := runtime.Apply(t.Context(), planned, cli.ApplyOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("done"), result.Outputs["summary"])
	assert.Equal(t, "go_tool_finish", opener.calledTool)
}

func TestProductionRuntimeReturnsNullForOmittedTerminalOutput(t *testing.T) {
	t.Parallel()
	directory := writeTerminalToolFixture(t)
	opener := &repairingToolOpener{omitOutput: true}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := runtime.Plan(t.Context(), directory, nil)
	require.NoError(t, err)

	result, err := runtime.Apply(t.Context(), planned, cli.ApplyOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.True(t, result.Outputs["summary"].RawEquals(cty.NullVal(cty.String)))
}

func TestProductionRuntimeReturnsRepairableToolArgumentIssuesToSameSession(t *testing.T) {
	t.Parallel()
	directory := writeTerminalToolFixture(t)
	opener := &repairingToolOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := runtime.Plan(t.Context(), directory, nil)
	require.NoError(t, err)

	result, err := runtime.Apply(t.Context(), planned, cli.ApplyOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("fixed"), result.Outputs["summary"])
	assert.Contains(t, opener.firstResult, `"accepted":false`)
	assert.Contains(t, opener.firstResult, "invalid_arguments")
	assert.Equal(t, 2, opener.sendCalls)
}

func TestProductionRuntimeEvaluatesKnownAfterApplyLocalsAndFunctions(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42"), []byte(`
go_tool "finish" {
  description = "Finish research"
  source = <<-GO
    import "context"
    type Input struct { Summary string }
    type Output string
    func Invoke(ctx context.Context, input Input) (ToolResponse[Output], error) {
      _ = ctx
      output := Output(input.Summary)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }
  GO
}
research "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  terminate_tool = go_tool.finish
}
locals { summary = upper(research.source.result) }
output "summary" { value = local.summary }
`), 0o600))
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: &toolCallingOpener{}})
	planned, err := runtime.Plan(t.Context(), directory, nil)
	require.NoError(t, err)
	encoded, err := plan.Marshal(planned)
	require.NoError(t, err)
	planned, err = plan.Unmarshal(encoded)
	require.NoError(t, err)

	result, err := runtime.Apply(t.Context(), planned, cli.ApplyOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("DONE"), result.Outputs["summary"])
}

func TestProductionRuntimeRecordsFailedGoToolInDebugLog(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42"), []byte(`
go_tool "lookup" {
  description = "Look up evidence"
  source = <<-GO
    import (
      "context"
      "errors"
    )
    type Input struct { Query string }
    type Output string
    func Invoke(context.Context, Input) (ToolResponse[Output], error) {
      return ToolResponse[Output]{}, errors.New("lookup failed completely")
    }
  GO
}
research "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  tools = [go_tool.lookup]
}
`), 0o600))
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: &failingGoToolOpener{}})
	planned, err := runtime.Plan(t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = runtime.Apply(t.Context(), planned, cli.ApplyOptions{Parallelism: 1, Debug: true})

	require.Error(t, err)
	events := readOnlyRunEvents(t, directory)
	assert.Contains(t, events, `"tool_name":"go_tool_lookup"`)
	assert.Contains(t, events, `"Query":"facts"`)
	assert.Contains(t, events, "lookup failed completely")
}

func readOnlyRunEvents(t *testing.T, directory string) string {
	t.Helper()
	runs, err := os.ReadDir(filepath.Join(directory, ".r42", "runs"))
	require.NoError(t, err)
	require.Len(t, runs, 1)
	events, err := os.ReadFile(filepath.Join(directory, ".r42", "runs", runs[0].Name(), "events.jsonl"))
	require.NoError(t, err)
	return string(events)
}

type failingGoToolOpener struct{}

func (*failingGoToolOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	return &failingGoToolSession{config: config}, nil
}

type failingGoToolSession struct{ config copilot.SessionConfig }

func (s *failingGoToolSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	for _, tool := range s.config.Tools {
		if tool.Name == "go_tool_lookup" {
			_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"Query": "facts"}})
			return &sdk.SessionEvent{}, err
		}
	}
	return nil, assert.AnError
}

func (*failingGoToolSession) Close(context.Context) error { return nil }

func writeTerminalToolFixture(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42"), []byte(`
go_tool "finish" {
  description = "Finish research"
  source = <<-GO
    import "context"
    type Input struct { Summary string }
    type Output string
    func Invoke(ctx context.Context, input Input) (ToolResponse[Output], error) {
      _ = ctx
      if input.Summary == "none" {
        return ToolResponse[Output]{Accepted: true}, nil
      }
      output := Output(input.Summary)
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }
  GO
}
research "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  terminate_tool = go_tool.finish
}
output "summary" { value = research.source.result }
`), 0o600))
	return directory
}

type repairingToolOpener struct {
	omitOutput  bool
	firstResult string
	sendCalls   int
}

func (o *repairingToolOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	return &repairingToolSession{config: config, opener: o}, nil
}

type repairingToolSession struct {
	config copilot.SessionConfig
	opener *repairingToolOpener
}

func (s *repairingToolSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.opener.sendCalls++
	for _, tool := range s.config.Tools {
		if tool.Name != "go_tool_finish" {
			continue
		}
		arguments := map[string]any{"Summary": "fixed"}
		if s.opener.omitOutput {
			arguments["Summary"] = "none"
		} else if s.opener.sendCalls == 1 {
			arguments = map[string]any{}
		}
		result, err := tool.Handler(sdk.ToolInvocation{Arguments: arguments})
		if s.opener.sendCalls == 1 {
			s.opener.firstResult = result.TextResultForLLM
		}
		return &sdk.SessionEvent{}, err
	}
	return nil, assert.AnError
}

func (*repairingToolSession) Close(context.Context) error { return nil }

type fakeSessionOpener struct {
	mu      sync.Mutex
	configs []copilot.SessionConfig
	session fakeSession
}

func (o *fakeSessionOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	o.mu.Lock()
	o.configs = append(o.configs, config)
	o.mu.Unlock()
	return &o.session, nil
}

type fakeSession struct {
	mu                    sync.Mutex
	sendCalls, closeCalls int
	closeErr              error
}

func (s *fakeSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	s.mu.Unlock()
	return &sdk.SessionEvent{}, nil
}

func (s *fakeSession) Close(context.Context) error {
	s.mu.Lock()
	s.closeCalls++
	s.mu.Unlock()
	return s.closeErr
}

type toolCallingOpener struct{ calledTool string }

func (o *toolCallingOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	return &toolCallingSession{config: config, opener: o}, nil
}

type toolCallingSession struct {
	config copilot.SessionConfig
	opener *toolCallingOpener
}

func (s *toolCallingSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	for _, tool := range s.config.Tools {
		if tool.Name == "go_tool_finish" {
			s.opener.calledTool = tool.Name
			_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"Summary": "done"}})
			return &sdk.SessionEvent{}, err
		}
	}
	return nil, assert.AnError
}

func (*toolCallingSession) Close(context.Context) error { return nil }
