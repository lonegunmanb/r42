package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestProductionRuntimeMaterializesUnknownDynamicTasksDuringApply(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
go_tool "finish" {
  description = "Return topics for follow-up research."
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

research "static" "seed" {
  model             = "test-model"
  system_prompt     = "Generate follow-up topics."
  terminate_tool_id = go_tool.finish.id
}

research "dynamic" "followups" {
  tasks = [
    for topic in jsondecode(research.static.seed.result) : {
      model         = "test-model"
      system_prompt = "Research the assigned topic."
      prompt        = topic
	  terminate_tool_id = go_tool.finish.id
      artifact      = {}
      retry         = null
      qc            = null
    }
  ]
}

output "followup_results" {
  value = [for task in research.dynamic.followups.tasks : task.result]
}
`), 0o600))

	opener := &dynamicTestOpener{topics: []string{"alpha", "beta"}}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := applyRuntime(runtime, ctx, planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.True(t, result.Outputs["followup_results"].RawEquals(cty.TupleVal([]cty.Value{
		cty.StringVal("alpha"),
		cty.StringVal("beta"),
	})))
	assert.ElementsMatch(t, []string{"alpha", "beta"}, opener.Prompts())
}

func TestProductionRuntimeResolvesStaticPromptAfterDynamicResearch(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
go_tool "finish" {
  description = "Return the submitted summary."
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

research "static" "seed" {
  model             = "test-model"
  system_prompt     = "Generate follow-up topics."
  terminate_tool_id = go_tool.finish.id
}

research "dynamic" "followups" {
  tasks = [
    for topic in jsondecode(research.static.seed.result) : {
      model             = "test-model"
      system_prompt     = "Research the assigned topic."
      prompt            = topic
      terminate_tool_id = go_tool.finish.id
      artifact          = {}
      retry             = null
      qc                = null
    }
  ]
}

research "static" "summary" {
  model             = "test-model"
  system_prompt     = "Summarize completed follow-ups."
  prompt            = join(",", [for task in research.dynamic.followups.tasks : task.result])
  terminate_tool_id = go_tool.finish.id
}

output "summary" {
  value = research.static.summary.result
}
`), 0o600))

	opener := &dynamicTestOpener{topics: []string{"alpha", "beta"}}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	result, err := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2})

	require.NoError(t, err)
	assert.Equal(t, "alpha,beta", result.Outputs["summary"].AsString())
	assert.Contains(t, opener.Prompts(), "alpha,beta")
}

func TestProductionRuntimeResolvesDeferredStaticArtifactID(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
go_tool "finish" {
  description = "Finish research."
  source = <<-GO
    import "context"
    type Input struct { Summary string; ArtifactID *string }
    type Output string
    func Invoke(ctx context.Context, input Input) (ToolResponse[Output], error) {
      _ = ctx
      output := Output(input.Summary)
      if input.ArtifactID != nil {
        output = Output(*input.ArtifactID)
      }
      return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }
  GO
}

research "static" "seed" {
  model             = "test-model"
  system_prompt     = "Produce an upstream result."
  terminate_tool_id = go_tool.finish.id
}

research "static" "brainstorm" {
  model         = "test-model"
  system_prompt = "Use the upstream result."
  prompt        = research.static.seed.result

  artifact "scope" {
    type        = "file"
    path        = "scope.json"
    description = "Structured scope"
  }

  tool_use "finish" {
    tool_id   = go_tool.finish.id
    terminate = true
    input = {
      ArtifactID = artifact("scope").id
    }
  }
}

output "scope_id" { value = research.static.brainstorm.result }
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
	assert.Regexp(t, `^artifact-[0-9a-f-]{36}$`, result.Outputs["scope_id"].AsString())
}

func TestProductionRuntimeAcceptsEmptyDynamicTasksWithoutOpeningSession(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "dynamic" "empty" {
  tasks = []
}

output "count" {
  value = length(research.dynamic.empty.tasks)
}
`), 0o600))

	opener := &dynamicTestOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)
	result, err := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	count, _ := result.Outputs["count"].AsBigFloat().Int64()
	assert.Equal(t, int64(0), count)
	assert.Empty(t, opener.Prompts())
}

func TestProductionRuntimeFailsDynamicBlockWhenOneTaskFails(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
go_tool "finish" {
  description = "Return the submitted summary."
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

research "dynamic" "followups" {
  tasks = [for topic in ["alpha", "beta"] : {
    model             = "test-model"
    system_prompt     = "Research the assigned topic."
    prompt            = topic
    terminate_tool_id = go_tool.finish.id
    artifact          = {}
    retry             = null
    qc                = null
  }]
}
`), 0o600))

	opener := &dynamicTestOpener{failPrompt: "beta"}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2})

	require.ErrorContains(t, err, "forced task failure")
}

func TestProductionRuntimeRunsDynamicTasksSeriallyWhenConfigured(t *testing.T) {
	t.Parallel()

	directory := writeDynamicConcurrencyFixture(t, true)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	opener := &dynamicTestOpener{
		started: make(chan string, 2), blockPrompt: "alpha", release: release,
	}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, applyErr := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2})
		done <- applyErr
	}()

	assert.Equal(t, "alpha", receiveDynamicTaskStart(t, opener.started))
	select {
	case prompt := <-opener.started:
		require.Failf(t, "second task started early", "started %q before alpha completed", prompt)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	assert.Equal(t, "beta", receiveDynamicTaskStart(t, opener.started))
	require.NoError(t, <-done)
}

func TestProductionRuntimeRunsDynamicTasksConcurrentlyByDefault(t *testing.T) {
	t.Parallel()

	directory := writeDynamicConcurrencyFixture(t, false)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	opener := &dynamicTestOpener{
		started: make(chan string, 2), blockPrompt: "alpha", release: release,
	}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, applyErr := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2})
		done <- applyErr
	}()

	started := []string{
		receiveDynamicTaskStart(t, opener.started),
		receiveDynamicTaskStart(t, opener.started),
	}
	assert.ElementsMatch(t, []string{"alpha", "beta"}, started)
	close(release)
	require.NoError(t, <-done)
}

func TestProductionRuntimeDynamicTasksUseIndexedWorkspaces(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "dynamic" "followups" {
  tasks = [for index, topic in ["alpha", "beta"] : {
    model         = "test-model"
    system_prompt = "Research the assigned topic."
    prompt        = "${block_wd()}/${index}/${topic}"
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

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2})

	require.NoError(t, err)
	workingDirectories := make(map[string]struct{})
	for _, config := range opener.Configs() {
		workingDirectories[filepath.Clean(config.WorkingDirectory)] = struct{}{}
	}
	assert.Len(t, workingDirectories, 2)
	require.Len(t, opener.Prompts(), 2)
	for _, config := range opener.Configs() {
		workspace := filepath.ToSlash(config.WorkingDirectory)
		topic := map[string]string{"0": "alpha", "1": "beta"}[filepath.Base(workspace)]
		assert.Contains(t, opener.Prompts(), workspace+"/"+topic)
	}
}

func TestProductionRuntimeDynamicTasksKeepFinalQCIssuesIsolated(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "dynamic" "reviewed" {
  serial = true
  tasks = [for topic in ["alpha", "beta"] : {
    model         = "test-model"
    system_prompt = "Research the assigned topic."
    prompt        = topic
    artifact      = {}
    retry         = null
    qc = {
      criteria = { accuracy = "accurate" }
    }
  }]
}
`), 0o600))

	opener := &dynamicQCIssueOpener{finalRounds: map[string]int{}, handoffPrompts: map[string]string{}}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	require.Len(t, opener.finalRounds, 2)
	for workspace, rounds := range opener.finalRounds {
		assert.Equal(t, 2, rounds)
		assert.Contains(t, opener.handoffPrompts[workspace], "correct the task result")
	}
}

func TestProductionRuntimeDynamicTaskArtifactPathResolves(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "dynamic" "followups" {
  tasks = [for index, topic in ["alpha", "beta"] : {
    model         = "test-model"
    system_prompt = "Save source material under ${artifact("sources").path}."
    prompt        = artifact("knowledge").path
    artifact = {
      sources = {
        type        = "directory"
        path        = "${block_wd()}/${index}/collected"
        description = "Collected source material"
      }
      knowledge = {
        type        = "file"
        path        = "${block_wd()}/${index}/knowledge.json"
        description = "Collected knowledge"
      }
    }
    retry = null
    qc    = null
  }]
}
`), 0o600))

	opener := &dynamicTestOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2})

	require.NoError(t, err)
	for _, config := range opener.Configs() {
		if strings.Contains(config.SystemPrompt, "Save source material under") {
			assert.NotContains(t, config.SystemPrompt, "__r42_artifact_ref_")
			assert.Contains(t, config.SystemPrompt, filepath.ToSlash(filepath.Join(config.WorkingDirectory, "collected")))
			assert.Contains(t, opener.Prompts(), filepath.ToSlash(filepath.Join(config.WorkingDirectory, "knowledge.json")))
		}
	}
}

func TestProductionRuntimeStopsSerialDynamicTasksAfterFailure(t *testing.T) {
	t.Parallel()

	directory := writeDynamicConcurrencyFixture(t, true)
	opener := &dynamicTestOpener{started: make(chan string, 2), failPrompt: "alpha"}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2})

	require.ErrorContains(t, err, "forced task failure")
	assert.Equal(t, "alpha", receiveDynamicTaskStart(t, opener.started))
	select {
	case prompt := <-opener.started:
		require.Failf(t, "task started after failure", "started %q after alpha failed", prompt)
	default:
	}
}

func writeDynamicConcurrencyFixture(t *testing.T, serial bool) string {
	t.Helper()

	serialAttribute := ""
	if serial {
		serialAttribute = "serial = true"
	}
	directory := t.TempDir()
	source := fmt.Sprintf(`
research "dynamic" "followups" {
  %s
  tasks = [for topic in ["alpha", "beta"] : {
    model         = "test-model"
    system_prompt = "Research the assigned topic."
    prompt        = topic
    artifact      = {}
    retry         = null
    qc            = null
  }]
}
`, serialAttribute)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(source), 0o600))
	return directory
}

func receiveDynamicTaskStart(t *testing.T, started <-chan string) string {
	t.Helper()

	select {
	case prompt := <-started:
		return prompt
	case <-time.After(time.Second):
		require.FailNow(t, "dynamic task did not start")
		return ""
	}
}

type dynamicTestOpener struct {
	mu          sync.Mutex
	topics      []string
	prompts     []string
	configs     []copilot.SessionConfig
	failPrompt  string
	started     chan string
	blockPrompt string
	release     <-chan struct{}
}

func (o *dynamicTestOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	o.mu.Lock()
	o.configs = append(o.configs, config)
	o.mu.Unlock()
	return &dynamicTestSession{config: config, opener: o}, nil
}

func (o *dynamicTestOpener) Configs() []copilot.SessionConfig {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]copilot.SessionConfig(nil), o.configs...)
}

func (o *dynamicTestOpener) Prompts() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.prompts...)
}

type dynamicTestSession struct {
	config copilot.SessionConfig
	opener *dynamicTestOpener
}

func (s *dynamicTestSession) SendAndWait(_ context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	if handled, err := handleDefaultWorkflowProtocol(s.config); handled {
		return &sdk.SessionEvent{}, err
	}
	basePrompt := researchTaskPrompt(options.Prompt)
	if s.opener.started != nil {
		s.opener.started <- basePrompt
	}
	if basePrompt == s.opener.failPrompt {
		return nil, errors.New("forced task failure")
	}
	if s.opener.release != nil && basePrompt == s.opener.blockPrompt {
		<-s.opener.release
	}
	if len(s.config.Tools) > 0 {
		tool := s.config.Tools[0]
		summary := basePrompt
		if basePrompt == "Begin the configured research task." {
			arguments, err := json.Marshal(s.opener.topics)
			if err != nil {
				return nil, err
			}
			summary = string(arguments)
		} else {
			s.opener.mu.Lock()
			s.opener.prompts = append(s.opener.prompts, basePrompt)
			s.opener.mu.Unlock()
		}
		_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"Summary": summary}})
		return &sdk.SessionEvent{}, err
	}
	s.opener.mu.Lock()
	s.opener.prompts = append(s.opener.prompts, basePrompt)
	s.opener.mu.Unlock()
	return &sdk.SessionEvent{}, nil
}

func (*dynamicTestSession) Close(context.Context) error { return nil }

func researchTaskPrompt(prompt string) string {
	const marker = "\n\nComplete information_need_outcomes from Collection QC"
	base, _, found := strings.Cut(prompt, marker)
	if found {
		return base
	}
	return prompt
}

type dynamicQCIssueOpener struct {
	mu             sync.Mutex
	finalRounds    map[string]int
	handoffPrompts map[string]string
}

func (o *dynamicQCIssueOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	return &dynamicQCIssueSession{config: config, opener: o}, nil
}

type dynamicQCIssueSession struct {
	config copilot.SessionConfig
	opener *dynamicQCIssueOpener
}

func (s *dynamicQCIssueSession) SendAndWait(_ context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	switch workflowSessionKind(s.config) {
	case "collection":
		_ = callDynamicQCWorkflowTool(s.config, "r42_set_information_needs", map[string]any{
			"information_needs": []any{map[string]any{
				"question":        "test fixture need",
				"stop_conditions": []any{map[string]any{"condition": "test fixture condition"}},
			}},
		})
		return &sdk.SessionEvent{}, callDynamicQCWorkflowTool(s.config, "r42_collection_checkpoint", map[string]any{
			"empty_reason": "fixture has no acquisition work",
			"need_dispositions": []any{map[string]any{
				"information_need_id": "NEED-001", "search_disposition": "stalled",
			}},
		})
	case "collection_qc":
		return &sdk.SessionEvent{}, callDynamicQCWorkflowTool(s.config, "r42_collection_qc_verdict", map[string]any{
			"assessments": []any{map[string]any{
				"information_need_id": "NEED-001", "status": "sufficient",
				"unsatisfied_condition_ids": []any{}, "evidence_progress": "none",
			}},
		})
	case "final_qc":
		workspace := s.config.WorkingDirectory
		s.opener.mu.Lock()
		s.opener.finalRounds[workspace]++
		round := s.opener.finalRounds[workspace]
		s.opener.mu.Unlock()
		arguments := map[string]any{"decision": "pass"}
		if round == 1 {
			arguments = map[string]any{
				"decision": "revise_research",
				"issues":   []any{map[string]any{"id": "issue-accuracy", "code": "accuracy", "message": "correct the task result"}},
			}
		}
		return &sdk.SessionEvent{}, callDynamicQCWorkflowTool(s.config, "r42_qc_verdict", arguments)
	default:
		if !strings.Contains(options.Prompt, "correct the task result") {
			return &sdk.SessionEvent{}, nil
		}
		s.opener.mu.Lock()
		s.opener.handoffPrompts[s.config.WorkingDirectory] = options.Prompt
		s.opener.mu.Unlock()
		return &sdk.SessionEvent{}, nil
	}
}

func (*dynamicQCIssueSession) Close(context.Context) error { return nil }

func callDynamicQCWorkflowTool(config copilot.SessionConfig, name string, arguments map[string]any) error {
	for _, tool := range config.Tools {
		if tool.Name == name {
			_, err := tool.Handler(sdk.ToolInvocation{Arguments: arguments})
			return err
		}
	}
	return fmt.Errorf("tool %q not found", name)
}
