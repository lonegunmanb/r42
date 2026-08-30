package spec

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/mcp"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

type ServerBlock struct {
	*golden.BaseBlock
	Tools       []string     `hcl:"tools,optional"`
	Resources   []string     `hcl:"resources,optional"`
	Timeout     *string      `hcl:"timeout,optional"`
	HTTPBlocks  []HTTPBlock  `hcl:"http,block"`
	StdioBlocks []StdioBlock `hcl:"stdio,block"`

	planned mcp.Config
}

type HTTPBlock struct {
	URL            string    `hcl:"url"`
	Headers        cty.Value `hcl:"headers,optional"`
	BearerToken    *string   `hcl:"bearer_token,optional"`
	BearerTokenRef *string   `hcl:"bearer_token_ref,optional"`
}

type StdioBlock struct {
	Command          *string   `hcl:"command,optional"`
	Args             []string  `hcl:"args,optional"`
	Env              cty.Value `hcl:"env,optional"`
	EnvRefs          cty.Value `hcl:"env_refs,optional"`
	WorkingDirectory string    `hcl:"working_directory,optional"`
}

var (
	_ golden.PlanBlock        = (*ServerBlock)(nil)
	_ golden.SingleValueBlock = (*ServerBlock)(nil)
)

func (*ServerBlock) Type() string { return "" }

func (*ServerBlock) BlockType() string { return "mcp_server" }

func (*ServerBlock) AddressLength() int { return 2 }

func (*ServerBlock) CanExecutePrePlan() bool { return false }

func (b *ServerBlock) ExecuteDuringPlan() error {
	return debuglog.PlanBlock(b.Context(), b.Address(), b.BlockType(), func() error {
		config, err := b.toConfig()
		if err != nil {
			return err
		}
		if err = config.Validate(); err != nil {
			return err
		}
		b.planned = config.Clone()
		return nil
	})
}

func (b *ServerBlock) ServerConfig() mcp.Config {
	result := b.planned.Clone()
	result.RuntimeName = b.CanonicalAddress()
	return result
}

func (b *ServerBlock) CanonicalAddress() string {
	canonical := b.Address()
	if b.BaseBlock != nil {
		if provider, ok := b.Config().(interface{ CanonicalAddress(string) string }); ok {
			canonical = provider.CanonicalAddress(b.Address())
		}
	}
	return canonical
}

func (b *ServerBlock) Value() cty.Value {
	toolIDs := make(map[string]cty.Value, len(b.Tools))
	for _, tool := range b.Tools {
		toolIDs[tool] = cty.StringVal(b.ToolID(tool))
	}
	return cty.ObjectVal(map[string]cty.Value{
		"address":      cty.StringVal(b.Address()),
		"kind":         cty.StringVal("mcp_server"),
		"name":         cty.StringVal(b.Name()),
		"tool_ids":     stringMapValue(toolIDs),
		"resource_ids": stringMapValue(b.resourceIDs()),
	})
}

func (b *ServerBlock) resourceIDs() map[string]cty.Value {
	result := make(map[string]cty.Value, len(b.Resources))
	for _, uri := range b.Resources {
		result[uri] = cty.StringVal(b.ResourceID(uri))
	}
	return result
}

func (b *ServerBlock) ToolID(tool string) string {
	canonical := b.CanonicalAddress()
	digest := sha256.Sum256([]byte("r42/mcp-tool-id/v1\x00" + canonical + "\x00" + tool))
	digest[6] = (digest[6] & 0x0f) | 0x80
	digest[8] = (digest[8] & 0x3f) | 0x80
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x", digest[0:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16])
	return "mcp_tool_" + idPart(b.Name()) + "__" + idPart(tool) + "_" + uuid
}

func (b *ServerBlock) ResourceID(uri string) string {
	canonical := b.CanonicalAddress()
	digest := sha256.Sum256([]byte("r42/mcp-resource-id/v1\x00" + canonical + "\x00" + uri))
	digest[6] = (digest[6] & 0x0f) | 0x80
	digest[8] = (digest[8] & 0x3f) | 0x80
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x", digest[0:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16])
	return "mcp_resource_" + idPart(b.Name()) + "__" + idPart(uri) + "_" + uuid
}

func (b *ServerBlock) toConfig() (mcp.Config, error) {
	if len(b.HTTPBlocks)+len(b.StdioBlocks) != 1 {
		return mcp.Config{}, errors.New("mcp server must have exactly one http or stdio block")
	}
	timeout := mcp.DefaultTimeout
	if b.Timeout != nil {
		parsed, err := time.ParseDuration(*b.Timeout)
		if err != nil {
			return mcp.Config{}, fmt.Errorf("mcp server timeout: %w", err)
		}
		timeout = parsed
	}
	result := mcp.Config{Name: b.Name(), Tools: slices.Clone(b.Tools), Resources: slices.Clone(b.Resources), Timeout: timeout}
	if len(b.HTTPBlocks) == 1 {
		httpBlock := b.HTTPBlocks[0]
		headers, err := stringMap(httpBlock.Headers, "mcp http headers")
		if err != nil {
			return mcp.Config{}, err
		}
		result.Transport = mcp.TransportHTTP
		result.HTTP = &mcp.HTTPConfig{
			URL: httpBlock.URL, Headers: headers, BearerToken: clonePointer(httpBlock.BearerToken),
			BearerTokenRef: clonePointer(httpBlock.BearerTokenRef),
		}
		return result, nil
	}
	stdioBlock := b.StdioBlocks[0]
	environment, err := stringMap(stdioBlock.Env, "mcp stdio env")
	if err != nil {
		return mcp.Config{}, err
	}
	environmentReferences, err := stringMap(stdioBlock.EnvRefs, "mcp stdio env_refs")
	if err != nil {
		return mcp.Config{}, err
	}
	result.Transport = mcp.TransportStdio
	result.Stdio = &mcp.StdioConfig{
		Command: pointerValue(stdioBlock.Command), Args: slices.Clone(stdioBlock.Args), Env: environment,
		EnvRefs: environmentReferences, WorkingDirectory: stdioBlock.WorkingDirectory,
	}
	return result, nil
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringMap(value cty.Value, field string) (map[string]string, error) {
	if value == cty.NilVal || value.Type().Equals(cty.NilType) || value.IsNull() {
		return map[string]string{}, nil
	}
	unmarked, _ := value.UnmarkDeep()
	converted, err := convert.Convert(unmarked, cty.Map(cty.String))
	if err != nil {
		return nil, fmt.Errorf("%s must be a map of string", field)
	}
	result := make(map[string]string, converted.LengthInt())
	iterator := converted.ElementIterator()
	for iterator.Next() {
		key, item := iterator.Element()
		result[key.AsString()] = item.AsString()
	}
	return result, nil
}

func stringMapValue(values map[string]cty.Value) cty.Value {
	if len(values) == 0 {
		return cty.MapValEmpty(cty.String)
	}
	return cty.MapVal(values)
}

func idPart(value string) string {
	var result strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9', character == '_', character == '-':
			result.WriteRune(character)
		default:
			result.WriteByte('_')
		}
	}
	if result.Len() == 0 {
		return "tool"
	}
	return result.String()
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
