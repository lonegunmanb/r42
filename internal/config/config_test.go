package config_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/config"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestLoadDirectoryContextRecordsDirectoryScanFailure(t *testing.T) {
	t.Parallel()

	logDirectory := t.TempDir()
	recorder, err := debuglog.NewRecorder(logDirectory, true)
	require.NoError(t, err)
	ctx := debuglog.WithRecorder(t.Context(), recorder)
	missing := filepath.Join(t.TempDir(), "missing")

	_, _, err = config.LoadDirectoryContext(ctx, missing)
	require.Error(t, err)
	require.NoError(t, recorder.Close())

	content, err := os.ReadFile(filepath.Join(logDirectory, debuglog.EventsFileName))
	require.NoError(t, err)
	var events []debuglog.Event
	for line := range bytes.SplitSeq(bytes.TrimSpace(content), []byte("\n")) {
		var event debuglog.Event
		require.NoError(t, json.Unmarshal(line, &event))
		events = append(events, event)
	}
	require.Len(t, events, 2)
	assert.Equal(t, debuglog.StatusStarted, events[0].Status)
	assert.Equal(t, debuglog.StatusFailed, events[1].Status)
	assert.Equal(t, "config.directory.scan", events[1].Action)
	assert.NotEmpty(t, events[1].Error)
}

func TestLoadDirectoryLoadsR42HCLFilesInStableOrder(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeFile(t, directory, "b.r42.hcl", "loader_fixture \"second\" {}\n")
	writeFile(t, directory, "a.r42.hcl", "loader_fixture \"first\" {}\n")
	writeFile(t, directory, "legacy.r42", "not hcl")
	writeFile(t, directory, "ignored.txt", "not hcl")
	require.NoError(t, os.Mkdir(filepath.Join(directory, "nested.r42.hcl"), 0o755))

	loaded, diagnostics, err := config.LoadDirectoryContext(t.Context(), directory)
	require.NoError(t, err)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	require.Len(t, loaded.Files, 2)
	require.Len(t, loaded.Blocks, 2)

	assert.Equal(t, []string{"a.r42.hcl", "b.r42.hcl"}, []string{
		filepath.Base(loaded.Files[0].Path),
		filepath.Base(loaded.Files[1].Path),
	})
	assert.Equal(t, "loader_fixture \"first\" {}\n", string(loaded.Files[0].Source))
	assert.Equal(t, "first", loaded.Blocks[0].Labels[0])
	assert.Equal(t, "second", loaded.Blocks[1].Labels[0])
}

func TestLoadDirectoryReturnsParserDiagnosticsWithSources(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeFile(t, directory, "broken.r42.hcl", "loader_fixture \"broken\" { value = }\n")
	writeFile(t, directory, "valid.r42.hcl", "loader_fixture \"valid\" {}\n")

	loaded, diagnostics, err := config.LoadDirectoryContext(t.Context(), directory)
	require.NoError(t, err)
	require.True(t, diagnostics.HasErrors())
	assert.GreaterOrEqual(t, len(diagnostics), 2)
	require.Len(t, loaded.Files, 2)
	require.Len(t, loaded.Blocks, 1)
	assert.Equal(t, "valid", loaded.Blocks[0].Labels[0])

	for _, diagnostic := range diagnostics {
		require.NotNil(t, diagnostic.Subject)
		assert.Equal(t, filepath.Join(directory, "broken.r42.hcl"), diagnostic.Subject.Filename)
		assert.Positive(t, diagnostic.Subject.Start.Line)
		assert.Positive(t, diagnostic.Subject.Start.Column)
	}
	assert.Equal(t, "loader_fixture \"broken\" { value = }\n", string(loaded.Files[0].Source))
}

func TestLoadDirectoryRejectsNonDirectory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.r42")
	require.NoError(t, os.WriteFile(path, []byte("loader_fixture \"only\" {}\n"), 0o600))

	loaded, diagnostics, err := config.LoadDirectoryContext(t.Context(), path)
	require.Error(t, err)
	assert.Empty(t, loaded.Files)
	assert.Empty(t, loaded.Blocks)
	assert.Empty(t, diagnostics)
}

func TestToolNameConvertsTypedAddresses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		address  config.Address
		expected string
	}{
		{
			name:     "go tool",
			address:  config.Address{Kind: config.AddressKindGo, Value: "go_tool.finish"},
			expected: "go_tool_finish",
		},
		{
			name:     "external tool",
			address:  config.Address{Kind: config.AddressKindExternal, Value: "external_tool.search.catalog"},
			expected: "external_tool_search_catalog",
		},
		{
			name:     "built in tool",
			address:  config.Address{Kind: config.AddressKindBuiltin, Value: "builtin.web.search"},
			expected: "builtin_web_search",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := config.Functions()["tool_name"].Call([]cty.Value{tt.address.CtyValue()})
			require.NoError(t, err)
			assert.Equal(t, tt.expected, actual.AsString())

			address, ok := config.AddressFromValue(tt.address.CtyValue())
			assert.True(t, ok)
			assert.Equal(t, tt.address, address)
		})
	}
}

func TestToolNameReturnsTypedToolIDWhenPresent(t *testing.T) {
	t.Parallel()

	input := cty.ObjectVal(map[string]cty.Value{
		"id":      cty.StringVal("tool_go_tool_finish_12345678-1234-8234-9234-123456789abc"),
		"address": cty.StringVal("go_tool.finish"),
		"kind":    cty.StringVal("go"),
	})

	actual, err := config.Functions()["tool_name"].Call([]cty.Value{input})

	require.NoError(t, err)
	assert.Equal(t, "tool_go_tool_finish_12345678-1234-8234-9234-123456789abc", actual.AsString())
}

func TestToolNameFallsBackToAddressWhenOptionalIDIsInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		id            cty.Value
		expectedError string
	}{
		{name: "null", id: cty.NullVal(cty.String)},
		{name: "unknown", id: cty.UnknownVal(cty.String), expectedError: "tool_name requires a typed tool address"},
		{name: "wrong type", id: cty.NumberIntVal(1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := cty.ObjectVal(map[string]cty.Value{
				"id":      tt.id,
				"address": cty.StringVal("go_tool.finish"),
				"kind":    cty.StringVal("go"),
			})

			actual, err := config.Functions()["tool_name"].Call([]cty.Value{input})

			if tt.expectedError != "" {
				assert.ErrorContains(t, err, tt.expectedError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "go_tool_finish", actual.AsString())
		})
	}
}

func TestToolNamePreservesNonAddressMarks(t *testing.T) {
	t.Parallel()

	input := config.Address{
		Kind:  config.AddressKindGo,
		Value: "go_tool.finish",
	}.CtyValue().Mark("sensitive")
	actual, err := config.Functions()["tool_name"].Call([]cty.Value{input})
	require.NoError(t, err)

	assert.True(t, actual.HasMark("sensitive"))
	_, isAddress := config.AddressFromValue(actual)
	assert.False(t, isAddress)
}

func TestToolNameRejectsUntypedAddresses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input cty.Value
	}{
		{name: "plain string", input: cty.StringVal("go_tool.finish")},
		{name: "unknown object", input: cty.UnknownVal(cty.Object(map[string]cty.Type{
			"address": cty.String,
			"kind":    cty.String,
		}))},
		{name: "dynamic value", input: cty.DynamicVal},
		{
			name:  "unknown address kind",
			input: config.Address{Kind: config.AddressKindUnknown, Value: "go_tool.finish"}.CtyValue(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Functions()["tool_name"].Call([]cty.Value{tt.input})
			assert.ErrorContains(t, err, "typed tool address")
		})
	}
}

func TestToolNameReportsTheInvalidArgumentRange(t *testing.T) {
	t.Parallel()

	expression, parseDiagnostics := hclsyntax.ParseExpression(
		[]byte(`tool_name("plain")`),
		"input.r42",
		hcl.InitialPos,
	)
	require.False(t, parseDiagnostics.HasErrors(), parseDiagnostics.Error())
	_, diagnostics := expression.Value(&hcl.EvalContext{Functions: config.Functions()})
	require.True(t, diagnostics.HasErrors())
	require.NotNil(t, diagnostics[0].Subject)
	assert.Equal(t, 12, diagnostics[0].Subject.Start.Column)
	assert.Equal(t, 17, diagnostics[0].Subject.End.Column)
}

func TestCWDReturnsSlashNormalizedWorkingDirectory(t *testing.T) {
	t.Parallel()

	workingDirectory, err := os.Getwd()
	require.NoError(t, err)
	cwdFunction, exists := config.Functions()["cwd"]
	require.True(t, exists)

	actual, err := cwdFunction.Call(nil)

	require.NoError(t, err)
	assert.Equal(t, filepath.ToSlash(workingDirectory), actual.AsString())
	assert.NotContains(t, actual.AsString(), `\`)
}

func TestCWDRejectsArguments(t *testing.T) {
	t.Parallel()

	cwdFunction, exists := config.Functions()["cwd"]
	require.True(t, exists)

	_, err := cwdFunction.Call([]cty.Value{cty.StringVal("unexpected")})

	require.Error(t, err)
}

func TestOneSelectsOnlyCollectionElement(t *testing.T) {
	t.Parallel()

	oneFunction, exists := config.Functions()["one"]
	require.True(t, exists)
	tests := []struct {
		name      string
		input     cty.Value
		expected  cty.Value
		errorText string
	}{
		{name: "list", input: cty.ListVal([]cty.Value{cty.StringVal("only")}), expected: cty.StringVal("only")},
		{name: "set", input: cty.SetVal([]cty.Value{cty.StringVal("only")}), expected: cty.StringVal("only")},
		{name: "tuple", input: cty.TupleVal([]cty.Value{cty.StringVal("only")}), expected: cty.StringVal("only")},
		{name: "empty", input: cty.ListValEmpty(cty.String), expected: cty.NullVal(cty.String)},
		{name: "empty tuple", input: cty.EmptyTupleVal, expected: cty.NullVal(cty.DynamicPseudoType)},
		{name: "dynamic unknown", input: cty.DynamicVal, expected: cty.DynamicVal},
		{name: "multiple", input: cty.ListVal([]cty.Value{cty.StringVal("one"), cty.StringVal("two")}), errorText: "zero or one"},
		{name: "null", input: cty.NullVal(cty.List(cty.String)), errorText: "must not be null"},
		{name: "not collection", input: cty.StringVal("only"), errorText: "list, set, or tuple"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actual, err := oneFunction.Call([]cty.Value{test.input})
			if test.errorText != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, test.errorText)
				return
			}
			require.NoError(t, err)
			assert.True(t, test.expected.RawEquals(actual))
		})
	}
}

func TestAddressFromValueRejectsNonAddresses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input cty.Value
	}{
		{name: "nil value", input: cty.NilVal},
		{name: "number", input: cty.NumberIntVal(1)},
		{name: "unknown object", input: cty.UnknownVal(cty.Object(map[string]cty.Type{
			"address": cty.String,
			"kind":    cty.String,
		}))},
		{name: "null object", input: cty.NullVal(cty.Object(map[string]cty.Type{
			"address": cty.String,
			"kind":    cty.String,
		}))},
		{name: "unmarked string", input: cty.StringVal("go_tool.finish")},
		{
			name:  "unknown address kind",
			input: config.Address{Kind: config.AddressKindUnknown, Value: "go_tool.finish"}.CtyValue(),
		},
		{
			name: "kind and address disagree",
			input: cty.ObjectVal(map[string]cty.Value{
				"address": cty.StringVal("external_tool.finish"),
				"kind":    cty.StringVal("go"),
			}),
		},
		{
			name: "missing address attribute",
			input: cty.ObjectVal(map[string]cty.Value{
				"kind": cty.StringVal("go"),
			}),
		},
		{
			name: "missing kind attribute",
			input: cty.ObjectVal(map[string]cty.Value{
				"address": cty.StringVal("go_tool.finish"),
			}),
		},
		{
			name: "address attribute has wrong type",
			input: cty.ObjectVal(map[string]cty.Value{
				"address": cty.NumberIntVal(1),
				"kind":    cty.StringVal("go"),
			}),
		},
		{
			name: "kind attribute has wrong type",
			input: cty.ObjectVal(map[string]cty.Value{
				"address": cty.StringVal("go_tool.finish"),
				"kind":    cty.NumberIntVal(1),
			}),
		},
		{
			name: "unknown address attribute",
			input: cty.ObjectVal(map[string]cty.Value{
				"address": cty.UnknownVal(cty.String),
				"kind":    cty.StringVal("go"),
			}),
		},
		{
			name: "unknown block attribute",
			input: cty.ObjectVal(map[string]cty.Value{
				"address":     cty.StringVal("go_tool.finish"),
				"kind":        cty.StringVal("go"),
				"description": cty.UnknownVal(cty.String),
			}),
		},
		{
			name: "null kind attribute",
			input: cty.ObjectVal(map[string]cty.Value{
				"address": cty.StringVal("go_tool.finish"),
				"kind":    cty.NullVal(cty.String),
			}),
		},
		{
			name: "null address attribute",
			input: cty.ObjectVal(map[string]cty.Value{
				"address": cty.NullVal(cty.String),
				"kind":    cty.StringVal("go"),
			}),
		},
		{
			name: "marked address attribute",
			input: cty.ObjectVal(map[string]cty.Value{
				"address": cty.StringVal("go_tool.finish").Mark("sensitive"),
				"kind":    cty.StringVal("go"),
			}),
		},
		{
			name: "marked kind attribute",
			input: cty.ObjectVal(map[string]cty.Value{
				"address": cty.StringVal("go_tool.finish"),
				"kind":    cty.StringVal("go").Mark("sensitive"),
			}),
		},
		{
			name: "marked block attribute",
			input: cty.ObjectVal(map[string]cty.Value{
				"address":     cty.StringVal("go_tool.finish"),
				"kind":        cty.StringVal("go"),
				"description": cty.StringVal("Finish").Mark("sensitive"),
			}),
		},
		{
			name:  "go tool missing name",
			input: config.Address{Kind: config.AddressKindGo, Value: "go_tool."}.CtyValue(),
		},
		{
			name:  "empty built in address",
			input: config.Address{Kind: config.AddressKindBuiltin}.CtyValue(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, ok := config.AddressFromValue(tt.input)
			assert.False(t, ok)
		})
	}
}

func TestAddressFromValueAllowsBlockAttributes(t *testing.T) {
	t.Parallel()

	value := cty.ObjectVal(map[string]cty.Value{
		"address":     cty.StringVal("go_tool.finish"),
		"kind":        cty.StringVal("go"),
		"description": cty.StringVal("Finish research"),
		"source":      cty.StringVal("package main"),
	})

	address, ok := config.AddressFromValue(value)
	require.True(t, ok)
	assert.Equal(t, config.Address{Kind: config.AddressKindGo, Value: "go_tool.finish"}, address)
}

func TestAddressCtyValueIsAnObject(t *testing.T) {
	t.Parallel()

	value := config.Address{Kind: config.AddressKindGo, Value: "go_tool.finish"}.CtyValue()
	require.True(t, value.Type().Equals(cty.Object(map[string]cty.Type{
		"address": cty.String,
		"kind":    cty.String,
	})))
	assert.Equal(t, "go_tool.finish", value.GetAttr("address").AsString())
	assert.Equal(t, "go", value.GetAttr("kind").AsString())
}

func TestNewBaseConfigIncludesGoldenEnvAndR42Functions(t *testing.T) {
	t.Setenv("R42_CONFIG_ENV", "from-environment")

	base := config.NewBaseConfig(golden.NewBaseConfigArgs{
		Basedir:         t.TempDir(),
		Ctx:             context.Background(),
		DslFullName:     "ignored",
		DslAbbreviation: "ignored",
	})
	evaluation := base.EmptyEvalContext()

	environment, err := evaluation.Functions["env"].Call([]cty.Value{cty.StringVal("R42_CONFIG_ENV")})
	require.NoError(t, err)
	assert.Equal(t, "from-environment", environment.AsString())

	name, err := evaluation.Functions["tool_name"].Call([]cty.Value{
		config.Address{Kind: config.AddressKindBuiltin, Value: "builtin.web"}.CtyValue(),
	})
	require.NoError(t, err)
	assert.Equal(t, "builtin_web", name.AsString())

	cwdFunction, exists := evaluation.Functions["cwd"]
	require.True(t, exists)
	workingDirectory, err := cwdFunction.Call(nil)
	require.NoError(t, err)
	assert.NotEmpty(t, workingDirectory.AsString())
	assert.NotContains(t, workingDirectory.AsString(), `\`)
	assert.Equal(t, "r42", base.DslFullName())
	assert.Equal(t, "r42", base.DslAbbreviation())
}

func TestJSONDecodeWithType(t *testing.T) {
	t.Parallel()

	targetType := cty.Object(map[string]cty.Type{"name": cty.String})
	result, err := config.Functions()["jsondecodewithtype"].Call([]cty.Value{
		cty.StringVal(`{"name":"r42"}`),
		typeexpr.TypeConstraintVal(targetType),
	})
	require.NoError(t, err)
	assert.Equal(t, targetType, result.Type())
	assert.Equal(t, "r42", result.GetAttr("name").AsString())
	expression, diagnostics := hclsyntax.ParseExpression(
		[]byte(`jsondecodewithtype("{\"name\":\"r42\"}", object({name = string}))`),
		"config.r42.hcl", hcl.InitialPos,
	)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	value, diagnostics := expression.Value(&hcl.EvalContext{Functions: config.Functions()})
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	assert.Equal(t, "r42", value.GetAttr("name").AsString())
}

var registerFixtureBlocks sync.Once

// Golden's block registry is process-global, so this test must remain serial.
func TestGoldenHandoffKeepsImplicitAndExplicitDependencies(t *testing.T) {
	registerFixtureBlocks.Do(func() {
		golden.RegisterBlock(new(fixtureTool))
		golden.RegisterBlock(new(fixtureConsumer))
	})
	t.Setenv("R42_FIXTURE_VALUE", "from-environment")

	directory := t.TempDir()
	writeFile(t, directory, "main.r42.hcl", `
fixture_tool "implicit" {}
fixture_tool "explicit" {}

fixture_consumer "target" {
  tool_name   = tool_name(fixture_tool.implicit)
  environment = env("R42_FIXTURE_VALUE")
  depends_on  = [fixture_tool.explicit]
}
`)
	loaded, diagnostics, err := config.LoadDirectoryContext(t.Context(), directory)
	require.NoError(t, err)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())

	base := config.NewBaseConfig(golden.NewBaseConfigArgs{Basedir: directory})
	configured := &fixtureConfig{BaseConfig: base}
	require.NoError(t, golden.InitConfig(configured, loaded.Blocks))
	require.NoError(t, configured.RunPlan())

	consumers := golden.Blocks[*fixtureConsumer](configured)
	require.Len(t, consumers, 1)
	assert.Equal(t, "fixture_tool_implicit", consumers[0].ToolName)
	assert.Equal(t, "from-environment", consumers[0].Environment)
	ancestors, err := configured.GetAncestors("fixture_consumer.target")
	require.NoError(t, err)
	assert.Contains(t, ancestors, "fixture_tool.implicit")
	assert.Contains(t, ancestors, "fixture_tool.explicit")
}

type fixtureConfig struct {
	*golden.BaseConfig
}

type fixtureTool struct {
	*golden.BaseBlock
}

func (*fixtureTool) Type() string { return "" }

func (*fixtureTool) BlockType() string { return "fixture_tool" }

func (*fixtureTool) AddressLength() int { return 2 }

func (*fixtureTool) CanExecutePrePlan() bool { return false }

func (*fixtureTool) ExecuteDuringPlan() error { return nil }

func (t *fixtureTool) Value() cty.Value {
	return config.Address{Kind: config.AddressKindBuiltin, Value: t.Address()}.CtyValue()
}

type fixtureConsumer struct {
	*golden.BaseBlock
	ToolName    string `hcl:"tool_name"`
	Environment string `hcl:"environment"`
}

func (*fixtureConsumer) Type() string { return "" }

func (*fixtureConsumer) BlockType() string { return "fixture_consumer" }

func (*fixtureConsumer) AddressLength() int { return 2 }

func (*fixtureConsumer) CanExecutePrePlan() bool { return false }

func (*fixtureConsumer) ExecuteDuringPlan() error { return nil }

func writeFile(t *testing.T, directory, name, source string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(source), 0o600))
}
