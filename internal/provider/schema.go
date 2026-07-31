package provider

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Azure/golden"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

type ModelProviderBlock struct {
	*golden.BaseBlock
	ProviderType         Type         `hcl:"type"`
	Endpoint             string       `hcl:"endpoint"`
	WireAPI              *WireAPI     `hcl:"wire_api,optional"`
	Transport            *Transport   `hcl:"transport,optional"`
	Headers              cty.Value    `hcl:"headers,optional"`
	APIKey               *string      `hcl:"api_key,optional"`
	APIKeyRef            *string      `hcl:"api_key_ref,optional"`
	BearerToken          *string      `hcl:"bearer_token,optional"`
	BearerTokenRef       *string      `hcl:"bearer_token_ref,optional"`
	RetryBlocks          []RetryBlock `hcl:"retry,block"`
	APIKeyAttribute      cty.Value    `attribute:"api_key"`
	BearerTokenAttribute cty.Value    `attribute:"bearer_token"`

	planned Config
}

var _ golden.SingleValueBlock = (*ModelProviderBlock)(nil)

func (*ModelProviderBlock) Type() string { return "" }

func (*ModelProviderBlock) BlockType() string { return "model_provider" }

func (*ModelProviderBlock) AddressLength() int { return 2 }

func (*ModelProviderBlock) CanExecutePrePlan() bool { return false }

func (b *ModelProviderBlock) Value() cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"address": cty.StringVal(b.Address()),
		"kind":    cty.StringVal("provider"),
		"retry":   retryBlockValues(b.RetryBlocks),
	})
}

func (b *ModelProviderBlock) ExecuteDuringPlan() error {
	return debuglog.PlanBlock(b.Context(), b.Address(), b.BlockType(), func() error {
		if err := b.validateStringAttributes(); err != nil {
			return err
		}
		config, err := b.toConfig()
		if err != nil {
			return err
		}
		if err = config.Validate(); err != nil {
			return err
		}
		b.APIKeyAttribute = sensitiveString(b.APIKey)
		b.BearerTokenAttribute = sensitiveString(b.BearerToken)
		b.planned = config
		return nil
	})
}

func (b *ModelProviderBlock) validateStringAttributes() error {
	if b.BaseBlock == nil {
		return nil
	}
	for _, name := range []string{
		"type", "endpoint", "wire_api", "transport", "api_key", "api_key_ref", "bearer_token", "bearer_token_ref",
	} {
		attribute, ok := b.HclBlock().Body.Attributes[name]
		if !ok {
			continue
		}
		value, diagnostics := attribute.Expr.Value(b.EvalContext())
		if diagnostics.HasErrors() {
			// note: untested because Golden evaluates this expression during native decoding first.
			return fmt.Errorf("evaluate %s: %w", name, diagnostics)
		}
		unmarked, _ := value.UnmarkDeep()
		if !unmarked.IsWhollyKnown() {
			// note: untested because Golden rejects unknown values while decoding native strings first.
			return fmt.Errorf("%s must be known during plan", name)
		}
		if !unmarked.Type().Equals(cty.String) {
			return fmt.Errorf("%s must be a string", name)
		}
	}
	return nil
}

func (b *ModelProviderBlock) ProviderConfig() Config {
	return b.planned
}

func (b *ModelProviderBlock) APIKeyValue() cty.Value {
	if b.APIKeyAttribute.Type().Equals(cty.NilType) {
		return sensitiveString(b.APIKey)
	}
	return b.APIKeyAttribute
}

func (b *ModelProviderBlock) BearerTokenValue() cty.Value {
	if b.BearerTokenAttribute.Type().Equals(cty.NilType) {
		return sensitiveString(b.BearerToken)
	}
	return b.BearerTokenAttribute
}

type RetryBlock struct {
	LifecycleRetries   *int     `hcl:"lifecycle_retries,optional"`
	ModelCallRetries   *int     `hcl:"model_call_retries,optional"`
	IntervalSeconds    *int     `hcl:"interval_seconds,optional"`
	MaxIntervalSeconds *int     `hcl:"max_interval_seconds,optional"`
	ErrorMessageRegex  []string `hcl:"error_message_regex,optional"`
}

var retryBlockType = cty.Object(map[string]cty.Type{
	"lifecycle_retries":    cty.Number,
	"model_call_retries":   cty.Number,
	"interval_seconds":     cty.Number,
	"max_interval_seconds": cty.Number,
	"error_message_regex":  cty.List(cty.String),
})

func retryBlockValues(blocks []RetryBlock) cty.Value {
	if len(blocks) == 0 {
		return cty.ListValEmpty(retryBlockType)
	}
	values := make([]cty.Value, len(blocks))
	for index, block := range blocks {
		values[index] = cty.ObjectVal(map[string]cty.Value{
			"lifecycle_retries":    optionalIntValue(block.LifecycleRetries),
			"model_call_retries":   optionalIntValue(block.ModelCallRetries),
			"interval_seconds":     optionalIntValue(block.IntervalSeconds),
			"max_interval_seconds": optionalIntValue(block.MaxIntervalSeconds),
			"error_message_regex":  stringListValue(block.ErrorMessageRegex),
		})
	}
	return cty.ListVal(values)
}

func optionalIntValue(value *int) cty.Value {
	if value == nil {
		return cty.NullVal(cty.Number)
	}
	return cty.NumberIntVal(int64(*value))
}

func stringListValue(values []string) cty.Value {
	if len(values) == 0 {
		return cty.ListValEmpty(cty.String)
	}
	result := make([]cty.Value, len(values))
	for index, value := range values {
		result[index] = cty.StringVal(value)
	}
	return cty.ListVal(result)
}

func (b *ModelProviderBlock) toConfig() (Config, error) {
	if len(b.RetryBlocks) > 1 {
		return Config{}, errors.New("model provider must have at most one retry block")
	}

	headers, err := normalizeHeaders(b.Headers)
	if err != nil {
		return Config{}, err
	}
	b.Headers = headers

	config := Config{
		Type:           b.ProviderType,
		Endpoint:       b.Endpoint,
		WireAPI:        clonePointer(b.WireAPI),
		Transport:      clonePointer(b.Transport),
		Headers:        headers,
		APIKey:         clonePointer(b.APIKey),
		APIKeyRef:      clonePointer(b.APIKeyRef),
		BearerToken:    clonePointer(b.BearerToken),
		BearerTokenRef: clonePointer(b.BearerTokenRef),
	}
	if len(b.RetryBlocks) == 1 {
		config.Retry, err = b.RetryBlocks[0].override()
		if err != nil {
			return Config{}, err
		}
	}
	return config, nil
}

func normalizeHeaders(value cty.Value) (cty.Value, error) {
	if value.Type().Equals(cty.NilType) {
		return value, nil
	}
	unmarked, marks := value.UnmarkDeepWithPaths()
	converted, err := convert.Convert(unmarked, cty.Map(cty.String))
	if err != nil {
		return cty.NilVal, fmt.Errorf("headers must be map of string: %w", err)
	}
	return converted.MarkWithPaths(objectMarksToMapPaths(marks)), nil
}

func objectMarksToMapPaths(marks []cty.PathValueMarks) []cty.PathValueMarks {
	result := make([]cty.PathValueMarks, len(marks))
	for index, pathMarks := range marks {
		path := make(cty.Path, len(pathMarks.Path))
		for stepIndex, step := range pathMarks.Path {
			if attribute, ok := step.(cty.GetAttrStep); ok {
				step = cty.IndexStep{Key: cty.StringVal(attribute.Name)}
			}
			path[stepIndex] = step
		}
		result[index] = cty.PathValueMarks{Path: path, Marks: pathMarks.Marks}
	}
	return result
}

func (b RetryBlock) override() (RetryOverride, error) {
	interval, err := optionalDuration(b.IntervalSeconds, "interval_seconds")
	if err != nil {
		return RetryOverride{}, err
	}
	maxInterval, err := optionalDuration(b.MaxIntervalSeconds, "max_interval_seconds")
	if err != nil {
		return RetryOverride{}, err
	}
	return RetryOverride{
		LifecycleRetries:  clonePointer(b.LifecycleRetries),
		ModelCallRetries:  clonePointer(b.ModelCallRetries),
		Interval:          interval,
		MaxInterval:       maxInterval,
		ErrorMessageRegex: append([]string{}, b.ErrorMessageRegex...),
	}, nil
}

func sensitiveString(value *string) cty.Value {
	if value == nil {
		return cty.NilVal
	}
	return spec.MarkSensitive(cty.StringVal(*value))
}

func optionalDuration(seconds *int, name string) (*time.Duration, error) {
	if seconds == nil {
		return nil, nil
	}
	if *seconds < math.MinInt64/int(time.Second) || *seconds > math.MaxInt64/int(time.Second) {
		return nil, fmt.Errorf("%s is too large", name)
	}
	result := time.Duration(*seconds) * time.Second
	return &result, nil
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
