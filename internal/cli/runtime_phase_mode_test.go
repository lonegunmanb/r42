package cli_test

import (
	"context"
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

func TestProductionRuntimeCollectionOnlyUsesOneCollectionSession(t *testing.T) {
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

research "static" "builder" {
  phase_mode    = "collection_only"
  model         = "test-model"
  system_prompt = "Build a DCF."

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

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	require.Len(t, opener.configs, 1)
	assert.Contains(t, opener.configs[0].SystemPrompt, "sole Collection session")
	assert.NotContains(t, toolNamesFromConfig(opener.configs[0]), "r42_set_information_needs")
	assert.NotContains(t, toolNamesFromConfig(opener.configs[0]), "r42_collection_checkpoint")
	assert.NotContains(t, toolNamesFromConfig(opener.configs[0]), "r42_collection_qc_verdict")
	assert.Equal(t, 1, opener.session.closeCalls)
	assert.True(t, opener.session.savedArtifact)
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

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

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

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, 2, opener.research.sendCalls)
	assert.Equal(t, 2, opener.qc.sendCalls)
	assert.Len(t, opener.research.prompts, 2)
	assert.Contains(t, opener.research.prompts[1], "add a citation")
	assert.Equal(t, 1, opener.research.closeCalls)
	assert.Equal(t, 1, opener.qc.closeCalls)
	assert.Zero(t, opener.collection.sendCalls)
	assert.Zero(t, opener.collectionQC.sendCalls)
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

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2})

	require.NoError(t, err)
	configs := opener.Configs()
	require.Len(t, configs, 2)
	for _, config := range configs {
		assert.NotContains(t, toolNamesFromConfig(config), "r42_set_information_needs")
		assert.NotContains(t, toolNamesFromConfig(config), "r42_collection_checkpoint")
		assert.NotContains(t, toolNamesFromConfig(config), "r42_collection_qc_verdict")
	}
	assert.ElementsMatch(t, []string{"alpha", "beta"}, opener.Prompts())
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

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	require.Len(t, opener.finalRounds, 2)
	for workspace, rounds := range opener.finalRounds {
		assert.Equal(t, 2, rounds)
		assert.Contains(t, opener.handoffPrompts[workspace], "correct the task result")
	}
}
