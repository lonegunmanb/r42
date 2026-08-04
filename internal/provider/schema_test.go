package provider_test

import (
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/provider"
	"github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

type providerTestConfig struct {
	*golden.BaseConfig
}

var registerProviderBlock sync.Once

//nolint:paralleltest // Golden's block registry is process-global.
func TestModelProviderBlockPlansDesignShape(t *testing.T) {
	registerProviderBlock.Do(func() { golden.RegisterBlock(new(provider.ModelProviderBlock)) })
	config := parseProviderConfig(t, `
model_provider "primary" {
  type      = "azure"
  endpoint  = "https://example.openai.azure.com"
  wire_api  = "responses"
  transport = "http"
  api_key_ref = "AZURE_OPENAI_API_KEY"
  headers = {
    "x-project" = "r42"
  }
  retry {
    lifecycle_retries    = 7
    model_call_retries   = 3
    interval_seconds     = 2
    max_interval_seconds = 30
    error_message_regex  = ["temporarily unavailable"]
  }
}
`)

	require.NoError(t, config.RunPlan())
	blocks := golden.Blocks[*provider.ModelProviderBlock](config)
	require.Len(t, blocks, 1)
	block := blocks[0]
	assert.Equal(t, "primary", block.Name())
	assert.Equal(t, "model_provider.primary", block.Address())

	planned := block.ProviderConfig()
	assertNativeProviderFields(t, block.WireAPI, block.Transport, block.APIKeyRef)
	assert.Equal(t, provider.TypeAzure, planned.Type)
	assert.Equal(t, "https://example.openai.azure.com", planned.Endpoint)
	assert.Equal(t, provider.WireAPIResponses, *planned.WireAPI)
	assert.Equal(t, provider.TransportHTTP, *planned.Transport)
	assert.Equal(t, "AZURE_OPENAI_API_KEY", *planned.APIKeyRef)
	retry, err := provider.MergeRetry(provider.DefaultRetryPolicy(), planned.Retry)
	require.NoError(t, err)
	assert.Equal(t, 7, retry.LifecycleRetries)
	assert.Equal(t, 3, retry.ModelCallRetries)
	assert.Equal(t, 2*time.Second, retry.Interval)
	assert.Equal(t, 30*time.Second, retry.MaxInterval)
	assert.Equal(t, []string{"temporarily unavailable"}, retry.ErrorMessageRegex)
	assert.True(t, block.APIKeyValue().Type().Equals(cty.NilType))
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestModelProviderBlockExposesRetryAsNestedBlockList(t *testing.T) {
	registerProviderBlock.Do(func() { golden.RegisterBlock(new(provider.ModelProviderBlock)) })
	config := parseProviderConfig(t, `
model_provider "primary" {
  type     = "openai"
  endpoint = "https://models.example.test"

  retry {
    lifecycle_retries = 7
  }
}
`)

	require.NoError(t, config.RunPlan())
	block := golden.Blocks[*provider.ModelProviderBlock](config)[0]
	value := block.Value()
	require.True(t, value.Type().HasAttribute("retry"))
	retries := value.GetAttr("retry")
	assert.True(t, retries.Type().IsListType())
	require.Equal(t, 1, retries.LengthInt())
	retry := retries.Index(cty.NumberIntVal(0))
	assert.True(t, retry.Type().IsObjectType())
	assert.Equal(t, "7", retry.GetAttr("lifecycle_retries").AsBigFloat().Text('f', 0))
	assert.True(t, retry.GetAttr("model_call_retries").IsNull())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestModelProviderBlockDefaultsAndMarksLiteralCredential(t *testing.T) {
	registerProviderBlock.Do(func() { golden.RegisterBlock(new(provider.ModelProviderBlock)) })
	config := parseProviderConfig(t, `
model_provider "minimal" {
  type     = "openai"
  endpoint = "https://models.example.test"
  api_key  = "secret"
}
`)

	require.NoError(t, config.RunPlan())
	blocks := golden.Blocks[*provider.ModelProviderBlock](config)
	require.Len(t, blocks, 1)
	block := blocks[0]
	assert.False(t, block.CanExecutePrePlan())
	require.NotNil(t, block.APIKey)
	assert.Equal(t, "secret", *block.APIKey)
	assert.True(t, spec.IsSensitive(block.APIKeyValue()))
	assert.True(t, spec.IsSensitive(golden.Value(block)["api_key"]))
	planned := block.ProviderConfig()
	require.NotNil(t, planned.APIKey)
	assert.Equal(t, "secret", *planned.APIKey)
	assert.Nil(t, planned.WireAPI)
	assert.Nil(t, planned.Transport)
	assert.Empty(t, planned.Retry.ErrorMessageRegex)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestModelProviderBlockPreservesOptionalRetryZeroValues(t *testing.T) {
	registerProviderBlock.Do(func() { golden.RegisterBlock(new(provider.ModelProviderBlock)) })
	config := parseProviderConfig(t, `
model_provider "zero" {
  type     = "openai"
  endpoint = "https://models.example.test"
  retry {
    lifecycle_retries = 0
    interval_seconds  = 0
  }
}
`)

	require.NoError(t, config.RunPlan())
	blocks := golden.Blocks[*provider.ModelProviderBlock](config)
	require.Len(t, blocks, 1)
	retry := blocks[0].RetryBlocks[0]
	require.NotNil(t, retry.LifecycleRetries)
	require.NotNil(t, retry.IntervalSeconds)
	assert.Zero(t, *retry.LifecycleRetries)
	assert.Zero(t, *retry.IntervalSeconds)
	assert.Nil(t, retry.ModelCallRetries)
	assert.Nil(t, retry.MaxIntervalSeconds)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestModelProviderBlockKeepsPlannedConfigIndependentFromDecodedFields(t *testing.T) {
	registerProviderBlock.Do(func() { golden.RegisterBlock(new(provider.ModelProviderBlock)) })
	config := parseProviderConfig(t, `
model_provider "snapshot" {
  type      = "openai"
  endpoint  = "https://models.example.test"
  wire_api  = "responses"
  transport = "http"
  api_key   = "original"
  retry {
    lifecycle_retries  = 7
    model_call_retries = 3
  }
}
`)

	require.NoError(t, config.RunPlan())
	blocks := golden.Blocks[*provider.ModelProviderBlock](config)
	require.Len(t, blocks, 1)
	block := blocks[0]

	*block.WireAPI = provider.WireAPI("changed")
	*block.Transport = provider.Transport("changed")
	*block.APIKey = "changed"
	*block.RetryBlocks[0].LifecycleRetries = 99
	*block.RetryBlocks[0].ModelCallRetries = 98

	planned := block.ProviderConfig()
	assert.Equal(t, provider.WireAPIResponses, *planned.WireAPI)
	assert.Equal(t, provider.TransportHTTP, *planned.Transport)
	assert.Equal(t, "original", *planned.APIKey)
	assert.Equal(t, 7, *planned.Retry.LifecycleRetries)
	assert.Equal(t, 3, *planned.Retry.ModelCallRetries)
}

func TestModelProviderBlockKeepsPlannedAuthenticationIndependentFromDecodedFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*provider.ModelProviderBlock, *string)
		planned   func(provider.Config) *string
	}{
		{
			name: "api key",
			configure: func(block *provider.ModelProviderBlock, value *string) {
				block.APIKey = value
			},
			planned: func(config provider.Config) *string { return config.APIKey },
		},
		{
			name: "api key environment reference",
			configure: func(block *provider.ModelProviderBlock, value *string) {
				block.APIKeyRef = value
			},
			planned: func(config provider.Config) *string { return config.APIKeyRef },
		},
		{
			name: "bearer token",
			configure: func(block *provider.ModelProviderBlock, value *string) {
				block.BearerToken = value
			},
			planned: func(config provider.Config) *string { return config.BearerToken },
		},
		{
			name: "bearer token environment reference",
			configure: func(block *provider.ModelProviderBlock, value *string) {
				block.BearerTokenRef = value
			},
			planned: func(config provider.Config) *string { return config.BearerTokenRef },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			credential := "original"
			block := provider.ModelProviderBlock{
				ProviderType: provider.TypeOpenAI,
				Endpoint:     "https://models.example.test",
			}
			tt.configure(&block, &credential)
			require.NoError(t, block.ExecuteDuringPlan())

			credential = "changed"
			planned := tt.planned(block.ProviderConfig())
			require.NotNil(t, planned)
			assert.Equal(t, "original", *planned)
		})
	}
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestModelProviderBlockRejectsInvalidSchemaValues(t *testing.T) {
	registerProviderBlock.Do(func() { golden.RegisterBlock(new(provider.ModelProviderBlock)) })
	tests := []struct {
		name          string
		body          string
		expectedError string
	}{
		{
			name: "invalid provider type",
			body: `type = "local"
endpoint = "https://models.example.test"`,
			expectedError: "provider type must be one of openai, azure, or anthropic",
		},
		{
			name: "two retry blocks",
			body: `type = "openai"
endpoint = "https://models.example.test"
retry {}
retry {}`,
			expectedError: "model provider must have at most one retry block",
		},
		{
			name: "headers not convertible to map string",
			body: `type = "openai"
endpoint = "https://models.example.test"
headers = ["x"]`,
			expectedError: "headers must be map of string",
		},
		{
			name: "wire api wrong type",
			body: `type = "openai"
endpoint = "https://models.example.test"
wire_api = 42`,
			expectedError: "wire_api must be a string",
		},
		{
			name: "literal credential wrong type",
			body: `type = "openai"
endpoint = "https://models.example.test"
api_key = 42`,
			expectedError: "api_key must be a string",
		},
		{
			name: "fractional retry count",
			body: `type = "openai"
endpoint = "https://models.example.test"
retry { lifecycle_retries = 1.5 }`,
			expectedError: "value must be a whole number",
		},
		{
			name: "retry count wrong type",
			body: `type = "openai"
endpoint = "https://models.example.test"
retry { model_call_retries = "five" }`,
			expectedError: "a number is required",
		},
		{
			name: "duration too large",
			body: `type = "openai"
endpoint = "https://models.example.test"
retry { interval_seconds = ` + fmtInt(math.MaxInt64/int64(time.Second)+1) + ` }`,
			expectedError: "interval_seconds is too large",
		},
		{
			name: "negative duration too large",
			body: `type = "openai"
endpoint = "https://models.example.test"
retry { max_interval_seconds = ` + fmtInt(math.MinInt64/int64(time.Second)-1) + ` }`,
			expectedError: "max_interval_seconds is too large",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := parseProviderConfig(t, "model_provider \"invalid\" {\n"+tt.body+"\n}\n")
			err := config.RunPlan()
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

func TestModelProviderBlockPreservesDeepHeaderSensitivity(t *testing.T) {
	t.Parallel()

	block := provider.ModelProviderBlock{
		ProviderType: "openai",
		Endpoint:     "https://models.example.test",
		Headers: cty.ObjectVal(map[string]cty.Value{
			"Authorization": spec.MarkSensitive(cty.StringVal("secret")),
			"X-Project":     cty.StringVal("r42"),
		}),
	}
	require.NoError(t, block.ExecuteDuringPlan())

	headers := block.ProviderConfig().Headers
	assert.True(t, headers.Type().Equals(cty.Map(cty.String)))
	assert.True(t, spec.IsSensitive(headers.Index(cty.StringVal("Authorization"))))
	assert.False(t, spec.IsSensitive(headers.Index(cty.StringVal("X-Project"))))
}

func TestModelProviderBlockMarksBearerTokenValueSensitive(t *testing.T) {
	t.Parallel()

	secret := "secret"
	block := provider.ModelProviderBlock{BearerToken: &secret}
	value := block.BearerTokenValue()
	assert.True(t, spec.IsSensitive(value))
	unmarked, _ := value.Unmark()
	assert.Equal(t, secret, unmarked.AsString())
}

func fmtInt(value int64) string {
	return fmt.Sprintf("%d", value)
}

func assertNativeProviderFields(t *testing.T, wireAPI *provider.WireAPI, transport *provider.Transport, apiKeyRef *string) {
	t.Helper()
	require.NotNil(t, wireAPI)
	require.NotNil(t, transport)
	require.NotNil(t, apiKeyRef)
}

func parseProviderConfig(t *testing.T, source string) *providerTestConfig {
	t.Helper()

	syntaxFile, diagnostics := hclsyntax.ParseConfig([]byte(source), "provider.r42", hcl.InitialPos)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	writeFile, diagnostics := hclwrite.ParseConfig([]byte(source), "provider.r42", hcl.InitialPos)
	require.False(t, diagnostics.HasErrors(), diagnostics.Error())
	body, ok := syntaxFile.Body.(*hclsyntax.Body)
	require.True(t, ok)

	config := &providerTestConfig{BaseConfig: golden.NewBasicConfig("", "r42", "r42", nil, nil, nil)}
	err := golden.InitConfig(config, golden.AsHclBlocks(body.Blocks, writeFile.Body().Blocks()))
	require.NoError(t, err)
	return config
}
