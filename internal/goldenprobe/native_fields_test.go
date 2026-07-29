package goldenprobe

import (
	"testing"

	"github.com/Azure/golden"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	golden.RegisterBlock(new(nativeFieldsBlock))
	golden.RegisterBlock(new(objectReferenceBlock))
	golden.RegisterBlock(new(objectConsumerBlock))
}

type objectReferenceBlock struct {
	*golden.BaseBlock
	Description string `hcl:"description"`
}

func (*objectReferenceBlock) Type() string             { return "" }
func (*objectReferenceBlock) BlockType() string        { return "object_reference" }
func (*objectReferenceBlock) AddressLength() int       { return 2 }
func (*objectReferenceBlock) CanExecutePrePlan() bool  { return false }
func (*objectReferenceBlock) ExecuteDuringPlan() error { return nil }
func (b *objectReferenceBlock) Value() cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"address":     cty.StringVal(b.Address()),
		"description": cty.StringVal(b.Description),
		"kind":        cty.StringVal("fixture"),
	})
}

type objectConsumerBlock struct {
	*golden.BaseBlock
	Reference cty.Value `hcl:"reference"`
}

func (*objectConsumerBlock) Type() string             { return "" }
func (*objectConsumerBlock) BlockType() string        { return "object_consumer" }
func (*objectConsumerBlock) AddressLength() int       { return 2 }
func (*objectConsumerBlock) CanExecutePrePlan() bool  { return false }
func (*objectConsumerBlock) ExecuteDuringPlan() error { return nil }

type nativeFieldsBlock struct {
	*golden.BaseBlock
	Required      string   `hcl:"required"`
	Optional      string   `hcl:"optional,optional"`
	Enabled       bool     `hcl:"enabled,optional"`
	Count         int      `hcl:"count,optional"`
	Names         []string `hcl:"names,optional"`
	Retries       *int     `hcl:"retries,optional"`
	DefaultBool   bool     `hcl:"default_bool,optional" default:"true"`
	DefaultInt    int      `hcl:"default_int,optional" default:"10"`
	DefaultString string   `hcl:"default_string,optional" default:"fallback"`
}

func (*nativeFieldsBlock) Type() string { return "" }

func (*nativeFieldsBlock) BlockType() string { return "native_fields" }

func (*nativeFieldsBlock) AddressLength() int { return 2 }

func (*nativeFieldsBlock) CanExecutePrePlan() bool { return false }

func (*nativeFieldsBlock) ExecuteDuringPlan() error { return nil }

//nolint:paralleltest // Golden's block registry is process-global.
func TestGoldenDecodesNativePrimitiveFields(t *testing.T) {
	config, err := newProbeConfig(`
native_fields "explicit" {
  required = "value"
  optional = "set"
  enabled  = true
  count    = 3
  names    = ["a", "b"]
  retries  = 0
  default_bool   = false
  default_int    = 0
  default_string = ""
}

native_fields "defaults" {
  required = "value"
}
`, "", nil)
	require.NoError(t, err)
	require.NoError(t, config.RunPlan())

	blocks := golden.Blocks[*nativeFieldsBlock](config)
	require.Len(t, blocks, 2)
	byName := map[string]*nativeFieldsBlock{}
	for _, block := range blocks {
		byName[block.Name()] = block
	}

	explicit := byName["explicit"]
	require.NotNil(t, explicit)
	assert.Equal(t, "value", explicit.Required)
	assert.Equal(t, "set", explicit.Optional)
	assert.True(t, explicit.Enabled)
	assert.Equal(t, 3, explicit.Count)
	assert.Equal(t, []string{"a", "b"}, explicit.Names)
	require.NotNil(t, explicit.Retries)
	assert.Zero(t, *explicit.Retries)
	assert.True(t, explicit.DefaultBool)
	assert.Equal(t, 10, explicit.DefaultInt)
	assert.Equal(t, "fallback", explicit.DefaultString)

	defaults := byName["defaults"]
	require.NotNil(t, defaults)
	assert.Empty(t, defaults.Optional)
	assert.False(t, defaults.Enabled)
	assert.Zero(t, defaults.Count)
	assert.Nil(t, defaults.Names)
	assert.Nil(t, defaults.Retries)
	assert.True(t, defaults.DefaultBool)
	assert.Equal(t, 10, defaults.DefaultInt)
	assert.Equal(t, "fallback", defaults.DefaultString)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestGoldenDecodesBlockTraversalAsObjectValue(t *testing.T) {
	config, err := newProbeConfig(`
object_reference "tool" {
  description = "research tool"
}

object_consumer "session" {
  reference = object_reference.tool
}
`, "", nil)
	require.NoError(t, err)
	require.NoError(t, config.RunPlan())

	consumers := golden.Blocks[*objectConsumerBlock](config)
	require.Len(t, consumers, 1)
	reference := consumers[0].Reference
	require.True(t, reference.Type().IsObjectType())
	require.True(t, reference.Type().HasAttribute("description"))
	require.True(t, reference.Type().HasAttribute("address"))
	require.True(t, reference.Type().HasAttribute("kind"))
	assert.Equal(t, "research tool", reference.GetAttr("description").AsString())
	assert.Equal(t, "object_reference.tool", reference.GetAttr("address").AsString())
	assert.Equal(t, "fixture", reference.GetAttr("kind").AsString())

	ancestors, err := config.GetAncestors("object_consumer.session")
	require.NoError(t, err)
	assert.Contains(t, ancestors, "object_reference.tool")
}
