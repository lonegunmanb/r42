package provider

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Azure/golden"
	"github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/gocty"
)

type ModelProviderBlock struct {
	*golden.BaseBlock
	ProviderType   string       `hcl:"type"`
	Endpoint       string       `hcl:"endpoint"`
	WireAPIValue   cty.Value    `hcl:"wire_api,optional"`
	TransportValue cty.Value    `hcl:"transport,optional"`
	Headers        cty.Value    `hcl:"headers,optional"`
	APIKey         cty.Value    `hcl:"api_key,optional"`
	APIKeyRef      cty.Value    `hcl:"api_key_ref,optional"`
	BearerToken    cty.Value    `hcl:"bearer_token,optional"`
	BearerTokenRef cty.Value    `hcl:"bearer_token_ref,optional"`
	RetryBlocks    []RetryBlock `hcl:"retry,block"`

	planned Config
}

func (*ModelProviderBlock) Type() string { return "" }

func (*ModelProviderBlock) BlockType() string { return "model_provider" }

func (*ModelProviderBlock) AddressLength() int { return 2 }

func (*ModelProviderBlock) CanExecutePrePlan() bool { return false }

func (b *ModelProviderBlock) ExecuteDuringPlan() error {
	config, err := b.toConfig()
	if err != nil {
		return err
	}
	if err = config.Validate(); err != nil {
		return err
	}
	b.planned = config
	return nil
}

func (b *ModelProviderBlock) ProviderConfig() Config {
	return b.planned
}

type RetryBlock struct {
	LifecycleRetries   cty.Value `hcl:"lifecycle_retries,optional"`
	ModelCallRetries   cty.Value `hcl:"model_call_retries,optional"`
	IntervalSeconds    cty.Value `hcl:"interval_seconds,optional"`
	MaxIntervalSeconds cty.Value `hcl:"max_interval_seconds,optional"`
	ErrorMessageRegex  []string  `hcl:"error_message_regex,optional"`
}

func (b *ModelProviderBlock) toConfig() (Config, error) {
	if len(b.RetryBlocks) > 1 {
		return Config{}, errors.New("model provider must have at most one retry block")
	}

	wireAPI, err := optionalString(b.WireAPIValue, "wire_api")
	if err != nil {
		return Config{}, err
	}
	transport, err := optionalString(b.TransportValue, "transport")
	if err != nil {
		return Config{}, err
	}
	apiKey, err := optionalSecret(&b.APIKey, "api_key")
	if err != nil {
		return Config{}, err
	}
	apiKeyRef, err := optionalString(b.APIKeyRef, "api_key_ref")
	if err != nil {
		return Config{}, err
	}
	bearerToken, err := optionalSecret(&b.BearerToken, "bearer_token")
	if err != nil {
		return Config{}, err
	}
	bearerTokenRef, err := optionalString(b.BearerTokenRef, "bearer_token_ref")
	if err != nil {
		return Config{}, err
	}
	headers, err := normalizeHeaders(b.Headers)
	if err != nil {
		return Config{}, err
	}
	b.Headers = headers

	config := Config{
		Type:           Type(b.ProviderType),
		Endpoint:       b.Endpoint,
		Headers:        headers,
		APIKey:         apiKey,
		APIKeyRef:      apiKeyRef,
		BearerToken:    bearerToken,
		BearerTokenRef: bearerTokenRef,
	}
	if wireAPI != nil {
		value := WireAPI(*wireAPI)
		config.WireAPI = &value
	}
	if transport != nil {
		value := Transport(*transport)
		config.Transport = &value
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
	lifecycle, err := optionalInt(b.LifecycleRetries, "lifecycle_retries")
	if err != nil {
		return RetryOverride{}, err
	}
	modelCalls, err := optionalInt(b.ModelCallRetries, "model_call_retries")
	if err != nil {
		return RetryOverride{}, err
	}
	interval, err := optionalDuration(b.IntervalSeconds, "interval_seconds")
	if err != nil {
		return RetryOverride{}, err
	}
	maxInterval, err := optionalDuration(b.MaxIntervalSeconds, "max_interval_seconds")
	if err != nil {
		return RetryOverride{}, err
	}
	return RetryOverride{
		LifecycleRetries:  lifecycle,
		ModelCallRetries:  modelCalls,
		Interval:          interval,
		MaxInterval:       maxInterval,
		ErrorMessageRegex: append([]string{}, b.ErrorMessageRegex...),
	}, nil
}

func optionalSecret(value *cty.Value, name string) (*string, error) {
	result, err := optionalString(*value, name)
	if err != nil || result == nil {
		return result, err
	}
	*value = spec.MarkSensitive(*value)
	return result, nil
}

func optionalString(value cty.Value, name string) (*string, error) {
	if value.Type().Equals(cty.NilType) || value.IsNull() {
		return nil, nil
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.IsWhollyKnown() {
		return nil, fmt.Errorf("%s must be known during plan", name)
	}
	if !unmarked.Type().Equals(cty.String) {
		return nil, fmt.Errorf("%s must be a string", name)
	}
	result := unmarked.AsString()
	return &result, nil
}

func optionalInt(value cty.Value, name string) (*int, error) {
	if value.Type().Equals(cty.NilType) || value.IsNull() {
		return nil, nil
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.IsWhollyKnown() || !unmarked.Type().Equals(cty.Number) {
		return nil, fmt.Errorf("%s must be a known integer", name)
	}
	var result int
	if err := gocty.FromCtyValue(unmarked, &result); err != nil {
		return nil, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return &result, nil
}

func optionalDuration(value cty.Value, name string) (*time.Duration, error) {
	seconds, err := optionalInt(value, name)
	if err != nil || seconds == nil {
		return nil, err
	}
	if *seconds > math.MaxInt64/int(time.Second) {
		return nil, fmt.Errorf("%s is too large", name)
	}
	result := time.Duration(*seconds) * time.Second
	return &result, nil
}
