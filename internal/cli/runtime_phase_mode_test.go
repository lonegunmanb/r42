package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/tool/starlarktool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductionRuntimeCollectionOnlyUsesOneCollectionSession(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
mcp_server "market_data" {
  tools = ["get_quote", "get_kline"]
  resources = ["quote://codes"]
  http {
    url              = "https://mcp.example.test/mcp"
    bearer_token_ref = "MARKET_DATA_API_KEY"
  }
}

go_tool "submit" {
  description = "Submit the completed DCF."
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

research "static" "builder" {
  phase_mode    = "collection_only"
  model         = "test-model"
  system_prompt = "Build a DCF."
  collection_mcp_tool_ids = [
    mcp_server.market_data.tool_ids["get_quote"],
    mcp_server.market_data.tool_ids["get_kline"],
  ]
	collection_mcp_resource_ids = [mcp_server.market_data.resource_ids["quote://codes"]]
	allowed_tools = ["web_search", mcp_server.market_data.tool_ids["get_quote"], go_tool.submit.id]

  artifact "sources" {
    type        = "directory"
    path        = "sources"
    description = "Saved source material"
  }

  tool_use "submit" {
    tool_id   = go_tool.submit.id
    terminate = true
  }
}
`), 0o600))

	opener := &collectionOnlyOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1, Debug: true})

	require.NoError(t, err)
	require.Len(t, opener.configs, 1)
	require.Len(t, opener.configs[0].MCPServers, 1)
	assert.Equal(t, "market_data", opener.configs[0].MCPServers[0].Name)
	require.Len(t, opener.configs[0].MCPResources, 1)
	assert.Equal(t, "quote://codes", opener.configs[0].MCPResources[0].URI)
	assert.Contains(t, opener.configs[0].AvailableTools, "mcp:mcp_server.market_data-get_quote")
	assert.NotContains(t, opener.configs[0].AvailableTools, "mcp:mcp_server.market_data-get_kline")
	assert.Contains(t, opener.configs[0].SystemPrompt, "sole Collection session")
	assert.NotContains(t, toolNamesFromConfig(opener.configs[0]), "r42_set_information_needs")
	assert.NotContains(t, toolNamesFromConfig(opener.configs[0]), "r42_collection_checkpoint")
	assert.NotContains(t, toolNamesFromConfig(opener.configs[0]), "r42_collection_qc_verdict")
	assert.Equal(t, 1, opener.session.closeCalls)
	assert.True(t, opener.session.savedArtifact)
	assert.Equal(t, []string{"collection"}, workflowDebugSessionOpens(t, directory))
}

type collectionOnlyOpener struct {
	configs []copilot.SessionConfig
	session collectionOnlySession
}

func (o *collectionOnlyOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	o.configs = append(o.configs, config)
	o.session.config = config
	return &o.session, nil
}

type collectionOnlySession struct {
	config        copilot.SessionConfig
	closeCalls    int
	savedArtifact bool
}

func (s *collectionOnlySession) SendAndWait(_ context.Context, _ sdk.MessageOptions) (*sdk.SessionEvent, error) {
	for _, tool := range s.config.Tools {
		if tool.Name == "r42_save_artifact" {
			result, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
				"artifact_path": "sources/source.md", "content": "saved source material", "source": "https://example.test/source",
			}})
			if err != nil {
				return nil, err
			}
			s.savedArtifact = strings.Contains(result.TextResultForLLM, "artifact-")
			break
		}
	}
	for _, tool := range s.config.Tools {
		if strings.HasPrefix(tool.Name, "tool_go_tool_submit_") {
			_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"Summary": "complete"}})
			return &sdk.SessionEvent{}, err
		}
	}
	return &sdk.SessionEvent{}, nil
}

func (s *collectionOnlySession) Close(context.Context) error {
	s.closeCalls++
	return nil
}

func TestProductionRuntimeResearchOnlyOpensOnlyResearchSession(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "juror" {
  phase_mode    = "research_only"
  model         = "test-model"
  system_prompt = "Review the frozen DCF."
  prompt        = "Return a verdict."
}
`), 0o600))

	opener := &qcOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1, Debug: true})

	require.NoError(t, err)
	require.Len(t, opener.configs, 1)
	assert.NotContains(t, toolNamesFromConfig(opener.configs[0]), "r42_set_information_needs")
	assert.NotContains(t, toolNamesFromConfig(opener.configs[0]), "r42_collection_checkpoint")
	assert.NotContains(t, toolNamesFromConfig(opener.configs[0]), "r42_collection_qc_verdict")
	assert.Contains(t, opener.configs[0].SystemPrompt, "closed Research synthesis phase")
	assert.NotContains(t, opener.configs[0].SystemPrompt, "Collection phase")
	assert.Equal(t, 1, opener.research.sendCalls)
	assert.Equal(t, 1, opener.research.closeCalls)
	assert.Zero(t, opener.collection.sendCalls)
	assert.Zero(t, opener.collectionQC.sendCalls)
	assert.Equal(t, []string{"research"}, workflowDebugSessionOpens(t, directory))
}

func TestProductionRuntimeResearchOnlyPreservesFinalQCRevision(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "juror" {
  phase_mode    = "research_only"
  model         = "test-model"
  system_prompt = "Review the frozen DCF."
  prompt        = "Return a verdict."

  qc { criteria = { accuracy = "Be accurate." } }
}
`), 0o600))

	opener := &revisionQCOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1, Debug: true})

	require.NoError(t, err)
	assert.Equal(t, 1, opener.research.sendCalls)
	assert.Equal(t, 2, opener.qc.sendCalls)
	assert.Len(t, opener.research.prompts, 1)
	assert.Equal(t, 1, opener.research.closeCalls)
	assert.Equal(t, 1, opener.qc.closeCalls)
	assert.Zero(t, opener.collection.sendCalls)
	assert.Zero(t, opener.collectionQC.sendCalls)
	assert.Equal(t, []string{"research", "final_qc"}, workflowDebugSessionOpens(t, directory))
}

func TestProductionRuntimeDynamicResearchOnlyOpensOnlyResearchSessions(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "dynamic" "jurors" {
  tasks = [for prompt in ["alpha", "beta"] : {
    phase_mode    = "research_only"
    model         = "test-model"
    system_prompt = "Review the frozen DCF."
    prompt        = prompt
    artifact      = {}
    retry         = null
    qc            = null
  }]
}
`), 0o600))

	opener := &dynamicTestOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2, Debug: true})

	require.NoError(t, err)
	configs := opener.Configs()
	require.Len(t, configs, 2)
	for _, config := range configs {
		assert.NotContains(t, toolNamesFromConfig(config), "r42_set_information_needs")
		assert.NotContains(t, toolNamesFromConfig(config), "r42_collection_checkpoint")
		assert.NotContains(t, toolNamesFromConfig(config), "r42_collection_qc_verdict")
	}
	assert.ElementsMatch(t, []string{"alpha", "beta"}, opener.Prompts())
	assert.ElementsMatch(t, []string{"research", "research"}, workflowDebugSessionOpens(t, directory))
}

func TestProductionRuntimeDynamicResearchOnlyPreservesFinalQCRevision(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "dynamic" "jurors" {
  serial = true
  tasks = [for prompt in ["alpha", "beta"] : {
    phase_mode    = "research_only"
    model         = "test-model"
    system_prompt = "Review the frozen DCF."
    prompt        = prompt
    artifact      = {}
    retry         = null
    qc            = { criteria = { accuracy = "Be accurate." } }
  }]
}

`), 0o600))

	opener := &dynamicQCIssueOpener{finalRounds: map[string]int{}, handoffPrompts: map[string]string{}}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1, Debug: true})

	require.NoError(t, err)
	require.Len(t, opener.finalRounds, 2)
	for workspace, rounds := range opener.finalRounds {
		assert.Equal(t, 2, rounds)
		assert.Empty(t, opener.handoffPrompts[workspace])
	}
	assert.ElementsMatch(t, []string{"research", "final_qc", "research", "final_qc"}, workflowDebugSessionOpens(t, directory))
}

func TestProductionRuntimeDynamicCollectionOnlyOpensOnlyCollectionSessions(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
go_tool "submit" {
  description = "Submit the completed DCF."
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

research "dynamic" "builders" {
  tasks = [for prompt in ["alpha", "beta"] : {
    phase_mode    = "collection_only"
    model         = "test-model"
    system_prompt = "Build a DCF."
    prompt        = prompt
    artifact      = {}
    retry         = null
    qc            = null
    tool_use = {
      submit = {
        tool_id          = go_tool.submit.id
        terminate        = true
        input            = {}
        input_from_agent = {}
      }
    }
  }]
}
`), 0o600))

	opener := &dynamicTestOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2, Debug: true})

	require.NoError(t, err)
	assert.Equal(t, []string{"collection", "collection"}, workflowDebugSessionOpens(t, directory))
	assert.ElementsMatch(t, []string{"alpha", "beta"}, opener.Prompts())
}

func TestProductionRuntimeCollectionOnlyRepairsRejectedTerminalCallInPersistentSession(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(collectionOnlySubmitFixture("")), 0o600))

	opener := &terminalRepairOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, 2, opener.session.sendCalls)
	require.Len(t, opener.session.prompts, 2)
	assert.Contains(t, opener.session.prompts[1], "rejected its last call")
	assert.Equal(t, 1, opener.session.closeCalls)
}

func TestProductionRuntimeCollectionOnlyStopsSessionOnCancellationAndTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		timeout       string
		cancel        bool
		expectedError error
	}{
		{name: "cancellation", cancel: true, expectedError: context.Canceled},
		{name: "timeout", timeout: `timeout = "5s"`, expectedError: context.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(collectionOnlySubmitFixture(tt.timeout)), 0o600))

			opener := newBlockingCollectionOnlyOpener()
			runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
			planned, err := planRuntime(runtime, t.Context(), directory, nil)
			require.NoError(t, err)

			ctx := t.Context()
			cancel := func() {}
			if tt.cancel {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, applyErr := applyRuntime(runtime, ctx, planned, executor.ResearchConfigOptions{Parallelism: 1})
				result <- applyErr
			}()
			select {
			case <-opener.started:
			case applyErr := <-result:
				require.NoError(t, applyErr, "Collection session did not open before the block ended")
				return
			}
			if tt.cancel {
				cancel()
			}

			err = <-result
			require.ErrorIs(t, err, tt.expectedError)
			assert.Equal(t, 1, opener.session.closeCalls)
		})
	}
}

func TestProductionRuntimeCollectionOnlyRepairsStarlarkStepLimit(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
starlark_tool "calculator" {
  description = "Calculate values."
  max_steps   = 100
}

go_tool "submit" {
  description = "Submit the completed DCF."
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

research "static" "builder" {
  phase_mode          = "collection_only"
  model               = "test-model"
  system_prompt       = "Build a DCF."
  collection_tool_ids = [starlark_tool.calculator.id]

  tool_use "submit" {
    tool_id   = go_tool.submit.id
    terminate = true
  }
}
`), 0o600))

	runner := &phaseModeStarlarkRunner{responses: []starlarktool.WorkerResponse{
		{Error: &starlarktool.WorkerError{Code: "starlark_step_limit", Message: "evaluation exceeded max_steps"}},
		{Result: &starlarktool.Result{ResultJSON: `{"total":42}`, Steps: 9}},
	}}
	opener := &starlarkRepairOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener, StarlarkRunner: runner})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, 2, opener.session.sendCalls)
	assert.Contains(t, opener.session.starlarkResponses[0], `"accepted":false`)
	assert.Contains(t, opener.session.starlarkResponses[0], `"code":"starlark_step_limit"`)
	assert.Contains(t, opener.session.starlarkResponses[1], `"accepted":true`)
	assert.Len(t, runner.requests, 2)
}

func workflowDebugSessionOpens(t *testing.T, directory string) []string {
	t.Helper()

	var sessions []string
	for line := range strings.SplitSeq(strings.TrimSpace(readOnlyRunEvents(t, directory)), "\n") {
		var event struct {
			Action  string `json:"action"`
			Status  string `json:"status"`
			Session string `json:"session"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		if event.Action == "session.open" && event.Status == "started" {
			sessions = append(sessions, event.Session)
		}
	}
	return sessions
}

func collectionOnlySubmitFixture(timeout string) string {
	return `
go_tool "submit" {
  description = "Submit the completed DCF."
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

research "static" "builder" {
  phase_mode    = "collection_only"
  model         = "test-model"
  system_prompt = "Build a DCF."
  ` + timeout + `

  tool_use "submit" {
    tool_id   = go_tool.submit.id
    terminate = true
  }
}
`
}

type terminalRepairOpener struct{ session terminalRepairSession }

func (o *terminalRepairOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	o.session.config = config
	return &o.session, nil
}

type terminalRepairSession struct {
	config                copilot.SessionConfig
	sendCalls, closeCalls int
	prompts               []string
}

func (s *terminalRepairSession) SendAndWait(_ context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.sendCalls++
	s.prompts = append(s.prompts, options.Prompt)
	for _, tool := range s.config.Tools {
		if !strings.HasPrefix(tool.Name, "tool_go_tool_submit_") {
			continue
		}
		arguments := map[string]any{}
		if s.sendCalls > 1 {
			arguments["Summary"] = "complete"
		}
		_, err := tool.Handler(sdk.ToolInvocation{Arguments: arguments})
		return &sdk.SessionEvent{}, err
	}
	return nil, assert.AnError
}

func (s *terminalRepairSession) Close(context.Context) error {
	s.closeCalls++
	return nil
}

type blockingCollectionOnlyOpener struct {
	started chan struct{}
	session blockingCollectionOnlySession
}

func newBlockingCollectionOnlyOpener() *blockingCollectionOnlyOpener {
	started := make(chan struct{})
	return &blockingCollectionOnlyOpener{started: started, session: blockingCollectionOnlySession{started: started}}
}

func (o *blockingCollectionOnlyOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	o.session.config = config
	return &o.session, nil
}

type blockingCollectionOnlySession struct {
	config     copilot.SessionConfig
	started    chan struct{}
	closeCalls int
}

func (s *blockingCollectionOnlySession) SendAndWait(ctx context.Context, _ sdk.MessageOptions) (*sdk.SessionEvent, error) {
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *blockingCollectionOnlySession) Close(context.Context) error {
	s.closeCalls++
	return nil
}

type phaseModeStarlarkRunner struct {
	responses []starlarktool.WorkerResponse
	requests  []starlarktool.WorkerRequest
}

func (r *phaseModeStarlarkRunner) Run(_ context.Context, request starlarktool.WorkerRequest) (starlarktool.WorkerResponse, error) {
	r.requests = append(r.requests, request)
	response := r.responses[0]
	r.responses = r.responses[1:]
	return response, nil
}

type starlarkRepairOpener struct{ session starlarkRepairSession }

func (o *starlarkRepairOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	o.session.config = config
	return &o.session, nil
}

type starlarkRepairSession struct {
	config            copilot.SessionConfig
	sendCalls         int
	prompts           []string
	starlarkResponses []string
}

func (s *starlarkRepairSession) SendAndWait(_ context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.sendCalls++
	s.prompts = append(s.prompts, options.Prompt)
	for _, tool := range s.config.Tools {
		if strings.Contains(tool.Name, "starlark") {
			response, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
				"code": "result = {\"total\": 42}", "data_json": "null",
			}})
			if err != nil {
				return nil, err
			}
			s.starlarkResponses = append(s.starlarkResponses, response.TextResultForLLM)
			break
		}
	}
	if s.sendCalls == 1 {
		return &sdk.SessionEvent{}, nil
	}
	for _, tool := range s.config.Tools {
		if strings.HasPrefix(tool.Name, "tool_go_tool_submit_") {
			_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"Summary": "complete"}})
			return &sdk.SessionEvent{}, err
		}
	}
	return nil, assert.AnError
}

func (*starlarkRepairSession) Close(context.Context) error { return nil }
