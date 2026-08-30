package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/lonegunmanb/r42/internal/executor"
	modulespec "github.com/lonegunmanb/r42/internal/module/spec"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

type runtimeResult struct {
	Outputs  map[string]cty.Value
	Warnings []error
}

func planRuntime(
	runtime *cli.Engine,
	ctx context.Context,
	directory string,
	variables []golden.CliFlagAssignedVariables,
) (*plan.Plan, error) {
	config, err := runtime.Config(directory, executor.ResearchConfigOptions{
		Context:   ctx,
		Variables: variables,
	})
	if err != nil {
		return nil, err
	}
	planned, err := executor.RunResearchPlan(config)
	if err != nil {
		return nil, err
	}
	return planned.SavedPlan(), nil
}

func applyRuntime(
	runtime *cli.Engine,
	ctx context.Context,
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (runtimeResult, error) {
	options.Context = ctx
	config, err := runtime.ConfigFromPlan(planned, options)
	if err != nil {
		return runtimeResult{}, err
	}
	err = config.Plan().Apply()
	return runtimeResult{Outputs: config.Outputs(), Warnings: config.Warnings()}, err
}

func TestProductionRuntimePlansAndAppliesGoldenVariablesWithoutStartingCopilot(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
variable "topic" { type = string }
output "summary" { value = "topic=${var.topic}" }
`), 0o600))
	runtime := cli.NewRuntime()
	planned, err := planRuntime(runtime, t.Context(), directory, []golden.CliFlagAssignedVariables{
		golden.NewCliFlagAssignedVariable("topic", `"markets"`),
	})

	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "saved.r42plan")
	_, err = plan.Save(path, planned)
	require.NoError(t, err)
	planned, err = plan.Load(path)
	require.NoError(t, err)

	result, err := applyRuntime(runtime, context.Background(), planned, executor.ResearchConfigOptions{Parallelism: 2})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("topic=markets"), result.Outputs["summary"])
	assert.Empty(t, result.Warnings)
}

func TestProductionRuntimeAppliesPlanFromSourceConfig(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
output "summary" { value = "planned-and-applied" }
`), 0o600))
	runtime := cli.NewRuntime()
	config, err := runtime.Config(directory, executor.ResearchConfigOptions{Context: t.Context()})
	require.NoError(t, err)
	planned, err := executor.RunResearchPlan(config)
	require.NoError(t, err)

	require.NoError(t, planned.Apply())
	assert.Equal(t, cty.StringVal("planned-and-applied"), config.Outputs()["summary"])
}

func TestProductionRuntimeAppliesTheBlockWorkingDirectoryReservedByPlan(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	runRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "source" {
  model         = "test-model"
  system_prompt = block_wd()
}
`), 0o600))
	opener := &fakeSessionOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	config, err := runtime.Config(directory, executor.ResearchConfigOptions{
		Context: t.Context(), RunDirectory: runRoot,
	})
	require.NoError(t, err)
	planned, err := executor.RunResearchPlan(config)
	require.NoError(t, err)
	saved := planned.SavedPlan()
	require.NotNil(t, saved)
	assert.NoDirExists(t, filepath.Join(runRoot, ".r42"))
	nodes := saved.Nodes()
	require.Len(t, nodes, 1)
	decoded, err := modulespec.DecodeResearchPlan(nodes[0].Config)
	require.NoError(t, err)
	plannedWorkingDirectory := decoded.Config.SystemPrompt
	encoded, err := plan.Marshal(saved)
	require.NoError(t, err)
	saved, err = plan.Unmarshal(encoded)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), saved, executor.ResearchConfigOptions{Parallelism: 1})
	require.NoError(t, err)
	require.Len(t, opener.configs, 3)
	workingDirectory := opener.configs[2].WorkingDirectory

	assert.Equal(t, plannedWorkingDirectory, filepath.ToSlash(workingDirectory))
	assert.DirExists(t, workingDirectory)
	assert.True(t, strings.HasSuffix(opener.configs[2].SystemPrompt, plannedWorkingDirectory))
}

func TestProductionRuntimeUsesPlanKnownFunctionOutputWithoutReevaluation(t *testing.T) {
	t.Setenv("R42_TEST_PLAN_OUTPUT", "planned-value")
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
output "summary" { value = env("R42_TEST_PLAN_OUTPUT") }
`), 0o600))
	runtime := cli.NewRuntime()
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)
	encoded, err := plan.Marshal(planned)
	require.NoError(t, err)
	planned, err = plan.Unmarshal(encoded)
	require.NoError(t, err)

	result, err := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("planned-value"), result.Outputs["summary"])
}

func TestProductionRuntimeRunsOneResearchSessionAndPublishesArtifacts(t *testing.T) {
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

research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
	collection_mcp_tool_ids = [
	  mcp_server.market_data.tool_ids["get_quote"],
	  mcp_server.market_data.tool_ids["get_kline"],
	]
	collection_mcp_resource_ids = [mcp_server.market_data.resource_ids["quote://codes"]]
	allowed_tools = ["web_search", mcp_server.market_data.tool_ids["get_quote"]]
	disallowed_tools = [mcp_server.market_data.tool_ids["get_quote"]]
	retry {
	  lifecycle_retries = 3
	}
	artifact "report" {
	  type = "file"
	  path = "report.md"
	  description = "Runtime report fixture"
	}
}
locals {
  report_with_retries = "${research.static.source.artifact.report.path}|${one(research.static.source.retry).lifecycle_retries}"
}
output "report_path" { value = local.report_with_retries }
output "report_id" { value = research.static.source.artifact.report.id }
`), 0o600))
	opener := &fakeSessionOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)
	encoded, err := plan.Marshal(planned)
	require.NoError(t, err)
	planned, err = plan.Unmarshal(encoded)
	require.NoError(t, err)

	result, err := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	require.Len(t, opener.configs, 3)
	require.Len(t, opener.configs[0].MCPServers, 1)
	assert.Equal(t, "market_data", opener.configs[0].MCPServers[0].Name)
	require.Len(t, opener.configs[0].MCPResources, 1)
	assert.Equal(t, "quote://codes", opener.configs[0].MCPResources[0].URI)
	assert.Contains(t, opener.configs[0].AvailableTools, "mcp:mcp_server.market_data-get_quote")
	assert.Contains(t, opener.configs[0].AvailableTools, "r42_read_information_needs")
	assert.NotContains(t, opener.configs[0].AvailableTools, "mcp:mcp_server.market_data-get_kline")
	assert.NotContains(t, opener.configs[0].AvailableTools, opener.configs[0].MCPServers[0].Tools[0])
	assert.Contains(t, opener.configs[0].ExcludedTools, "mcp:mcp_server.market_data-get_quote")
	assert.Contains(t, opener.configs[0].SystemPrompt, "An accepted r42_collection_checkpoint is the only completion condition for this session")
	assert.Contains(t, opener.configs[0].SystemPrompt, "The host will open a separate closed Research session")
	assert.NotContains(t, opener.configs[0].SystemPrompt, "submit_morning_evidence")
	assert.Empty(t, opener.configs[1].MCPServers)
	assert.Empty(t, opener.configs[2].MCPServers)
	assert.NotContains(t, opener.configs[2].AvailableTools, "mcp:mcp_server.market_data-get_quote")
	assert.NotContains(t, opener.configs[2].ExcludedTools, "mcp:mcp_server.market_data-get_quote")
	assert.Equal(t, "test-model", opener.configs[2].Model)
	assert.Contains(t, opener.configs[2].SystemPrompt, "Collect evidence.")
	assert.True(t, strings.HasPrefix(opener.configs[2].SystemPrompt, "You are the closed Research synthesis phase"))
	assert.Equal(t, 1, opener.session.sendCalls)
	assert.Equal(t, 3, opener.session.closeCalls)
	path, retries, ok := strings.Cut(result.Outputs["report_path"].AsString(), "|")
	require.True(t, ok)
	assert.True(t, filepath.IsAbs(path))
	assert.Equal(t, "report.md", filepath.Base(path))
	assert.Equal(t, "3", retries)
	assert.Regexp(t, `^artifact-[0-9a-f-]{36}$`, result.Outputs["report_id"].AsString())
}

func TestProductionRuntimePreservesExplicitEmptyMCPAllowlist(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
mcp_server "market_data" {
  tools = ["get_quote"]
  http { url = "https://mcp.example.test/mcp" }
}

research "static" "source" {
  model                   = "test-model"
  system_prompt           = "Collect evidence."
  collection_mcp_tool_ids = [mcp_server.market_data.tool_ids["get_quote"]]
  allowed_tools           = []
}
`), 0o600))
	opener := &fakeSessionOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)
	encoded, err := plan.Marshal(planned)
	require.NoError(t, err)
	planned, err = plan.Unmarshal(encoded)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	require.Len(t, opener.configs, 3)
	assert.NotNil(t, opener.configs[0].AvailableTools)
	assert.Contains(t, opener.configs[0].AvailableTools, "r42_read_information_needs")
	assert.NotContains(t, opener.configs[0].AvailableTools, "mcp:mcp_server.market_data-get_quote")
}

func TestProductionRuntimeExecutesTerminalGoTool(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
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
research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  terminate_tool_id = go_tool.finish.id
}
output "summary" { value = research.static.source.result }
`), 0o600))
	opener := &toolCallingOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)
	encoded, err := plan.Marshal(planned)
	require.NoError(t, err)
	planned, err = plan.Unmarshal(encoded)
	require.NoError(t, err)

	result, err := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("done"), result.Outputs["summary"])
	assert.True(t, strings.HasPrefix(opener.calledTool, "tool_go_tool_finish_"))
}

func TestProductionRuntimeReturnsNullForOmittedTerminalOutput(t *testing.T) {
	t.Parallel()
	directory := writeTerminalToolFixture(t)
	opener := &repairingToolOpener{omitOutput: true}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	result, err := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.True(t, result.Outputs["summary"].RawEquals(cty.NullVal(cty.String)))
}

func TestProductionRuntimeReturnsRepairableToolArgumentIssuesToSameSession(t *testing.T) {
	t.Parallel()
	directory := writeTerminalToolFixture(t)
	opener := &repairingToolOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	result, err := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("fixed"), result.Outputs["summary"])
	assert.Contains(t, opener.firstResult, `"accepted":false`)
	assert.Contains(t, opener.firstResult, "invalid_arguments")
	assert.Equal(t, 2, opener.sendCalls)
}

func TestProductionRuntimeEvaluatesKnownAfterApplyLocalsAndFunctions(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
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
research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  terminate_tool_id = go_tool.finish.id
}
locals { summary = upper(research.static.source.result) }
output "summary" { value = local.summary }
`), 0o600))
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: &toolCallingOpener{}})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)
	encoded, err := plan.Marshal(planned)
	require.NoError(t, err)
	planned, err = plan.Unmarshal(encoded)
	require.NoError(t, err)

	result, err := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("DONE"), result.Outputs["summary"])
}

func TestProductionRuntimeRecordsFailedGoToolInDebugLog(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
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
research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  tool_ids = [go_tool.lookup.id]
}
`), 0o600))
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: &failingGoToolOpener{}})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1, Debug: true})

	require.Error(t, err)
	events := readOnlyRunEvents(t, directory)
	assert.Contains(t, events, `"tool_name":"tool_go_tool_lookup_`)
	assert.Contains(t, events, `"tool_address":"go_tool.lookup"`)
	assert.Contains(t, events, `"Query":"facts"`)
	assert.Contains(t, events, "lookup failed completely")
	assert.Contains(t, events, `"action":"session.send","status":"failed"`)
	assert.Contains(t, events, `"action":"block.apply","status":"failed"`)
	assert.Contains(t, events, `"action":"apply.golden.apply","status":"failed"`)
	assert.Contains(t, events, `"action":"apply","status":"failed"`)
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
	if handled, err := handleDefaultWorkflowProtocol(s.config); handled {
		return &sdk.SessionEvent{}, err
	}
	for _, tool := range s.config.Tools {
		if strings.HasPrefix(tool.Name, "tool_go_tool_lookup_") {
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
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
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
research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  terminate_tool_id = go_tool.finish.id
}
output "summary" { value = research.static.source.result }
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
	if handled, err := handleDefaultWorkflowProtocol(s.config); handled {
		return &sdk.SessionEvent{}, err
	}
	s.opener.sendCalls++
	for _, tool := range s.config.Tools {
		if !strings.HasPrefix(tool.Name, "tool_go_tool_finish_") {
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
	return &protocolFixtureSession{config: config, session: &o.session}, nil
}

type protocolFixtureSession struct {
	config  copilot.SessionConfig
	session cli.Session
}

type recoveringProtocolFixtureSession struct {
	*protocolFixtureSession
	recovery interface {
		Abort(context.Context) error
		Resume(context.Context) error
	}
}

func (s *protocolFixtureSession) SendAndWait(ctx context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	if handled, err := handleDefaultWorkflowProtocol(s.config); handled {
		return &sdk.SessionEvent{}, err
	}
	return s.session.SendAndWait(ctx, options)
}

func (s *protocolFixtureSession) Close(ctx context.Context) error { return s.session.Close(ctx) }

func (s *recoveringProtocolFixtureSession) Abort(ctx context.Context) error {
	return s.recovery.Abort(ctx)
}

func (s *recoveringProtocolFixtureSession) Resume(ctx context.Context) error {
	return s.recovery.Resume(ctx)
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
	if handled, err := handleDefaultWorkflowProtocol(s.config); handled {
		return &sdk.SessionEvent{}, err
	}
	for _, tool := range s.config.Tools {
		if strings.HasPrefix(tool.Name, "tool_go_tool_finish_") {
			s.opener.calledTool = tool.Name
			_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"Summary": "done"}})
			return &sdk.SessionEvent{}, err
		}
	}
	return nil, assert.AnError
}

func handleDefaultWorkflowProtocol(config copilot.SessionConfig) (bool, error) {
	for _, tool := range config.Tools {
		switch tool.Name {
		case "r42_set_information_needs":
			_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
				"information_needs": []any{map[string]any{
					"question":        "test fixture need",
					"stop_conditions": []any{map[string]any{"condition": "test fixture condition"}},
				}},
			}})
			if err != nil {
				return true, err
			}
		case "r42_collection_checkpoint":
			_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
				"empty_reason": "test fixture has no acquisition work",
				"need_dispositions": []any{map[string]any{
					"information_need_id": "NEED-001", "search_disposition": "stalled",
				}},
			}})
			return true, err
		case "r42_collection_qc_verdict":
			_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{
				"assessments": []any{map[string]any{
					"information_need_id": "NEED-001", "status": "sufficient",
					"unsatisfied_condition_ids": []any{}, "evidence_progress": "none",
				}},
			}})
			return true, err
		case "r42_qc_verdict":
			_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"decision": "pass"}})
			return true, err
		}
	}
	return false, nil
}

func workflowSessionKind(config copilot.SessionConfig) string {
	for _, tool := range config.Tools {
		switch tool.Name {
		case "r42_collection_checkpoint":
			return "collection"
		case "r42_collection_qc_verdict":
			return "collection_qc"
		case "r42_qc_verdict":
			return "final_qc"
		}
	}
	return "research"
}

func (*toolCallingSession) Close(context.Context) error { return nil }
