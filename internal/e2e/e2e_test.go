package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestResearchWithoutTerminalCompletesHermetically(t *testing.T) {
	t.Parallel()

	opener := &scenarioOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := runtime.Plan(t.Context(), fixtureDirectory(t, "no_terminal"), nil)
	require.NoError(t, err)

	result, err := runtime.Apply(t.Context(), planned, cli.ApplyOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Empty(t, result.Outputs)
	assert.Equal(t, 1, opener.opens)
	assert.Equal(t, 1, opener.session.sends)
	assert.Equal(t, 1, opener.session.closes)
}

func TestDocumentedBasicExamplePlans(t *testing.T) {
	t.Parallel()

	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: &scenarioOpener{}})
	planned, err := runtime.Plan(t.Context(), filepath.Join("..", "..", "docs", "examples", "basic"), nil)

	require.NoError(t, err)
	require.Len(t, planned.Nodes(), 1)
	assert.Equal(t, "research.summary", planned.Nodes()[0].Address)
}

func TestTerminalResultAndArtifactsCrossRuntimeBoundaries(t *testing.T) {
	t.Parallel()

	opener := &terminalArtifactOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := runtime.Plan(t.Context(), fixtureDirectory(t, "terminal_artifacts"), nil)
	require.NoError(t, err)

	result, err := runtime.Apply(t.Context(), planned, cli.ApplyOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("research complete"), result.Outputs["summary"])
	report := result.Outputs["report_path"].AsString()
	evidence := result.Outputs["evidence_path"].AsString()
	assert.FileExists(t, report)
	assert.DirExists(t, evidence)
	contents, err := os.ReadFile(filepath.Join(evidence, "source.txt"))
	require.NoError(t, err)
	assert.Equal(t, "source", string(contents))
}

func TestRejectedTerminalArgumentsAreRepairedInSameSession(t *testing.T) {
	t.Parallel()

	opener := &repairOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := runtime.Plan(t.Context(), fixtureDirectory(t, "repair_terminal"), nil)
	require.NoError(t, err)

	result, err := runtime.Apply(t.Context(), planned, cli.ApplyOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("repaired"), result.Outputs["summary"])
	assert.Contains(t, opener.firstResult, `"accepted":false`)
	assert.Equal(t, 2, opener.sends)
	assert.Equal(t, 1, opener.opens)
}

func TestQCIssuesTriggerRevisionAndPassInPersistentSessions(t *testing.T) {
	t.Parallel()

	opener := &qcScenarioOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := runtime.Plan(t.Context(), fixtureDirectory(t, "qc_revision"), nil)
	require.NoError(t, err)

	_, err = runtime.Apply(t.Context(), planned, cli.ApplyOptions{Parallelism: 1})

	require.NoError(t, err)
	assert.Equal(t, 2, opener.opens)
	assert.Equal(t, 2, opener.research.sends)
	assert.Equal(t, 2, opener.qc.sends)
	require.Len(t, opener.research.prompts, 2)
	assert.Contains(t, opener.research.prompts[1], "add a citation")
}

func TestParallelModulesPublishOutputsAfterChildrenComplete(t *testing.T) {
	t.Parallel()

	tracker := &parallelTracker{allStarted: make(chan struct{})}
	opener := &parallelOpener{tracker: tracker}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := runtime.Plan(t.Context(), fixtureDirectory(t, "parallel_modules"), nil)
	require.NoError(t, err)

	result, err := runtime.Apply(t.Context(), planned, cli.ApplyOptions{Parallelism: 2})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("child-done"), result.Outputs["first"])
	assert.Equal(t, cty.StringVal("child-done"), result.Outputs["second"])
	assert.Equal(t, 2, tracker.maximum)
}

func TestPlanAndApplyRunInDifferentProcesses(t *testing.T) {
	t.Parallel()

	planPath := filepath.Join(t.TempDir(), "research.r42plan")
	planOutput := runE2ECLIProcess(
		t,
		"plan",
		"--directory",
		fixtureDirectory(t, "terminal_artifacts"),
		"--out",
		planPath,
	)
	assert.Contains(t, planOutput, "research.source")
	assert.FileExists(t, planPath)

	applyOutput := runE2ECLIProcess(t, "apply", planPath, "--parallelism", "1")
	assert.Contains(t, applyOutput, `"summary":"research complete"`)
}

func fixtureDirectory(t *testing.T, name string) string {
	t.Helper()
	directory := t.TempDir()
	require.NoError(t, os.CopyFS(directory, os.DirFS(filepath.Join("testdata", name))))
	return directory
}

func runE2ECLIProcess(t *testing.T, arguments ...string) string {
	t.Helper()
	commandArguments := append([]string{"-test.run=^TestE2ECLIProcess$", "--"}, arguments...)
	command := exec.CommandContext(t.Context(), os.Args[0], commandArguments...)
	command.Env = append(os.Environ(), "R42_E2E_CLI=1")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return string(output)
}

//nolint:paralleltest // Helper subprocess executes the CLI once and exits.
func TestE2ECLIProcess(t *testing.T) {
	if os.Getenv("R42_E2E_CLI") != "1" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	require.NotEqual(t, -1, separator)
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: &terminalArtifactOpener{}})
	command := cli.NewCommand(runtime)
	command.SetArgs(os.Args[separator+1:])
	command.SetOut(os.Stdout)
	command.SetErr(os.Stderr)
	require.NoError(t, command.ExecuteContext(t.Context()))
}

func TestFailFastCancelsSessionAndExternalChildProcess(t *testing.T) {
	t.Parallel()

	startedFile := filepath.Join(t.TempDir(), "external-started")
	directory := renderFailFastFixture(t, startedFile)
	state := &failFastState{startedFile: startedFile, slowStopped: make(chan struct{})}
	opener := &failFastOpener{state: state}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := runtime.Plan(t.Context(), directory, nil)
	require.NoError(t, err)

	result, err := runtime.Apply(t.Context(), planned, cli.ApplyOptions{Parallelism: 2})

	require.ErrorContains(t, err, "trigger fail-fast")
	assert.Empty(t, result.Outputs)
	select {
	case <-state.slowStopped:
	case <-time.After(2 * time.Second):
		t.Fatal("slow external tool did not stop after fail-fast cancellation")
	}
	state.mu.Lock()
	slowErr := state.slowErr
	state.mu.Unlock()
	require.ErrorIs(t, slowErr, context.Canceled)
	assert.Equal(t, 2, state.closedSessions)
}

func renderFailFastFixture(t *testing.T, startedFile string) string {
	t.Helper()
	template, err := os.ReadFile(filepath.Join("testdata", "fail_fast", "main.r42.tmpl"))
	require.NoError(t, err)
	program, err := json.Marshal(os.Args[0])
	require.NoError(t, err)
	started, err := json.Marshal(startedFile)
	require.NoError(t, err)
	source := strings.ReplaceAll(string(template), "__PROGRAM__", string(program))
	source = strings.ReplaceAll(source, "__STARTED_FILE__", string(started))
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42"), []byte(source), 0o600))
	return directory
}

type failFastState struct {
	mu             sync.Mutex
	startedFile    string
	slowErr        error
	slowStopped    chan struct{}
	closedSessions int
}

type failFastOpener struct{ state *failFastState }

func (o *failFastOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	if strings.Contains(config.SystemPrompt, "TRIGGER_FAIL_FAST") {
		return &failSession{state: o.state}, nil
	}
	return &slowExternalSession{state: o.state, config: config}, nil
}

type failSession struct{ state *failFastState }

func (s *failSession) SendAndWait(ctx context.Context, _ sdk.MessageOptions) (*sdk.SessionEvent, error) {
	for {
		if _, err := os.Stat(s.state.startedFile); err == nil {
			return nil, errors.New("trigger fail-fast")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (s *failSession) Close(context.Context) error {
	s.state.mu.Lock()
	s.state.closedSessions++
	s.state.mu.Unlock()
	return nil
}

type slowExternalSession struct {
	state  *failFastState
	config copilot.SessionConfig
}

func (s *slowExternalSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	_, err := findTool(s.config.Tools, "external_tool_blocker").Handler(sdk.ToolInvocation{Arguments: map[string]any{}})
	s.state.mu.Lock()
	s.state.slowErr = err
	close(s.state.slowStopped)
	s.state.mu.Unlock()
	return nil, err
}

func (s *slowExternalSession) Close(context.Context) error {
	s.state.mu.Lock()
	s.state.closedSessions++
	s.state.mu.Unlock()
	return nil
}

//nolint:paralleltest // Helper subprocess intentionally owns its lifecycle and blocks until killed.
func TestE2EHelperProcess(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+2 >= len(os.Args) || os.Args[separator+1] != "block" {
		return
	}
	if err := os.WriteFile(os.Args[separator+2], []byte("started"), 0o600); err != nil {
		os.Exit(2)
	}
	for {
		time.Sleep(time.Hour)
	}
}

type terminalArtifactOpener struct{}

func (*terminalArtifactOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	return &terminalArtifactSession{config: config}, nil
}

type terminalArtifactSession struct{ config copilot.SessionConfig }

func (s *terminalArtifactSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	if err := os.WriteFile(filepath.Join(s.config.WorkingDirectory, "report.md"), []byte("report"), 0o600); err != nil {
		return nil, err
	}
	evidence := filepath.Join(s.config.WorkingDirectory, "evidence")
	if err := os.MkdirAll(evidence, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(evidence, "source.txt"), []byte("source"), 0o600); err != nil {
		return nil, err
	}
	tool := findTool(s.config.Tools, "go_tool_finish")
	_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"Summary": "research complete"}})
	return &sdk.SessionEvent{}, err
}

func (*terminalArtifactSession) Close(context.Context) error { return nil }

type repairOpener struct {
	mu          sync.Mutex
	opens       int
	sends       int
	firstResult string
}

func (o *repairOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	o.opens++
	return &repairSession{opener: o, config: config}, nil
}

type repairSession struct {
	opener *repairOpener
	config copilot.SessionConfig
}

func (s *repairSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.opener.mu.Lock()
	s.opener.sends++
	call := s.opener.sends
	s.opener.mu.Unlock()
	arguments := map[string]any{"Summary": "repaired"}
	if call == 1 {
		arguments = map[string]any{}
	}
	result, err := findTool(s.config.Tools, "go_tool_finish").Handler(sdk.ToolInvocation{Arguments: arguments})
	if call == 1 {
		s.opener.firstResult = result.TextResultForLLM
	}
	return &sdk.SessionEvent{}, err
}

func (*repairSession) Close(context.Context) error { return nil }

type qcScenarioOpener struct {
	opens    int
	research promptScenarioSession
	qc       qcScenarioSession
}

func (o *qcScenarioOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	o.opens++
	if findTool(config.Tools, "r42_qc_verdict").Name == "" {
		return &o.research, nil
	}
	o.qc.config = config
	return &o.qc, nil
}

type promptScenarioSession struct {
	sends, closes int
	prompts       []string
}

func (s *promptScenarioSession) SendAndWait(_ context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.sends++
	s.prompts = append(s.prompts, options.Prompt)
	return &sdk.SessionEvent{}, nil
}

func (s *promptScenarioSession) Close(context.Context) error { s.closes++; return nil }

type qcScenarioSession struct {
	sends, closes int
	config        copilot.SessionConfig
}

func (s *qcScenarioSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.sends++
	arguments := map[string]any{"pass": true}
	if s.sends == 1 {
		arguments = map[string]any{
			"pass":   false,
			"issues": []any{map[string]any{"code": "missing_source", "message": "add a citation"}},
		}
	}
	_, err := findTool(s.config.Tools, "r42_qc_verdict").Handler(sdk.ToolInvocation{Arguments: arguments})
	return &sdk.SessionEvent{}, err
}

func (s *qcScenarioSession) Close(context.Context) error { s.closes++; return nil }

type parallelTracker struct {
	mu                   sync.Mutex
	active, maximum      int
	started              int
	allStarted           chan struct{}
	allStartedCloseGuard sync.Once
}

type parallelOpener struct{ tracker *parallelTracker }

func (o *parallelOpener) Open(context.Context, copilot.SessionConfig) (cli.Session, error) {
	return &parallelSession{tracker: o.tracker}, nil
}

type parallelSession struct{ tracker *parallelTracker }

func (s *parallelSession) SendAndWait(ctx context.Context, _ sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.tracker.mu.Lock()
	s.tracker.active++
	s.tracker.started++
	if s.tracker.active > s.tracker.maximum {
		s.tracker.maximum = s.tracker.active
	}
	if s.tracker.started == 2 {
		s.tracker.allStartedCloseGuard.Do(func() { close(s.tracker.allStarted) })
	}
	s.tracker.mu.Unlock()
	select {
	case <-s.tracker.allStarted:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Second):
		return nil, assert.AnError
	}
	s.tracker.mu.Lock()
	s.tracker.active--
	s.tracker.mu.Unlock()
	return &sdk.SessionEvent{}, nil
}

func (*parallelSession) Close(context.Context) error { return nil }

func findTool(tools []sdk.Tool, name string) sdk.Tool {
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	return sdk.Tool{}
}

type scenarioOpener struct {
	mu      sync.Mutex
	opens   int
	session scenarioSession
}

func (o *scenarioOpener) Open(_ context.Context, _ copilot.SessionConfig) (cli.Session, error) {
	o.mu.Lock()
	o.opens++
	o.mu.Unlock()
	return &o.session, nil
}

type scenarioSession struct {
	mu            sync.Mutex
	sends, closes int
}

func (s *scenarioSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sends++
	s.mu.Unlock()
	return &sdk.SessionEvent{}, nil
}

func (s *scenarioSession) Close(context.Context) error {
	s.mu.Lock()
	s.closes++
	s.mu.Unlock()
	return nil
}
