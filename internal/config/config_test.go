package config_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Azure/golden"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/r42/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestLoadDirectoryLoadsR42FilesInStableOrder(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeFile(t, directory, "b.r42", "loader_fixture \"second\" {}\n")
	writeFile(t, directory, "a.r42", "loader_fixture \"first\" {}\n")
	writeFile(t, directory, "ignored.txt", "not hcl")
	require.NoError(t, os.Mkdir(filepath.Join(directory, "nested.r42"), 0o755))

	loaded, diagnostics, err := config.LoadDirectory(directory)
	require.NoError(t, err)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	require.Len(t, loaded.Files, 2)
	require.Len(t, loaded.Blocks, 2)

	assert.Equal(t, []string{"a.r42", "b.r42"}, []string{
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
	writeFile(t, directory, "broken.r42", "loader_fixture \"broken\" { value = }\n")
	writeFile(t, directory, "valid.r42", "loader_fixture \"valid\" {}\n")

	loaded, diagnostics, err := config.LoadDirectory(directory)
	require.NoError(t, err)
	require.True(t, diagnostics.HasErrors())
	assert.GreaterOrEqual(t, len(diagnostics), 2)
	require.Len(t, loaded.Files, 2)
	require.Len(t, loaded.Blocks, 1)
	assert.Equal(t, "valid", loaded.Blocks[0].Labels[0])

	for _, diagnostic := range diagnostics {
		require.NotNil(t, diagnostic.Subject)
		assert.Equal(t, filepath.Join(directory, "broken.r42"), diagnostic.Subject.Filename)
		assert.Positive(t, diagnostic.Subject.Start.Line)
		assert.Positive(t, diagnostic.Subject.Start.Column)
	}
	assert.Equal(t, "loader_fixture \"broken\" { value = }\n", string(loaded.Files[0].Source))
}

func TestLoadDirectoryRejectsNonDirectory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.r42")
	require.NoError(t, os.WriteFile(path, []byte("loader_fixture \"only\" {}\n"), 0o600))

	loaded, diagnostics, err := config.LoadDirectory(path)
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
		{name: "unknown string", input: cty.UnknownVal(cty.String)},
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

func TestAddressFromValueRejectsNonAddresses(t *testing.T) {
	t.Parallel()

	_, addressMarks := config.Address{Kind: config.AddressKindGo, Value: "go_tool.finish"}.CtyValue().Unmark()
	tests := []struct {
		name  string
		input cty.Value
	}{
		{name: "number", input: cty.NumberIntVal(1)},
		{name: "unknown string", input: cty.UnknownVal(cty.String)},
		{name: "null string", input: cty.NullVal(cty.String)},
		{name: "unmarked string", input: cty.StringVal("go_tool.finish")},
		{name: "mark value mismatch", input: cty.StringVal("go_tool.other").WithMarks(addressMarks)},
		{
			name:  "unknown address kind",
			input: config.Address{Kind: config.AddressKindUnknown, Value: "go_tool.finish"}.CtyValue(),
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

func TestAddressFromValueRejectsMultipleAddressMarks(t *testing.T) {
	t.Parallel()

	value := config.Address{Kind: config.AddressKindGo, Value: "shared.tool"}.CtyValue()
	_, secondMarks := config.Address{Kind: config.AddressKindExternal, Value: "shared.tool"}.CtyValue().Unmark()
	for mark := range secondMarks {
		value = value.Mark(mark)
	}

	_, ok := config.AddressFromValue(value)
	assert.False(t, ok)
}

func TestNewBaseConfigIncludesGoldenEnvAndToolName(t *testing.T) {
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
	assert.Equal(t, "r42", base.DslFullName())
	assert.Equal(t, "r42", base.DslAbbreviation())
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
	writeFile(t, directory, "main.r42", `
fixture_tool "implicit" {}
fixture_tool "explicit" {}

fixture_consumer "target" {
  tool_name   = tool_name(fixture_tool.implicit)
  environment = env("R42_FIXTURE_VALUE")
  depends_on  = [fixture_tool.explicit]
}
`)
	loaded, diagnostics, err := config.LoadDirectory(directory)
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
