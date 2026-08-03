package gotool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompilerBuildsCachesInvokesAndCleansProgram(t *testing.T) {
	t.Parallel()
	compiler, err := NewCompiler()
	require.NoError(t, err)

	program, err := compiler.Compile(t.Context(), readFixture(t, "decision.go.txt"))
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(program.Path()))
	_, err = os.Stat(program.Path())
	require.NoError(t, err)

	cached, err := compiler.Compile(t.Context(), readFixture(t, "decision.go.txt"))
	require.NoError(t, err)
	assert.Equal(t, program.Path(), cached.Path())

	accepted, err := program.Invoke(t.Context(), json.RawMessage(`{"action":"accept"}`), "")
	require.NoError(t, err)
	assert.True(t, accepted.Accepted)
	require.NotNil(t, accepted.Output)
	assert.JSONEq(t, `{"message":"done"}`, string(*accepted.Output))
	assert.Empty(t, accepted.Issues)

	rejected, err := program.Invoke(t.Context(), json.RawMessage(`{"action":"reject"}`), "")
	require.NoError(t, err)
	assert.False(t, rejected.Accepted)
	assert.Nil(t, rejected.Output)
	require.Len(t, rejected.Issues, 1)
	assert.Equal(t, "invalid_action", rejected.Issues[0].Code)

	_, err = program.Invoke(t.Context(), json.RawMessage(`{"action":"error"}`), "")
	require.Error(t, err)
	require.ErrorContains(t, err, "tool returned an error")
	var invocationErr *InvocationError
	require.ErrorAs(t, err, &invocationErr)
	assert.Contains(t, invocationErr.Stderr(), "tool returned an error")

	_, err = program.Invoke(t.Context(), json.RawMessage(`{"action":"invalid_response"}`), "")
	require.Error(t, err)
	require.ErrorContains(t, err, "running inline Go tool")
	require.ErrorContains(t, err, "issue 0 code is required")
	require.ErrorAs(t, err, &invocationErr)

	path := program.Path()
	require.NoError(t, compiler.Close())
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.NoError(t, compiler.Close())
}

func TestCompilerCloseRetriesFailedCleanup(t *testing.T) {
	t.Parallel()
	compiler, err := NewCompiler()
	require.NoError(t, err)

	removeAttempts := 0
	compiler.removeAll = func(path string) error {
		removeAttempts++
		if removeAttempts == 1 {
			return errors.New("directory is busy")
		}
		return os.RemoveAll(path)
	}

	require.ErrorContains(t, compiler.Close(), "directory is busy")
	require.NoError(t, compiler.Close())
	assert.Equal(t, 2, removeAttempts)
}

func TestCompilerReportsBuildErrors(t *testing.T) {
	t.Parallel()
	compiler, err := NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { _ = compiler.Close() })

	_, err = compiler.Compile(context.Background(), readFixture(t, "build_error.go.txt"))
	require.Error(t, err)
	assert.ErrorContains(t, err, "compiling inline Go tool")
}

func TestProgramInvokeUsesWorkingDirectory(t *testing.T) {
	t.Parallel()
	compiler, err := NewCompiler()
	require.NoError(t, err)
	t.Cleanup(func() { _ = compiler.Close() })

	program, err := compiler.Compile(t.Context(), readFixture(t, "working_directory.go.txt"))
	require.NoError(t, err)
	workingDirectory := t.TempDir()

	response, err := program.Invoke(t.Context(), json.RawMessage(`{}`), workingDirectory)
	require.NoError(t, err)
	require.NotNil(t, response.Output)
	var output struct {
		WorkingDirectory string `json:"working_directory"`
	}
	require.NoError(t, json.Unmarshal(*response.Output, &output))
	assert.Equal(t, workingDirectory, output.WorkingDirectory)
}
