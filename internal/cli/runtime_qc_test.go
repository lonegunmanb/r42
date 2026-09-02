package cli_test

import (
	"context"
	"errors"
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
)

func TestProductionRuntimeRunsPersistentQCSessionWithVerdictTool(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "source" {
  model                              = "test-model"
  profile                            = "gpt-5.4"
  system_prompt                      = "Collect evidence."
  collection_allowed_builtin_tools   = ["powershell"]
  collection_qc_allowed_builtin_tools = ["web_fetch"]
  research_allowed_builtin_tools     = ["shell"]
  final_qc_allowed_builtin_tools     = ["edit"]
  qc { criteria = { accuracy = "Must be accurate" } }
}
`), 0o600))
	opener := &qcOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	require.Len(t, opener.configs, 4)
	assert.Equal(t, "gpt-5.4", opener.configs[0].Profile)
	assert.Equal(t, "gpt-5.4", opener.configs[3].Profile)
	assert.Contains(t, toolNamesFromConfig(opener.configs[0]), "r42_collection_checkpoint")
	assert.Contains(t, toolNamesFromConfig(opener.configs[0]), "r42_read_information_needs")
	assert.Contains(t, toolNamesFromConfig(opener.configs[1]), "r42_collection_qc_verdict")
	assert.Contains(t, toolNamesFromConfig(opener.configs[1]), "r42_read_information_needs")
	assert.Contains(t, toolNamesFromConfig(opener.configs[3]), "r42_qc_verdict")
	assert.Contains(t, toolNamesFromConfig(opener.configs[3]), "r42_qc_expand_quote")
	assert.Contains(t, toolNamesFromConfig(opener.configs[3]), "r42_qc_open_issues")
	assert.Contains(t, opener.configs[3].SystemPrompt, "r42_qc_expand_quote")
	assert.NotContains(t, opener.configs[0].ExcludedTools, "powershell")
	assert.Contains(t, opener.configs[0].ExcludedTools, "shell")
	assert.NotContains(t, opener.configs[1].ExcludedTools, "web_fetch")
	assert.Contains(t, opener.configs[1].ExcludedTools, "shell")
	assert.NotContains(t, opener.configs[2].ExcludedTools, "shell")
	assert.Contains(t, opener.configs[2].ExcludedTools, "edit")
	assert.NotContains(t, opener.configs[3].ExcludedTools, "edit")
	assert.Contains(t, opener.configs[3].ExcludedTools, "shell")
	assert.Contains(t, opener.configs[3].SystemPrompt, `Strictness="balanced"`)
	assert.Contains(t, opener.configs[3].SystemPrompt, "Balanced provenance")
	assert.Equal(t, 1, opener.research.sendCalls)
	assert.Equal(t, 1, opener.qc.sendCalls)
	assert.Equal(t, 1, opener.collection.closeCalls)
	assert.Equal(t, 1, opener.collectionQC.closeCalls)
	assert.Equal(t, 1, opener.research.closeCalls)
	assert.Equal(t, 1, opener.qc.closeCalls)
}

func TestProductionRuntimeExposesCollectionQCCriteriaBeforePlan(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  collection_qc {
    criteria = { primary_sources = "Use primary sources for every stop condition." }
  }
}
`), 0o600))
	opener := &qcOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	require.NotEmpty(t, opener.configs)
	assert.Contains(t, opener.configs[0].SystemPrompt, "Collection QC evidence-quality criteria")
	assert.Contains(t, opener.configs[0].SystemPrompt, "primary_sources")
	assert.Contains(t, opener.configs[0].SystemPrompt, "Use primary sources for every stop condition.")
	assert.Contains(t, opener.configs[0].SystemPrompt, "After every MCP query or information read, save the result as evidence or a snapshot")
}

func TestProductionRuntimeKeepsFinalQCIssueTrackingOutOfResearchTools(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  final_qc_strictness = "brief"
  qc { criteria = { accuracy = "Must be accurate" } }
}
`), 0o600))
	opener := &qcOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	require.Len(t, opener.configs, 4)
	assert.NotContains(t, toolNamesFromConfig(opener.configs[2]), "r42_report_qc_issue_resolutions")
	assert.Contains(t, toolNamesFromConfig(opener.configs[3]), "r42_qc_patch_artifact")
	assert.Contains(t, toolNamesFromConfig(opener.configs[3]), "r42_qc_patch_knowledge")
	assert.Contains(t, opener.configs[3].SystemPrompt, "focused audit of material semantic issues")
	assert.Contains(t, opener.configs[3].SystemPrompt, "analysis and mixed claims")
	assert.Contains(t, opener.configs[3].SystemPrompt, "Do not demand a formal reasoning chain")
	assert.Contains(t, opener.configs[3].SystemPrompt, "final_qc_strictness is authoritative")
	assert.Contains(t, opener.configs[3].SystemPrompt, `Strictness="brief"`)
	assert.Contains(t, opener.configs[3].SystemPrompt, "Brief provenance")
	assert.Contains(t, opener.configs[3].SystemPrompt, "Final QC is a convergent, narrow audit")
	assert.Contains(t, opener.configs[3].SystemPrompt, "must not reject a plausible analysis")
	assert.Contains(t, opener.configs[2].SystemPrompt, "Final QC reviews and repairs the candidate directly")
	assert.NotContains(t, opener.configs[2].SystemPrompt, "return this block to Research")
}

func TestProductionRuntimeUsesStrictProvenanceGuidance(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  final_qc_strictness = "strict"
  qc { criteria = { accuracy = "Must be accurate" } }
}
`), 0o600))
	opener := &qcOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})
	require.NoError(t, err)
	require.Len(t, opener.configs, 4)
	assert.Contains(t, opener.configs[3].SystemPrompt, `Strictness="strict"`)
	assert.Contains(t, opener.configs[3].SystemPrompt, "Strict provenance")
}

func TestProductionRuntimeReusesResearchProviderAcrossWorkflowSessions(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
model_provider "primary" {
  type     = "openai"
  endpoint = "https://models.example.test"
  retry { lifecycle_retries = 17 }
}

research "static" "source" {
  model_provider = model_provider.primary
  model          = "test-model"
  system_prompt  = "Collect evidence."
  qc { criteria = { accuracy = "Must be accurate" } }
}
`), 0o600))
	opener := &qcOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	require.Len(t, opener.configs, 4)
	providerConfig := opener.configs[0].Provider
	require.NotNil(t, providerConfig)
	for _, sessionConfig := range opener.configs[1:] {
		assert.Same(t, providerConfig, sessionConfig.Provider)
	}
	for _, sessionConfig := range opener.configs {
		assert.Equal(t, 17, sessionConfig.Retry.LifecycleRetries)
	}
}

func TestProductionRuntimeUsesExplicitWorkflowPhaseProviders(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
model_provider "primary" {
  type     = "openai"
  endpoint = "https://primary.example.test"
  retry { lifecycle_retries = 11 }
}
model_provider "collection" {
  type     = "openai"
  endpoint = "https://collection.example.test"
  retry { lifecycle_retries = 12 }
}
model_provider "collection_qc" {
  type     = "openai"
  endpoint = "https://collection-qc.example.test"
  retry { lifecycle_retries = 13 }
}
model_provider "final_qc" {
  type     = "openai"
  endpoint = "https://final-qc.example.test"
  retry { lifecycle_retries = 14 }
}

research "static" "source" {
  model_provider            = model_provider.primary
  collection_model_provider = model_provider.collection
  model                     = "test-model"
  system_prompt             = "Collect evidence."

  collection_qc {
    model_provider = model_provider.collection_qc
  }
  qc {
    criteria       = { accuracy = "Must be accurate" }
    model_provider = model_provider.final_qc
  }
}
`), 0o600))
	opener := &qcOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	require.Len(t, opener.configs, 4)
	assert.Equal(t, "https://collection.example.test", opener.configs[0].Provider.Endpoint)
	assert.Equal(t, "https://collection-qc.example.test", opener.configs[1].Provider.Endpoint)
	assert.Equal(t, "https://primary.example.test", opener.configs[2].Provider.Endpoint)
	assert.Equal(t, "https://final-qc.example.test", opener.configs[3].Provider.Endpoint)
	assert.Equal(t, 12, opener.configs[0].Retry.LifecycleRetries)
	assert.Equal(t, 13, opener.configs[1].Retry.LifecycleRetries)
	assert.Equal(t, 11, opener.configs[2].Retry.LifecycleRetries)
	assert.Equal(t, 14, opener.configs[3].Retry.LifecycleRetries)
	assert.Contains(t, opener.configs[3].SystemPrompt, "focused audit of material semantic issues")
	assert.Contains(t, opener.configs[3].SystemPrompt, "do not manufacture issues about optional detail")
	assert.Contains(t, opener.configs[3].SystemPrompt, "every configured criterion")
	assert.Contains(t, opener.configs[3].SystemPrompt, "all independent issues")
	assert.Contains(t, opener.configs[3].SystemPrompt, "do not stop after the first issue")
	assert.Contains(t, opener.configs[3].SystemPrompt, "Repeat the full audit after every repair")
	assert.Contains(t, opener.configs[3].SystemPrompt, "use r42_qc_patch_knowledge")
	assert.Contains(t, opener.configs[3].SystemPrompt, "provide exactly one patch per call")
	assert.Contains(t, opener.configs[3].SystemPrompt, "Never batch multiple changes")
	assert.Contains(t, opener.configs[3].SystemPrompt, "must not judge whether evidence coverage is sufficient")
	assert.Contains(t, opener.configs[3].SystemPrompt, "claims actually present in the candidate")
	assert.NotContains(t, opener.configs[3].SystemPrompt, "if evidence is insufficient")
}

func TestProductionRuntimeAppliesResearchTimeoutAcrossQC(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  timeout = "50ms"
  qc { criteria = { accuracy = "Must be accurate" } }
}
`), 0o600))
	opener := &blockingQCOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 1, opener.research.closeCalls)
	assert.Equal(t, 1, opener.qc.closeCalls)
}

func TestProductionRuntimeAppliesResearchTimeoutWhileOpeningQCSession(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  timeout = "50ms"
  qc { criteria = { accuracy = "Must be accurate" } }
}
`), 0o600))
	opener := &blockingQCOpenOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 1, opener.research.closeCalls)
}

func TestProductionRuntimeReportsResearchCloseWarningWhenQCOpenFails(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  qc { criteria = { accuracy = "Must be accurate" } }
}
`), 0o600))
	openErr := errors.New("open QC failed")
	closeErr := errors.New("close research failed")
	opener := &failingQCOpener{openErr: openErr, research: countingSession{closeErr: closeErr}}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	result, err := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.ErrorIs(t, err, openErr)
	require.Len(t, result.Warnings, 1)
	assert.ErrorIs(t, result.Warnings[0], closeErr)
}

func TestProductionRuntimeReusesSessionsForQCIssuesRevisionAndPass(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  qc { criteria = { accuracy = "Must be accurate" } }
}
`), 0o600))
	opener := &revisionQCOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, 1, opener.research.sendCalls)
	require.Len(t, opener.research.prompts, 1)
	assert.Equal(t, 2, opener.qc.sendCalls)
	require.Len(t, opener.qc.prompts, 2)
	assert.Contains(t, opener.qc.prompts[1], `"open_issues"`)
	assert.Contains(t, opener.qc.prompts[1], `"message":"add a citation"`)
	var openIssuesTool sdk.Tool
	for _, tool := range opener.qc.config.Tools {
		if tool.Name == "r42_qc_open_issues" {
			openIssuesTool = tool
		}
	}
	require.NotNil(t, openIssuesTool.Handler)
	query, queryErr := openIssuesTool.Handler(sdk.ToolInvocation{Arguments: map[string]any{}})
	require.NoError(t, queryErr)
	assert.Contains(t, query.TextResultForLLM, "issue-source")
	assert.Equal(t, 1, opener.research.closeCalls)
	assert.Equal(t, 1, opener.qc.closeCalls)
}

func TestProductionRuntimeRejectsNewFinalQCIssueAfterRevision(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  qc { criteria = { accuracy = "Must be accurate" } }
}
`), 0o600))
	opener := &changingIssueQCOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, 1, opener.research.sendCalls)
	assert.Equal(t, 3, opener.qc.sendCalls)
}

type qcOpener struct {
	mu           sync.Mutex
	configs      []copilot.SessionConfig
	collection   countingSession
	collectionQC countingSession
	research     countingSession
	qc           qcSession
}

func (o *qcOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	o.mu.Lock()
	o.configs = append(o.configs, config)
	o.mu.Unlock()
	switch workflowSessionKind(config) {
	case "collection":
		return &protocolFixtureSession{config: config, session: &o.collection}, nil
	case "collection_qc":
		return &protocolFixtureSession{config: config, session: &o.collectionQC}, nil
	case "research":
		return &o.research, nil
	}
	o.qc.config = config
	return &o.qc, nil
}

type countingSession struct {
	mu                    sync.Mutex
	sendCalls, closeCalls int
	closeErr              error
}

func (s *countingSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	s.mu.Unlock()
	return &sdk.SessionEvent{}, nil
}

func (s *countingSession) Close(context.Context) error {
	s.mu.Lock()
	s.closeCalls++
	s.mu.Unlock()
	return s.closeErr
}

type qcSession struct {
	countingSession
	config copilot.SessionConfig
}

type blockingQCOpener struct {
	collection   countingSession
	collectionQC countingSession
	research     countingSession
	qc           blockingSession
}

type blockingQCOpenOpener struct {
	research countingSession
	opened   bool
}

func (o *blockingQCOpenOpener) Open(ctx context.Context, _ copilot.SessionConfig) (cli.Session, error) {
	if !o.opened {
		o.opened = true
		return &o.research, nil
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("QC open context has no deadline")
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (o *blockingQCOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	switch workflowSessionKind(config) {
	case "collection":
		return &protocolFixtureSession{config: config, session: &o.collection}, nil
	case "collection_qc":
		return &protocolFixtureSession{config: config, session: &o.collectionQC}, nil
	case "research":
		return &o.research, nil
	}
	return &o.qc, nil
}

type blockingSession struct{ countingSession }

func (s *blockingSession) SendAndWait(ctx context.Context, _ sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Second):
		return nil, errors.New("QC timeout was not propagated")
	}
}

type failingQCOpener struct {
	openErr  error
	research countingSession
	opened   bool
}

type revisionQCOpener struct {
	collection   countingSession
	collectionQC countingSession
	research     promptSession
	qc           revisionQCSession
}

type changingIssueQCOpener struct {
	collection   countingSession
	collectionQC countingSession
	research     promptSession
	qc           changingIssueQCSession
}

func (o *revisionQCOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	switch workflowSessionKind(config) {
	case "collection":
		return &protocolFixtureSession{config: config, session: &o.collection}, nil
	case "collection_qc":
		return &protocolFixtureSession{config: config, session: &o.collectionQC}, nil
	case "research":
		o.research.config = config
		return &o.research, nil
	}
	o.qc.config = config
	return &o.qc, nil
}

func (o *changingIssueQCOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	switch workflowSessionKind(config) {
	case "collection":
		return &protocolFixtureSession{config: config, session: &o.collection}, nil
	case "collection_qc":
		return &protocolFixtureSession{config: config, session: &o.collectionQC}, nil
	case "research":
		o.research.config = config
		return &o.research, nil
	}
	o.qc.config = config
	return &o.qc, nil
}

func toolNamesFromConfig(config copilot.SessionConfig) []string {
	names := make([]string, len(config.Tools))
	for index := range config.Tools {
		names[index] = config.Tools[index].Name
	}
	return names
}

type promptSession struct {
	countingSession
	prompts []string
	config  copilot.SessionConfig
}

func (s *promptSession) SendAndWait(_ context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	call := s.sendCalls
	s.prompts = append(s.prompts, options.Prompt)
	s.mu.Unlock()
	_ = call
	return &sdk.SessionEvent{}, nil
}

type revisionQCSession struct {
	countingSession
	config  copilot.SessionConfig
	prompts []string
}

type changingIssueQCSession struct {
	countingSession
	config copilot.SessionConfig
}

func (s *revisionQCSession) SendAndWait(_ context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	call := s.sendCalls
	s.prompts = append(s.prompts, options.Prompt)
	s.mu.Unlock()
	arguments := map[string]any{"decision": "pass"}
	if call == 1 {
		arguments = map[string]any{
			"decision": "revise_research",
			"issues":   []any{map[string]any{"id": "issue-source", "code": "missing_source", "message": "add a citation"}},
		}
	}
	for _, tool := range s.config.Tools {
		if tool.Name == "r42_qc_verdict" {
			_, err := tool.Handler(sdk.ToolInvocation{Arguments: arguments})
			return &sdk.SessionEvent{}, err
		}
	}
	return nil, assert.AnError
}

func (s *changingIssueQCSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	call := s.sendCalls
	s.mu.Unlock()
	arguments := map[string]any{"decision": "pass"}
	switch call {
	case 1:
		arguments = map[string]any{
			"decision": "revise_research",
			"issues":   []any{map[string]any{"id": "issue-citation", "code": "citation", "message": "add a citation"}},
		}
	case 2:
		arguments = map[string]any{
			"decision": "revise_research",
			"issues":   []any{map[string]any{"id": "issue-new", "code": "accuracy", "message": "correct the changed total"}},
		}
	}
	for _, tool := range s.config.Tools {
		if tool.Name == "r42_qc_verdict" {
			_, err := tool.Handler(sdk.ToolInvocation{Arguments: arguments})
			return &sdk.SessionEvent{}, err
		}
	}
	return nil, assert.AnError
}

func (o *failingQCOpener) Open(context.Context, copilot.SessionConfig) (cli.Session, error) {
	if !o.opened {
		o.opened = true
		return &o.research, nil
	}
	return nil, o.openErr
}

func (s *qcSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	s.mu.Unlock()
	for _, tool := range s.config.Tools {
		if tool.Name == "r42_qc_verdict" {
			_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"decision": "pass"}})
			return &sdk.SessionEvent{}, err
		}
	}
	return nil, assert.AnError
}
