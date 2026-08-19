package cli_test

import (
	"context"
	"errors"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestProductionRuntimeAppliesSavedNestedModuleWithoutReparse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o700))
	childPath := filepath.Join(child, "main.r42.hcl")
	require.NoError(t, os.WriteFile(childPath, []byte(`
research "static" "inside" {
  model = "test-model"
  system_prompt = "Work."
}
output "status" { value = "child-done" }
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.r42.hcl"), []byte(`
module "child" {
  source = "./child"
  parallelism = 2
}
output "status" { value = module.child.status }
`), 0o600))
	opener := &fakeSessionOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), root, nil)
	require.NoError(t, err)
	require.NoError(t, os.Remove(childPath))

	result, err := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 4})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("child-done"), result.Outputs["status"])
	assert.Equal(t, 1, opener.session.sendCalls)
	identities, err := os.ReadDir(filepath.Join(root, ".r42", "runs"))
	require.NoError(t, err)
	require.Len(t, identities, 1)
	addressFiles, err := os.ReadDir(filepath.Join(
		root, ".r42", "runs", identities[0].Name(), "block-addresses",
	))
	require.NoError(t, err)
	require.Len(t, addressFiles, 1)
	address, err := os.ReadFile(filepath.Join(
		root, ".r42", "runs", identities[0].Name(), "block-addresses", addressFiles[0].Name(),
	))
	require.NoError(t, err)
	assert.Equal(t, "module.child.research.static.inside", string(address))
}

func TestProductionRuntimePropagatesSessionStallTimeoutToChildSession(t *testing.T) {
	t.Parallel()
	root := writeModuleFixture(t, "")
	opener := &moduleRecoveringOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), root, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	result, err := applyRuntime(runtime, ctx, planned, executor.ResearchConfigOptions{
		Parallelism: 2, SessionStallTimeout: 5 * time.Millisecond,
	})

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("child-done"), result.Outputs["status"])
	sendCalls, abortCalls, resumeCalls := opener.session.counts()
	assert.Equal(t, 2, sendCalls)
	assert.Equal(t, 1, abortCalls)
	assert.Equal(t, 1, resumeCalls)
}

func TestProductionRuntimePropagatesModuleTimeoutToChildSession(t *testing.T) {
	t.Parallel()
	root := writeModuleFixture(t, `timeout = "50ms"`)
	opener := &moduleBlockingOpener{}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), root, nil)
	require.NoError(t, err)

	_, err = applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 1, opener.session.closeCalls)
}

func TestProductionRuntimePropagatesChildSessionCloseWarning(t *testing.T) {
	t.Parallel()
	root := writeModuleFixture(t, "")
	closeErr := errors.New("close child session failed")
	opener := &fakeSessionOpener{session: fakeSession{closeErr: closeErr}}
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: opener})
	planned, err := planRuntime(runtime, t.Context(), root, nil)
	require.NoError(t, err)

	result, err := applyRuntime(runtime, t.Context(), planned, executor.ResearchConfigOptions{Parallelism: 2})

	require.NoError(t, err)
	require.Len(t, result.Warnings, 1)
	assert.ErrorIs(t, result.Warnings[0], closeErr)
}

func writeModuleFixture(t *testing.T, moduleAttributes string) string {
	t.Helper()
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(child, "main.r42.hcl"), []byte(`
research "static" "inside" {
  model = "test-model"
  system_prompt = "Work."
}
output "status" { value = "child-done" }
`), 0o600))
	moduleAttributes = strings.TrimSpace(moduleAttributes)
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.r42.hcl"), []byte(`
module "child" {
  source = "./child"
  `+moduleAttributes+`
}
output "status" { value = module.child.status }
`), 0o600))
	return root
}

type moduleBlockingOpener struct{ session moduleBlockingSession }

func (o *moduleBlockingOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	if workflowSessionKind(config) == "research" {
		return &o.session, nil
	}
	return &protocolFixtureSession{config: config, session: &fakeSession{}}, nil
}

type moduleBlockingSession struct{ fakeSession }

func (s *moduleBlockingSession) SendAndWait(ctx context.Context, _ sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	s.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

type moduleRecoveringOpener struct{ session moduleRecoveringSession }

func (o *moduleRecoveringOpener) Open(_ context.Context, config copilot.SessionConfig) (cli.Session, error) {
	if workflowSessionKind(config) == "research" {
		return &o.session, nil
	}
	return &protocolFixtureSession{config: config, session: &fakeSession{}}, nil
}

type moduleRecoveringSession struct {
	mu          sync.Mutex
	sendCalls   int
	abortCalls  int
	resumeCalls int
}

func (s *moduleRecoveringSession) SendAndWait(
	ctx context.Context,
	_ sdk.MessageOptions,
) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	call := s.sendCalls
	s.mu.Unlock()
	if call > 1 {
		return &sdk.SessionEvent{ID: "assistant-final", Data: &sdk.AssistantMessageData{Content: "done"}}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *moduleRecoveringSession) Abort(context.Context) error {
	s.mu.Lock()
	s.abortCalls++
	s.mu.Unlock()
	return nil
}

func (s *moduleRecoveringSession) Resume(context.Context) error {
	s.mu.Lock()
	s.resumeCalls++
	s.mu.Unlock()
	return nil
}

func (*moduleRecoveringSession) Close(context.Context) error { return nil }

func (s *moduleRecoveringSession) counts() (int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sendCalls, s.abortCalls, s.resumeCalls
}
