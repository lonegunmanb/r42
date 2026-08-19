package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/lonegunmanb/r42/internal/executor"
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
      artifacts     = []
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
      artifacts         = []
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
    artifacts         = []
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
    artifacts     = []
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
	failPrompt  string
	started     chan string
	blockPrompt string
	release     <-chan struct{}
}

func (o *dynamicTestOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	return &dynamicTestSession{config: config, opener: o}, nil
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
	if s.opener.started != nil {
		s.opener.started <- options.Prompt
	}
	if options.Prompt == s.opener.failPrompt {
		return nil, errors.New("forced task failure")
	}
	if s.opener.release != nil && options.Prompt == s.opener.blockPrompt {
		<-s.opener.release
	}
	if len(s.config.Tools) > 0 {
		tool := s.config.Tools[0]
		summary := options.Prompt
		if options.Prompt == "Begin the configured research task." {
			arguments, err := json.Marshal(s.opener.topics)
			if err != nil {
				return nil, err
			}
			summary = string(arguments)
		} else {
			s.opener.mu.Lock()
			s.opener.prompts = append(s.opener.prompts, options.Prompt)
			s.opener.mu.Unlock()
		}
		_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"Summary": summary}})
		return &sdk.SessionEvent{}, err
	}
	s.opener.mu.Lock()
	s.opener.prompts = append(s.opener.prompts, options.Prompt)
	s.opener.mu.Unlock()
	return &sdk.SessionEvent{}, nil
}

func (*dynamicTestSession) Close(context.Context) error { return nil }
