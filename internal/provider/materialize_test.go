package provider_test

import (
	"testing"

	"github.com/lonegunmanb/r42/internal/provider"
	"github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestConfigValidateAuthentication(t *testing.T) {
	t.Parallel()

	secret := "secret"
	reference := "MODEL_API_KEY"
	tests := []struct {
		name          string
		configure     func(*provider.Config)
		expectedError string
	}{
		{name: "none", configure: func(*provider.Config) {}},
		{name: "api key", configure: func(config *provider.Config) { config.APIKey = &secret }},
		{name: "api key ref", configure: func(config *provider.Config) { config.APIKeyRef = &reference }},
		{name: "bearer token", configure: func(config *provider.Config) { config.BearerToken = &secret }},
		{name: "bearer token ref", configure: func(config *provider.Config) { config.BearerTokenRef = &reference }},
		{
			name: "multiple",
			configure: func(config *provider.Config) {
				config.APIKey = &secret
				config.BearerTokenRef = &reference
			},
			expectedError: "at most one authentication field may be set",
		},
		{
			name:          "blank configured value",
			configure:     func(config *provider.Config) { config.APIKeyRef = pointer(" \t") },
			expectedError: "configured authentication field must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := provider.Config{Type: provider.TypeOpenAI, Endpoint: "https://models.example.test"}
			tt.configure(&config)
			err := config.Validate()
			if tt.expectedError == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestConfigMaterializeDefersEnvironmentLookupUntilApply(t *testing.T) {
	t.Parallel()

	reference := "MODEL_API_KEY"
	config := provider.Config{
		Type:      provider.TypeOpenAI,
		Endpoint:  "https://models.example.test",
		APIKeyRef: &reference,
	}
	lookupCalls := 0
	lookup := func(name string) (string, bool) {
		lookupCalls++
		assert.Equal(t, reference, name)
		return "resolved-secret", true
	}

	require.NoError(t, config.Validate())
	assert.Zero(t, lookupCalls)
	materialized, err := config.Materialize(lookup)
	require.NoError(t, err)
	assert.Equal(t, 1, lookupCalls)
	assert.Equal(t, provider.AuthAPIKey, materialized.Auth.Kind)
	assert.Equal(t, "resolved-secret", materialized.Auth.Value)
	assert.True(t, materialized.Auth.Sensitive())
	assert.Equal(t, provider.WireAPICompletions, *materialized.WireAPI)
	assert.Equal(t, provider.TransportHTTP, *materialized.Transport)
}

func TestConfigMaterializeMissingEnvironmentCredential(t *testing.T) {
	t.Parallel()

	reference := "MODEL_API_KEY"
	config := provider.Config{
		Type:      provider.TypeOpenAI,
		Endpoint:  "https://models.example.test",
		APIKeyRef: &reference,
	}

	_, err := config.Materialize(func(string) (string, bool) { return "", false })
	assert.EqualError(t, err, `environment variable "MODEL_API_KEY" is not set or empty`)
}

func TestConfigMaterializeAuthenticationKinds(t *testing.T) {
	t.Parallel()

	secret := "secret"
	reference := "MODEL_SECRET"
	tests := []struct {
		name         string
		providerType provider.Type
		configure    func(*provider.Config)
		lookup       provider.EnvLookup
		expectedAuth provider.Auth
	}{
		{name: "none", providerType: provider.TypeAnthropic, configure: func(*provider.Config) {}},
		{
			name:         "literal api key",
			providerType: provider.TypeOpenAI,
			configure:    func(config *provider.Config) { config.APIKey = &secret },
			expectedAuth: provider.Auth{Kind: provider.AuthAPIKey, Value: secret},
		},
		{
			name:         "literal bearer token",
			providerType: provider.TypeOpenAI,
			configure:    func(config *provider.Config) { config.BearerToken = &secret },
			expectedAuth: provider.Auth{Kind: provider.AuthBearerToken, Value: secret},
		},
		{
			name:         "referenced bearer token",
			providerType: provider.TypeOpenAI,
			configure:    func(config *provider.Config) { config.BearerTokenRef = &reference },
			lookup:       func(string) (string, bool) { return secret, true },
			expectedAuth: provider.Auth{Kind: provider.AuthBearerToken, Value: secret},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := provider.Config{Type: tt.providerType, Endpoint: "https://models.example.test"}
			tt.configure(&config)
			materialized, err := config.Materialize(tt.lookup)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedAuth, materialized.Auth)
			assert.Equal(t, tt.expectedAuth.Kind != provider.AuthNone, materialized.Auth.Sensitive())
			if tt.providerType == provider.TypeAnthropic {
				assert.Nil(t, materialized.WireAPI)
				assert.Nil(t, materialized.Transport)
			}
		})
	}
}

func TestConfigMaterializeRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := (provider.Config{}).Materialize(nil)
	assert.EqualError(t, err, "provider type must be one of openai, azure, or anthropic")
}

func TestConfigMaterializeRejectsEmptyReferencedCredential(t *testing.T) {
	t.Parallel()

	reference := "MODEL_API_KEY"
	config := provider.Config{
		Type:      provider.TypeOpenAI,
		Endpoint:  "https://models.example.test",
		APIKeyRef: &reference,
	}

	_, err := config.Materialize(func(string) (string, bool) { return "", true })
	assert.EqualError(t, err, `environment variable "MODEL_API_KEY" is not set or empty`)
}

func TestConfigMaterializeRejectsNilCredentialLookup(t *testing.T) {
	t.Parallel()

	reference := "MODEL_API_KEY"
	config := provider.Config{
		Type:      provider.TypeOpenAI,
		Endpoint:  "https://models.example.test",
		APIKeyRef: &reference,
	}

	_, err := config.Materialize(nil)
	assert.EqualError(t, err, `environment variable "MODEL_API_KEY" is not set or empty`)
}

func TestHeadersTypingSensitivityAndMaterialization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		headers           cty.Value
		expectedSensitive bool
		expected          map[string]string
		expectedError     string
	}{
		{name: "unset", headers: cty.NilVal, expected: map[string]string{}},
		{name: "null", headers: cty.NullVal(cty.Map(cty.String)), expected: map[string]string{}},
		{
			name: "plain",
			headers: cty.MapVal(map[string]cty.Value{
				"X-Project": cty.StringVal("r42"),
			}),
			expected: map[string]string{"X-Project": "r42"},
		},
		{
			name: "deep sensitive mark",
			headers: cty.MapVal(map[string]cty.Value{
				"Authorization": spec.MarkSensitive(cty.StringVal("secret")),
			}),
			expectedSensitive: true,
			expected:          map[string]string{"Authorization": "secret"},
		},
		{name: "wrong type", headers: cty.StringVal("x"), expectedError: "headers must be map of string"},
		{
			name:          "unknown",
			headers:       cty.UnknownVal(cty.Map(cty.String)),
			expectedError: "headers must be wholly known during plan",
		},
		{
			name: "null element",
			headers: cty.MapVal(map[string]cty.Value{
				"Authorization": cty.NullVal(cty.String),
			}),
			expectedError: "header values must not be null",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := provider.ValidateHeaders(tt.headers)
			if tt.expectedError != "" {
				assert.EqualError(t, err, tt.expectedError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedSensitive, provider.HeadersSensitive(tt.headers))
			actual, err := provider.MaterializeHeaders(tt.headers)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestMaterializeHeadersRejectsInvalidValue(t *testing.T) {
	t.Parallel()

	_, err := provider.MaterializeHeaders(cty.StringVal("invalid"))
	assert.EqualError(t, err, "headers must be map of string")
}
