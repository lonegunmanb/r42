package external

import (
	"path/filepath"
	"testing"

	toolspec "github.com/lonegunmanb/r42/internal/tool/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestRunnerMatchesDeclaredOutputType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		constraint    string
		output        string
		expectedError string
	}{
		{name: "string", constraint: `string`, output: `"value"`},
		{name: "number", constraint: `number`, output: `42`},
		{name: "boolean", constraint: `bool`, output: `true`},
		{name: "list", constraint: `list(string)`, output: `["one","two"]`},
		{name: "set", constraint: `set(number)`, output: `[1,2]`},
		{name: "tuple", constraint: `tuple([string, number])`, output: `["one",2]`},
		{name: "map", constraint: `map(bool)`, output: `{"one":true}`},
		{
			name:       "object omits optional attribute",
			constraint: `object({ required = string, optional = optional(number) })`,
			output:     `{"required":"value"}`,
		},
		{
			name:       "object has null optional attribute",
			constraint: `object({ required = string, optional = optional(number) })`,
			output:     `{"required":"value","optional":null}`,
		},
		{name: "string rejects number", constraint: `string`, output: `42`, expectedError: "expected string"},
		{name: "number rejects string", constraint: `number`, output: `"42"`, expectedError: "expected number"},
		{name: "boolean rejects number", constraint: `bool`, output: `1`, expectedError: "expected boolean"},
		{name: "list rejects object", constraint: `list(string)`, output: `{}`, expectedError: "expected array"},
		{name: "list rejects null element", constraint: `list(string)`, output: `[null]`, expectedError: "null is not allowed"},
		{name: "tuple rejects length", constraint: `tuple([string])`, output: `[]`, expectedError: "expected 1 elements"},
		{name: "tuple rejects element", constraint: `tuple([string])`, output: `[1]`, expectedError: "expected string"},
		{name: "map rejects element", constraint: `map(string)`, output: `{"one":1}`, expectedError: "expected string"},
		{
			name:          "object rejects unknown attribute",
			constraint:    `object({ value = string })`,
			output:        `{"value":"ok","extra":true}`,
			expectedError: "undeclared attribute",
		},
		{
			name:          "object rejects missing attribute",
			constraint:    `object({ value = string })`,
			output:        `{}`,
			expectedError: "required attribute is missing",
		},
		{
			name:          "object rejects null required attribute",
			constraint:    `object({ value = string })`,
			output:        `{"value":null}`,
			expectedError: "null is not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			response := `{"accepted":true,"output":` + tt.output + `}`
			result, err := NewRunner().Run(t.Context(), Config{
				Program:   helperProgram("raw", response),
				Workspace: t.TempDir(),
				Input:     toolspec.NewConstraint(cty.EmptyObject),
				Output:    testConstraint(t, tt.constraint),
			}, cty.EmptyObjectVal)
			if tt.expectedError != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.expectedError)
				return
			}
			require.NoError(t, err)
			assert.True(t, result.Accepted)
			require.NotNil(t, result.Output)
		})
	}
}

func TestRunnerWorkingDirectoryRules(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	absolute := t.TempDir()
	tests := []struct {
		name       string
		workingDir string
		expected   string
	}{
		{name: "workspace default", expected: workspace},
		{name: "absolute", workingDir: absolute, expected: absolute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := NewRunner().Run(t.Context(), Config{
				Program:    helperProgram("cwd"),
				Workspace:  workspace,
				WorkingDir: tt.workingDir,
				Input:      toolspec.NewConstraint(cty.EmptyObject),
				Output:     toolspec.NewConstraint(cty.String),
			}, cty.EmptyObjectVal)
			require.NoError(t, err)
			require.NotNil(t, result.Output)
			assert.Equal(t, filepath.Clean(tt.expected), filepath.Clean(result.Output.AsString()))
		})
	}
}

func TestRunnerRejectsInvalidConfigurationAndInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		config        Config
		input         cty.Value
		expectedError string
	}{
		{name: "missing program", config: Config{Workspace: t.TempDir()}, input: cty.EmptyObjectVal, expectedError: "program must contain"},
		{name: "blank executable", config: Config{Program: []string{" "}, Workspace: t.TempDir()}, input: cty.EmptyObjectVal, expectedError: "program must contain"},
		{name: "missing workspace", config: Config{Program: []string{"missing"}}, input: cty.EmptyObjectVal, expectedError: "workspace is required"},
		{
			name: "invalid input is rejected before process start",
			config: Config{
				Program:   []string{"program-that-must-not-run"},
				Workspace: t.TempDir(),
				Input:     testConstraint(t, `object({ query = string })`),
			},
			input:         cty.EmptyObjectVal,
			expectedError: "validating external tool input",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewRunner().Run(t.Context(), tt.config, tt.input)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

func TestRunnerRejectsMalformedResponseEnvelope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		response      string
		expectedError string
	}{
		{name: "empty", expectedError: "EOF"},
		{name: "malformed", response: `{`, expectedError: "unexpected EOF"},
		{name: "unknown field", response: `{"accepted":true,"extra":1}`, expectedError: "unknown field"},
		{name: "rejected without issues", response: `{"accepted":false}`, expectedError: "must contain at least one issue"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewRunner().Run(t.Context(), Config{
				Program:   helperProgram("raw", tt.response),
				Workspace: t.TempDir(),
				Input:     toolspec.NewConstraint(cty.EmptyObject),
				Output:    toolspec.NewConstraint(cty.String),
			}, cty.EmptyObjectVal)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

func TestRunnerReportsMissingWorkingDirectoryAtApply(t *testing.T) {
	t.Parallel()

	_, err := NewRunner().Run(t.Context(), Config{
		Program:   helperProgram("cwd"),
		Workspace: filepath.Join(t.TempDir(), "missing"),
		Input:     toolspec.NewConstraint(cty.EmptyObject),
		Output:    toolspec.NewConstraint(cty.String),
	}, cty.EmptyObjectVal)
	require.Error(t, err)
	assert.ErrorContains(t, err, "running external tool")
}
