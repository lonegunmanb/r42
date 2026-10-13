package cli

import (
	"errors"
	"testing"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
)

func TestCopilotClientOptionsUsesExplicitCLIPath(t *testing.T) {
	t.Parallel()
	options, err := copilotClientOptions(
		"/run/store",
		func(string) (string, bool) { return `C:\tools\copilot.exe`, true },
		func(string) (string, error) { return `C:\path\copilot.exe`, nil },
	)

	require.NoError(t, err)
	require.Equal(t, sdk.StdioConnection{Path: `C:\tools\copilot.exe`}, options.Connection)
	require.Equal(t, "/run/store", options.BaseDirectory)
}

func TestCopilotClientOptionsFindsSystemCLIWhenUnset(t *testing.T) {
	t.Parallel()
	options, err := copilotClientOptions(
		"/run/store",
		func(string) (string, bool) { return "", false },
		func(name string) (string, error) {
			require.Equal(t, "copilot", name)
			return "/usr/local/bin/copilot", nil
		},
	)

	require.NoError(t, err)
	require.Equal(t, sdk.StdioConnection{Path: "/usr/local/bin/copilot"}, options.Connection)
}

func TestCopilotClientOptionsRequiresSystemCLIWhenUnsetAndUnavailable(t *testing.T) {
	t.Parallel()
	lookupErr := errors.New("copilot not found")
	_, err := copilotClientOptions(
		"/run/store",
		func(string) (string, bool) { return "", false },
		func(string) (string, error) { return "", lookupErr },
	)

	require.ErrorIs(t, err, lookupErr)
}
