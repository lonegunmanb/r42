package external

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	toolspec "github.com/lonegunmanb/r42/internal/tool/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestRunnerAppliesDefaultsAndInvokesProgram(t *testing.T) {
	workspace := t.TempDir()
	workingDir := filepath.Join(workspace, "child")
	require.NoError(t, os.Mkdir(workingDir, 0o700))
	t.Setenv("R42_EXTERNAL_TEST", "inherited")

	runner := NewRunner()
	result, err := runner.Run(t.Context(), Config{
		Program:    helperProgram("success"),
		Workspace:  workspace,
		WorkingDir: "child",
		Input:      testConstraint(t, `object({ query = string, limit = optional(number, 20) })`),
		Output: testConstraint(t, `object({
			query = string
			limit = number
			working_dir = string
			environment = string
		})`),
	}, cty.ObjectVal(map[string]cty.Value{"query": cty.StringVal("energy")}))
	require.NoError(t, err)
	assert.True(t, result.Accepted)
	require.NotNil(t, result.Output)
	assert.Equal(t, "energy", result.Output.GetAttr("query").AsString())
	assert.True(t, cty.NumberIntVal(20).RawEquals(result.Output.GetAttr("limit")))
	assert.Equal(t, workingDir, result.Output.GetAttr("working_dir").AsString())
	assert.Equal(t, "inherited", result.Output.GetAttr("environment").AsString())
	assert.Equal(t, "success diagnostic\n", result.Stderr)
}

func TestRunnerValidatesResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mode          string
		expectedError string
		assertResult  func(*testing.T, Result)
	}{
		{
			name: "rejected response",
			mode: "reject",
			assertResult: func(t *testing.T, result Result) {
				t.Helper()
				assert.False(t, result.Accepted)
				require.Len(t, result.Issues, 1)
				assert.Equal(t, "bad_query", result.Issues[0].Code)
			},
		},
		{name: "multiple documents", mode: "multiple", expectedError: "expected exactly one JSON value"},
		{name: "invalid accepted response", mode: "invalid", expectedError: "accepted response must not contain issues"},
		{name: "wrong output type", mode: "wrong_output", expectedError: "decoding external tool output"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := NewRunner().Run(t.Context(), Config{
				Program:   helperProgram(tt.mode),
				Workspace: t.TempDir(),
				Input:     toolspec.NewConstraint(cty.EmptyObject),
				Output:    toolspec.NewConstraint(cty.String),
			}, cty.EmptyObjectVal)
			if tt.expectedError != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.expectedError)
				return
			}
			require.NoError(t, err)
			tt.assertResult(t, result)
		})
	}
}

func TestRunnerReportsNonzeroExitWithStderrTail(t *testing.T) {
	t.Parallel()

	_, err := NewRunner().Run(t.Context(), Config{
		Program:   helperProgram("nonzero"),
		Workspace: t.TempDir(),
		Input:     toolspec.NewConstraint(cty.EmptyObject),
		Output:    toolspec.NewConstraint(cty.String),
	}, cty.EmptyObjectVal)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "ignored stdout")
	assert.LessOrEqual(t, len(err.Error()), ErrorStderrTailBytes+1024)
	assert.Contains(t, err.Error(), strings.Repeat("z", 1024))

	var executionError *ExecutionError
	require.ErrorAs(t, err, &executionError)
	assert.Contains(t, executionError.Stdout(), "ignored stdout")
	assert.Greater(t, len(executionError.Stderr()), ErrorStderrTailBytes)
	assert.Contains(t, executionError.Stderr(), "BEGIN")
	assert.NotContains(t, err.Error(), "BEGIN")
	assert.Contains(t, err.Error(), "END")
}

func TestRunnerEnforcesStreamLimits(t *testing.T) {
	t.Parallel()
	assert.Equal(t, int64(100<<20), int64(MaxStreamBytes))

	tests := []struct {
		name string
		mode string
	}{
		{name: "stdout", mode: "overflow_stdout"},
		{name: "stderr", mode: "overflow_stderr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := NewRunner()
			runner.maxStreamBytes = 32
			_, err := runner.Run(t.Context(), Config{
				Program:   helperProgram(tt.mode),
				Workspace: t.TempDir(),
				Input:     toolspec.NewConstraint(cty.EmptyObject),
				Output:    toolspec.NewConstraint(cty.String),
			}, cty.EmptyObjectVal)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.name+" exceeded 100 MiB limit")
		})
	}
}

func TestRunnerCancellationTerminatesProcessTree(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	heartbeat := filepath.Join(workspace, "heartbeat")
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := NewRunner().Run(ctx, Config{
			Program:   helperProgram("spawn_child", heartbeat),
			Workspace: workspace,
			Input:     toolspec.NewConstraint(cty.EmptyObject),
			Output:    toolspec.NewConstraint(cty.String),
		}, cty.EmptyObjectVal)
		result <- err
	}()

	require.Eventually(t, func() bool {
		_, err := os.Stat(heartbeat)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
	cancel()
	err := <-result
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)

	before, readErr := os.ReadFile(heartbeat)
	require.NoError(t, readErr)
	time.Sleep(200 * time.Millisecond)
	after, readErr := os.ReadFile(heartbeat)
	require.NoError(t, readErr)
	assert.Equal(t, before, after)
}

//nolint:paralleltest // Helper subprocess modes use os.Exit and intentionally own process-global state.
func TestExternalHelper(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}

	mode := os.Args[separator+1]
	arguments := os.Args[separator+2:]
	switch mode {
	case "success":
		var input map[string]any
		require.NoError(t, json.NewDecoder(os.Stdin).Decode(&input))
		workingDir, err := os.Getwd()
		require.NoError(t, err)
		input["working_dir"] = workingDir
		input["environment"] = os.Getenv("R42_EXTERNAL_TEST")
		_, _ = fmt.Fprintln(os.Stderr, "success diagnostic")
		writeHelperJSON(t, map[string]any{"accepted": true, "output": input})
	case "reject":
		writeHelperJSON(t, map[string]any{
			"accepted": false,
			"issues":   []any{map[string]any{"code": "bad_query", "message": "query is invalid"}},
		})
	case "multiple":
		_, _ = fmt.Fprintln(os.Stdout, `{"accepted":true,"output":"one"}`)
		_, _ = fmt.Fprintln(os.Stdout, `{"accepted":true,"output":"two"}`)
	case "invalid":
		writeHelperJSON(t, map[string]any{
			"accepted": true,
			"output":   "value",
			"issues":   []any{map[string]any{"code": "bad", "message": "bad"}},
		})
	case "wrong_output":
		writeHelperJSON(t, map[string]any{"accepted": true, "output": 42})
	case "nonzero":
		_, _ = fmt.Fprintln(os.Stdout, "ignored stdout")
		_, _ = fmt.Fprintln(os.Stderr, "BEGIN"+strings.Repeat("z", ErrorStderrTailBytes+1024)+"END")
		os.Exit(7)
	case "overflow_stdout":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("o", 64))
	case "overflow_stderr":
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("e", 64))
	case "spawn_child":
		command := exec.Command(os.Args[0], "-test.run=^TestExternalHelper$", "--", "heartbeat", arguments[0])
		require.NoError(t, command.Start())
		for {
			time.Sleep(time.Hour)
		}
	case "heartbeat":
		for {
			heartbeat := strconv.FormatInt(time.Now().UnixNano(), 10)
			require.NoError(t, os.WriteFile(arguments[0], []byte(heartbeat), 0o600))
			time.Sleep(20 * time.Millisecond)
		}
	case "raw":
		_, _ = fmt.Fprint(os.Stdout, arguments[0])
	case "cwd":
		workingDir, err := os.Getwd()
		require.NoError(t, err)
		writeHelperJSON(t, map[string]any{"accepted": true, "output": workingDir})
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
	os.Exit(0)
}

func helperProgram(mode string, arguments ...string) []string {
	program := []string{os.Args[0], "-test.run=^TestExternalHelper$", "--", mode}
	return append(program, arguments...)
}

func writeHelperJSON(t *testing.T, value any) {
	t.Helper()
	require.NoError(t, json.NewEncoder(os.Stdout).Encode(value))
}

func testConstraint(t *testing.T, source string) toolspec.Constraint {
	t.Helper()
	expression, diagnostics := hclsyntax.ParseExpression([]byte(source), "constraint.r42", hcl.Pos{Line: 1, Column: 1})
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	typeValue, defaults, diagnostics := typeexpr.TypeConstraintWithDefaults(expression)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	return toolspec.NewConstraintWithDefaults(typeValue, defaults)
}
