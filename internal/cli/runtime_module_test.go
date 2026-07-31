package cli_test

import (
	"context"
	"errors"
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
	"github.com/zclconf/go-cty/cty"
)

func TestProductionRuntimeAppliesSavedNestedModuleWithoutReparse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o700))
	childPath := filepath.Join(child, "main.r42")
	require.NoError(t, os.WriteFile(childPath, []byte(`
research "inside" {
  model = "test-model"
  system_prompt = "Work."
}
output "status" { value = "child-done" }
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.r42"), []byte(`
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
	assert.Equal(t, "module.child.research.inside", string(address))
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
	require.NoError(t, os.WriteFile(filepath.Join(child, "main.r42"), []byte(`
research "inside" {
  model = "test-model"
  system_prompt = "Work."
}
output "status" { value = "child-done" }
`), 0o600))
	moduleAttributes = strings.TrimSpace(moduleAttributes)
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.r42"), []byte(`
module "child" {
  source = "./child"
  `+moduleAttributes+`
}
output "status" { value = module.child.status }
`), 0o600))
	return root
}

type moduleBlockingOpener struct{ session moduleBlockingSession }

func (o *moduleBlockingOpener) Open(context.Context, copilot.SessionConfig) (cli.Session, error) {
	return &o.session, nil
}

type moduleBlockingSession struct{ fakeSession }

func (s *moduleBlockingSession) SendAndWait(ctx context.Context, _ sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.mu.Lock()
	s.sendCalls++
	s.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}
