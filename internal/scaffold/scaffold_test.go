package scaffold

import (
	"runtime/debug"
	"testing"

	_ "github.com/Azure/golden"
	_ "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestModulePath(t *testing.T) {
	t.Parallel()

	info, ok := debug.ReadBuildInfo()
	require.True(t, ok)
	assert.Equal(t, "github.com/lonegunmanb/r42", info.Main.Path)
}
