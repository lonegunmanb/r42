package provider_test

import (
	"testing"
	"time"

	"github.com/lonegunmanb/r42/internal/provider"
	"github.com/stretchr/testify/assert"
)

func TestConfigValidateCompatibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		config        provider.Config
		expectedError string
	}{
		{
			name:   "openai defaults",
			config: provider.Config{Type: provider.TypeOpenAI, Endpoint: "https://openai.example.test"},
		},
		{
			name: "openai responses websocket",
			config: provider.Config{
				Type:      provider.TypeOpenAI,
				Endpoint:  "https://openai.example.test",
				WireAPI:   pointer(provider.WireAPIResponses),
				Transport: pointer(provider.TransportWebSockets),
			},
		},
		{
			name: "azure responses websocket",
			config: provider.Config{
				Type:      provider.TypeAzure,
				Endpoint:  "https://azure.example.test",
				WireAPI:   pointer(provider.WireAPIResponses),
				Transport: pointer(provider.TransportWebSockets),
			},
		},
		{
			name:   "anthropic omits openai wire fields",
			config: provider.Config{Type: provider.TypeAnthropic, Endpoint: "https://anthropic.example.test"},
		},
		{
			name:          "missing type",
			config:        provider.Config{Endpoint: "https://models.example.test"},
			expectedError: "provider type must be one of openai, azure, or anthropic",
		},
		{
			name:          "invalid type",
			config:        provider.Config{Type: "ollama", Endpoint: "https://models.example.test"},
			expectedError: "provider type must be one of openai, azure, or anthropic",
		},
		{
			name:          "missing endpoint",
			config:        provider.Config{Type: provider.TypeOpenAI},
			expectedError: "provider endpoint is required",
		},
		{
			name: "invalid wire api",
			config: provider.Config{
				Type:     provider.TypeOpenAI,
				Endpoint: "https://models.example.test",
				WireAPI:  pointer(provider.WireAPI("messages")),
			},
			expectedError: "wire api must be completions or responses",
		},
		{
			name: "invalid transport",
			config: provider.Config{
				Type:      provider.TypeOpenAI,
				Endpoint:  "https://models.example.test",
				Transport: pointer(provider.Transport("grpc")),
			},
			expectedError: "transport must be http or websockets",
		},
		{
			name: "websocket completions",
			config: provider.Config{
				Type:      provider.TypeAzure,
				Endpoint:  "https://models.example.test",
				WireAPI:   pointer(provider.WireAPICompletions),
				Transport: pointer(provider.TransportWebSockets),
			},
			expectedError: "websockets transport requires responses wire api",
		},
		{
			name: "anthropic wire api",
			config: provider.Config{
				Type:     provider.TypeAnthropic,
				Endpoint: "https://models.example.test",
				WireAPI:  pointer(provider.WireAPICompletions),
			},
			expectedError: "anthropic provider does not use wire api or transport",
		},
		{
			name: "anthropic transport",
			config: provider.Config{
				Type:      provider.TypeAnthropic,
				Endpoint:  "https://models.example.test",
				Transport: pointer(provider.TransportHTTP),
			},
			expectedError: "anthropic provider does not use wire api or transport",
		},
		{
			name: "invalid retry override",
			config: provider.Config{
				Type:     provider.TypeOpenAI,
				Endpoint: "https://models.example.test",
				Retry: provider.RetryOverride{
					Interval: pointer(-time.Second),
				},
			},
			expectedError: "retry interval must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.Validate()
			if tt.expectedError == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, tt.expectedError)
		})
	}
}

func pointer[T any](value T) *T {
	return &value
}
