package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticResearchWorkflowCoversBatchNeedsMoreAndRevision(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "source" {
  model = "test-model"
  system_prompt = "Investigate the topic."
  collection_batch_size = 10
  max_collection_rounds = 3
  qc { criteria = { accuracy = "accurate" } }
}
`), 0o600))
	opener := &workflowScenarioOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, 2, opener.collectionRounds)
	assert.Equal(t, 2, opener.collectionQCRounds)
	assert.Equal(t, 1, opener.researchRounds)
	assert.Equal(t, 2, opener.finalQCRounds)
}

func TestFinalQCRejectsReopenCollection(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "source" {
  model = "test-model"
  system_prompt = "Investigate the topic."
  max_collection_rounds = 1
  qc { criteria = { accuracy = "accurate" } }
}
`), 0o600))
	opener := &reopenRejectionOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Contains(t, opener.rejection, "unsupported final qc decision")
	assert.Equal(t, 2, opener.finalSends)
}

func TestDynamicResearchMembersHaveIsolatedArtifactRegistries(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "dynamic" "members" {
  serial = true
  tasks = [for topic in ["alpha", "beta"] : {
    model = "test-model"
    system_prompt = "Investigate ${topic}."
    prompt = topic
  }]
}
`), 0o600))
	opener := &isolatedRegistryOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, 2, opener.reviewed)
}

type workflowScenarioOpener struct {
	mu                                                                  sync.Mutex
	collectionRounds, collectionQCRounds, researchRounds, finalQCRounds int
}

func (o *workflowScenarioOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	return &workflowScenarioSession{opener: o, config: config}, nil
}

type workflowScenarioSession struct {
	opener *workflowScenarioOpener
	config copilot.SessionConfig
}

func (s *workflowScenarioSession) SendAndWait(_ context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.opener.mu.Lock()
	defer s.opener.mu.Unlock()
	switch workflowSessionKind(s.config) {
	case "collection":
		s.opener.collectionRounds++
		if s.opener.collectionRounds == 1 {
			_, err := findTool(s.config.Tools, "r42_set_information_needs").Handler(sdk.ToolInvocation{Arguments: map[string]any{
				"information_needs": []any{map[string]any{
					"question":        "Investigate the topic",
					"stop_conditions": []any{map[string]any{"condition": "evidence is sufficient"}},
				}},
			}})
			if err != nil {
				return &sdk.SessionEvent{}, err
			}
			for index := range 10 {
				path := filepath.Join(s.config.WorkingDirectory, fmt.Sprintf("source-%d.txt", index))
				if err := os.WriteFile(path, fmt.Appendf(nil, "evidence-%d", index), 0o600); err != nil {
					return nil, err
				}
				if _, err := findTool(s.config.Tools, "r42_register_artifact").Handler(sdk.ToolInvocation{Arguments: map[string]any{"path": path}}); err != nil {
					return nil, err
				}
			}
			_, err = findTool(s.config.Tools, "r42_collection_checkpoint").Handler(sdk.ToolInvocation{Arguments: map[string]any{
				"need_dispositions": []any{map[string]any{
					"information_need_id": "NEED-001", "search_disposition": "continue",
				}},
			}})
			return &sdk.SessionEvent{}, err
		}
		_, err := findTool(s.config.Tools, "r42_collection_checkpoint").Handler(sdk.ToolInvocation{Arguments: map[string]any{
			"empty_reason": "no additional source required by fixture",
			"need_dispositions": []any{map[string]any{
				"information_need_id": "NEED-001", "search_disposition": "continue",
			}},
		}})
		return &sdk.SessionEvent{}, err
	case "collection_qc":
		s.opener.collectionQCRounds++
		arguments := map[string]any{
			"assessments": []any{map[string]any{
				"information_need_id": "NEED-001", "status": "sufficient",
				"unsatisfied_condition_ids": []any{}, "evidence_progress": "none",
			}},
		}
		if s.opener.collectionQCRounds == 1 {
			arguments = map[string]any{
				"assessments": []any{map[string]any{
					"information_need_id": "NEED-001", "status": "needs_more",
					"unsatisfied_condition_ids": []any{"NEED-001-SC-001"}, "evidence_progress": "none",
				}},
			}
		}
		_, err := findTool(s.config.Tools, "r42_collection_qc_verdict").Handler(sdk.ToolInvocation{Arguments: arguments})
		return &sdk.SessionEvent{}, err
	case "final_qc":
		s.opener.finalQCRounds++
		arguments := map[string]any{"decision": "pass"}
		if s.opener.finalQCRounds == 1 {
			arguments = map[string]any{
				"decision": "revise_research",
				"issues": []any{
					map[string]any{"id": "issue-coverage", "code": "coverage", "message": "coverage"},
					map[string]any{"id": "issue-accuracy", "code": "accuracy", "message": "accuracy"},
				},
			}
		}
		if s.opener.finalQCRounds == 2 {
			arguments = map[string]any{"decision": "pass"}
		}
		_, err := findTool(s.config.Tools, "r42_qc_verdict").Handler(sdk.ToolInvocation{Arguments: arguments})
		return &sdk.SessionEvent{}, err
	default:
		s.opener.researchRounds++
		return &sdk.SessionEvent{}, nil
	}
}

func (*workflowScenarioSession) Close(context.Context) error { return nil }

func decisionArguments(decision, code string) map[string]any {
	return map[string]any{"decision": decision, "issues": []any{map[string]any{"id": "issue-" + code, "code": code, "message": code}}}
}

type reopenRejectionOpener struct {
	mu         sync.Mutex
	finalSends int
	rejection  string
}

func (o *reopenRejectionOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	return &reopenRejectionSession{opener: o, config: config}, nil
}

type reopenRejectionSession struct {
	opener *reopenRejectionOpener
	config copilot.SessionConfig
}

func (s *reopenRejectionSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	if workflowSessionKind(s.config) != "final_qc" {
		if handled, err := handleWorkflowProtocol(s.config); handled {
			return &sdk.SessionEvent{}, err
		}
		return &sdk.SessionEvent{}, nil
	}
	s.opener.mu.Lock()
	defer s.opener.mu.Unlock()
	s.opener.finalSends++
	arguments := map[string]any{"decision": "pass"}
	if s.opener.finalSends == 1 {
		arguments = decisionArguments("reopen_collection", "coverage")
	}
	result, err := findTool(s.config.Tools, "r42_qc_verdict").Handler(sdk.ToolInvocation{Arguments: arguments})
	if s.opener.finalSends == 1 {
		s.opener.rejection = result.TextResultForLLM
	}
	return &sdk.SessionEvent{}, err
}

func (*reopenRejectionSession) Close(context.Context) error { return nil }

type isolatedRegistryOpener struct {
	mu       sync.Mutex
	reviewed int
}

func (o *isolatedRegistryOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	return &isolatedRegistrySession{opener: o, config: config}, nil
}

type isolatedRegistrySession struct {
	opener *isolatedRegistryOpener
	config copilot.SessionConfig
}

func (s *isolatedRegistrySession) SendAndWait(_ context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	switch workflowSessionKind(s.config) {
	case "collection":
		name := strings.TrimSpace(options.Prompt)
		path := filepath.Join(s.config.WorkingDirectory, name+".txt")
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			return nil, err
		}
		_, err := findTool(s.config.Tools, "r42_set_information_needs").Handler(sdk.ToolInvocation{Arguments: map[string]any{
			"information_needs": []any{map[string]any{
				"question":        "fixture need",
				"stop_conditions": []any{map[string]any{"condition": "fixture condition"}},
			}},
		}})
		if err != nil {
			return nil, err
		}
		if _, err := findTool(s.config.Tools, "r42_register_artifact").Handler(sdk.ToolInvocation{Arguments: map[string]any{"path": path}}); err != nil {
			return nil, err
		}
		_, err = findTool(s.config.Tools, "r42_collection_checkpoint").Handler(sdk.ToolInvocation{Arguments: map[string]any{
			"need_dispositions": []any{map[string]any{
				"information_need_id": "NEED-001", "search_disposition": "continue",
			}},
		}})
		return &sdk.SessionEvent{}, err
	case "collection_qc":
		result, err := findTool(s.config.Tools, "r42_list_artifacts").Handler(sdk.ToolInvocation{Arguments: map[string]any{}})
		if err != nil {
			return nil, err
		}
		var response struct {
			Output []any `json:"output"`
		}
		if err = json.Unmarshal([]byte(result.TextResultForLLM), &response); err != nil {
			return nil, err
		}
		if len(response.Output) != 1 {
			return nil, fmt.Errorf("artifact registry contains %d entries, want 1", len(response.Output))
		}
		s.opener.mu.Lock()
		s.opener.reviewed++
		s.opener.mu.Unlock()
		_, err = findTool(s.config.Tools, "r42_collection_qc_verdict").Handler(sdk.ToolInvocation{Arguments: map[string]any{
			"assessments": []any{map[string]any{
				"information_need_id": "NEED-001", "status": "sufficient",
				"unsatisfied_condition_ids": []any{}, "evidence_progress": "material",
			}},
		}})
		return &sdk.SessionEvent{}, err
	default:
		return &sdk.SessionEvent{}, nil
	}
}

func (*isolatedRegistrySession) Close(context.Context) error { return nil }
