package provider_test

import (
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/Azure/golden"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
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
	assert.True(t, spec.IsSensitive(block.APIKey))
	planned := block.ProviderConfig()
	require.NotNil(t, planned.APIKey)
	assert.Equal(t, "secret", *planned.APIKey)
	assert.Nil(t, planned.WireAPI)
	assert.Nil(t, planned.Transport)
	assert.Empty(t, planned.Retry.ErrorMessageRegex)
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
			expectedError: "lifecycle_retries must be an integer",
		},
		{
			name: "retry count wrong type",
			body: `type = "openai"
endpoint = "https://models.example.test"
retry { model_call_retries = "five" }`,
			expectedError: "model_call_retries must be a known integer",
		},
		{
			name: "duration too large",
			body: `type = "openai"
endpoint = "https://models.example.test"
retry { interval_seconds = ` + fmtInt(math.MaxInt64/int64(time.Second)+1) + ` }`,
			expectedError: "interval_seconds is too large",
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

func TestModelProviderBlockRejectsUnknownPlanValues(t *testing.T) {
	t.Parallel()

	block := provider.ModelProviderBlock{
		ProviderType: "openai",
		Endpoint:     "https://models.example.test",
		WireAPIValue: cty.UnknownVal(cty.String),
	}
	err := block.ExecuteDuringPlan()
	assert.EqualError(t, err, "wire_api must be known during plan")
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

func fmtInt(value int64) string {
	return fmt.Sprintf("%d", value)
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
