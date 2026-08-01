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
research "source" {
  model = "test-model"
  system_prompt = "Collect evidence."
  qc { criteria = { accuracy = "Must be accurate" } }
}
`), 0o600))
	opener := &qcOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), directory, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 1})

	require.NoError(t, err)
	require.Len(t, opener.configs, 2)
	assert.Empty(t, opener.configs[0].Tools)
	require.Len(t, opener.configs[1].Tools, 1)
	assert.Equal(t, "r42_qc_verdict", opener.configs[1].Tools[0].Name)
	assert.Equal(t, 1, opener.research.sendCalls)
	assert.Equal(t, 1, opener.qc.sendCalls)
	assert.Equal(t, 1, opener.research.closeCalls)
	assert.Equal(t, 1, opener.qc.closeCalls)
}

func TestProductionRuntimeAppliesResearchTimeoutAcrossQC(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "source" {
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
research "source" {
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
research "source" {
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
research "source" {
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
	assert.Equal(t, 2, opener.research.sendCalls)
	require.Len(t, opener.research.prompts, 2)
	assert.Contains(t, opener.research.prompts[1], "add a citation")
	assert.Equal(t, 2, opener.qc.sendCalls)
	assert.Equal(t, 1, opener.research.closeCalls)
	assert.Equal(t, 1, opener.qc.closeCalls)
}

type qcOpener struct {
	mu       sync.Mutex
	configs  []copilot.SessionConfig
	research countingSession
	qc       qcSession
}

func (o *qcOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	o.mu.Lock()
	o.configs = append(o.configs, config)
	index := len(o.configs)
	o.mu.Unlock()
	if index == 1 {
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
	research countingSession
	qc       blockingSession
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
	if len(config.Tools) == 0 {
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
	research promptSession
	qc       revisionQCSession
}

func (o *revisionQCOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	if len(config.Tools) == 0 {
		return &o.research, nil
	}
	o.qc.config = config
	return &o.qc, nil
}

type promptSession struct {
	countingSession
	prompts []string
}

func (s *promptSession) SendAndWait(_ context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	s.prompts = append(s.prompts, options.Prompt)
	s.mu.Unlock()
	return &sdk.SessionEvent{}, nil
}

type revisionQCSession struct {
	countingSession
	config copilot.SessionConfig
}

func (s *revisionQCSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	call := s.sendCalls
	s.mu.Unlock()
	arguments := map[string]any{"pass": true}
	if call == 1 {
		arguments = map[string]any{
			"pass":   false,
			"issues": []any{map[string]any{"code": "missing_source", "message": "add a citation"}},
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
			_, err := tool.Handler(sdk.ToolInvocation{Arguments: map[string]any{"pass": true}})
			return &sdk.SessionEvent{}, err
		}
	}
	return nil, assert.AnError
}
