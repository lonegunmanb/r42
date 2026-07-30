package copilot

import (
	"context"
	"errors"
	"testing"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestFactoryOpenMaterializesProviderAndAssemblesSession(t *testing.T) {
	t.Parallel()

	apiKeyRef := "MODEL_API_KEY"
	wireAPI := provider.WireAPIResponses
	transport := provider.TransportWebSockets
	client := &fakeClient{}
	factory := newFactory(client, func(name string) (string, bool) {
		assert.Equal(t, apiKeyRef, name)
		return "secret-at-apply", true
	}, noDelay, fixedRandom)
	tool := sdk.Tool{Name: "go_tool_finish", Description: "finish"}

	session, err := factory.Open(t.Context(), SessionConfig{
		Provider: &provider.Config{
			Type:      provider.TypeOpenAI,
			Endpoint:  "https://models.example.test",
			WireAPI:   &wireAPI,
			Transport: &transport,
			Headers:   providerHeaders(map[string]string{"X-R42": "test"}),
			APIKeyRef: &apiKeyRef,
		},
		Retry:            retryPolicy(t, 2, 3),
		Model:            "gpt-5.6-sol",
		ReasoningEffort:  "max",
		SystemPrompt:     "r42 protocol\nauthor instructions",
		WorkingDirectory: "D:/run/research.market",
		Tools:            []sdk.Tool{tool},
		AvailableTools:   []string{"custom:go_tool_finish", "builtin:view"},
		ExcludedTools:    []string{"builtin:ask_user"},
		SkillDirectories: []string{"D:/skills"},
		Skills:           []string{"source-evaluation"},
		DisabledSkills:   []string{"unsafe-skill"},
	})
	require.NoError(t, err)
	require.NotNil(t, session)
	require.Len(t, client.configs, 1)

	config := client.configs[0]
	assert.Equal(t, "gpt-5.6-sol", config.Model)
	assert.Equal(t, "max", config.ReasoningEffort)
	assert.Equal(t, "D:/run/research.market", config.WorkingDirectory)
	require.NotNil(t, config.SystemMessage)
	assert.Equal(t, "append", config.SystemMessage.Mode)
	assert.Equal(t, "r42 protocol\nauthor instructions", config.SystemMessage.Content)
	assert.Equal(t, []sdk.Tool{tool}, config.Tools)
	assert.Equal(t, []string{"custom:go_tool_finish", "builtin:view"}, config.AvailableTools)
	assert.Equal(t, []string{"builtin:ask_user"}, config.ExcludedTools)
	assert.NotNil(t, config.OnPermissionRequest)
	assert.Equal(t, sdk.Bool(true), config.EnableSkills)
	assert.Equal(t, []string{"D:/skills"}, config.SkillDirectories)
	assert.Equal(t, []string{"unsafe-skill"}, config.DisabledSkills)
	assert.Equal(t, "r42_research", config.Agent)
	require.Len(t, config.CustomAgents, 1)
	assert.Equal(t, sdk.CustomAgentConfig{
		Name:            "r42_research",
		Prompt:          "r42 protocol\nauthor instructions",
		Skills:          []string{"source-evaluation"},
		Model:           "gpt-5.6-sol",
		ReasoningEffort: "max",
	}, config.CustomAgents[0])
	require.NotNil(t, config.Provider)
	assert.Equal(t, "openai", config.Provider.Type)
	assert.Equal(t, "https://models.example.test", config.Provider.BaseURL)
	assert.Equal(t, "responses", config.Provider.WireAPI)
	assert.Equal(t, "websockets", config.Provider.Transport)
	assert.Equal(t, "secret-at-apply", config.Provider.APIKey)
	assert.Equal(t, map[string]string{"X-R42": "test"}, config.Provider.Headers)
}

func TestFactoryOpenSupportsDefaultProviderAndNoSelectedSkills(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	factory := newFactory(client, nil, noDelay, fixedRandom)

	_, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 0)})
	require.NoError(t, err)
	require.Len(t, client.configs, 1)
	assert.Nil(t, client.configs[0].Provider)
	assert.Empty(t, client.configs[0].CustomAgents)
	assert.Empty(t, client.configs[0].Agent)
}

func TestFactoryOpenRejectsMissingProviderEnvironmentValueBeforeSDKCall(t *testing.T) {
	t.Parallel()

	apiKeyRef := "MISSING_KEY"
	client := &fakeClient{}
	factory := newFactory(client, func(string) (string, bool) { return "", false }, noDelay, fixedRandom)

	_, err := factory.Open(t.Context(), SessionConfig{
		Provider: &provider.Config{
			Type:      provider.TypeOpenAI,
			Endpoint:  "https://models.example.test",
			APIKeyRef: &apiKeyRef,
		},
		Retry: retryPolicy(t, 2, 0),
	})

	require.EqualError(t, err, `materialize provider: environment variable "MISSING_KEY" is not set or empty`)
	assert.Empty(t, client.configs)
}

func TestFactoryOpenMapsBearerToken(t *testing.T) {
	t.Parallel()

	bearerToken := "bearer-secret"
	client := &fakeClient{}
	factory := newFactory(client, nil, noDelay, fixedRandom)

	_, err := factory.Open(t.Context(), SessionConfig{
		Provider: &provider.Config{
			Type:        provider.TypeAnthropic,
			Endpoint:    "https://anthropic.example.test",
			Headers:     cty.NilVal,
			BearerToken: &bearerToken,
		},
		Retry: retryPolicy(t, 0, 0),
	})

	require.NoError(t, err)
	require.Len(t, client.configs, 1)
	require.NotNil(t, client.configs[0].Provider)
	assert.Equal(t, "anthropic", client.configs[0].Provider.Type)
	assert.Equal(t, "bearer-secret", client.configs[0].Provider.BearerToken)
	assert.Empty(t, client.configs[0].Provider.WireAPI)
	assert.Empty(t, client.configs[0].Provider.Transport)
}

func TestFactoryOpenRetriesTransientLifecycleFailure(t *testing.T) {
	t.Parallel()

	created := &fakeSession{}
	client := &fakeClient{
		createErrors: []error{
			provider.TransientError{Err: errors.New("runtime unavailable")},
			provider.TransientError{Err: errors.New("runtime still unavailable")},
		},
		session: created,
	}
	var delays []time.Duration
	factory := newFactory(client, nil, func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}, fixedRandom)

	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 2, 0)})

	require.NoError(t, err)
	assert.Same(t, created, session.sdk)
	assert.Len(t, client.configs, 3)
	assert.Len(t, delays, 2)
	require.NotEmpty(t, client.configs[0].SessionID)
	for _, config := range client.configs[1:] {
		assert.Equal(t, client.configs[0].SessionID, config.SessionID)
	}
}

func TestFactoryOpenFailsPermanentLifecycleErrorImmediately(t *testing.T) {
	t.Parallel()

	client := &fakeClient{createErrors: []error{
		provider.HTTPError{StatusCode: 400, Err: errors.New("invalid model")},
	}}
	factory := newFactory(client, nil, noDelay, fixedRandom)

	_, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 10, 0)})

	require.ErrorContains(t, err, "create copilot session: http status 400")
	assert.Len(t, client.configs, 1)
}

func TestSessionSendAndWaitRetriesSameSession(t *testing.T) {
	t.Parallel()

	want := &sdk.SessionEvent{ID: "completed"}
	underlying := &fakeSession{
		sendErrors: []error{
			provider.TransientError{Err: errors.New("model overloaded")},
			provider.TransientError{Err: errors.New("model overloaded again")},
		},
		event: want,
	}
	client := &fakeClient{session: underlying}
	factory := newFactory(client, nil, noDelay, fixedRandom)
	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 2)})
	require.NoError(t, err)

	got, err := session.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.NoError(t, err)
	assert.Same(t, want, got)
	assert.Len(t, underlying.messages, 3)
	assert.Len(t, client.configs, 1, "model retry must not replace the session")
}

func TestSessionSendAndWaitFailsUnrecoverableSessionLossWithoutReplacement(t *testing.T) {
	t.Parallel()

	underlying := &fakeSession{sendErrors: []error{errors.New("session no longer exists")}}
	client := &fakeClient{session: underlying}
	factory := newFactory(client, nil, noDelay, fixedRandom)
	session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 4, 4)})
	require.NoError(t, err)

	_, err = session.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

	require.EqualError(t, err, "send copilot message: session no longer exists")
	assert.Len(t, underlying.messages, 1)
	assert.Len(t, client.configs, 1)
}

func TestOfficialSessionPreservesSDKModelErrorClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		data      *sdk.SessionErrorData
		transient bool
		permanent bool
	}{
		{
			name: "rate limit is transient",
			data: &sdk.SessionErrorData{
				ErrorType: "rate_limit",
				Message:   "too many requests",
			},
			transient: true,
		},
		{
			name: "authentication is permanent",
			data: &sdk.SessionErrorData{
				ErrorType: "authentication",
				Message:   "invalid credential",
			},
			permanent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			underlying := &fakeEventSession{
				event: sdk.SessionEvent{Data: tt.data},
				err:   errors.New("session error: " + tt.data.Message),
			}
			session := newOfficialSession(underlying)

			_, err := session.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

			require.Error(t, err)
			policy := retryPolicy(t, 0, 1)
			assert.Equal(t, tt.transient, policy.IsTransient(err))
			var permanent interface{ IsPermanent() bool }
			require.ErrorAs(t, err, &permanent)
			assert.Equal(t, tt.permanent, permanent.IsPermanent())
			assert.Equal(t, 1, underlying.unsubscribeCalls)
		})
	}
}

func TestOfficialSessionPassesThroughNonModelResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		result    *sdk.SessionEvent
		err       error
		wantError error
	}{
		{
			name:   "success",
			result: &sdk.SessionEvent{ID: "assistant-result"},
		},
		{
			name:      "transport error",
			err:       provider.TransientError{Err: errors.New("connection reset")},
			wantError: provider.TransientError{Err: errors.New("connection reset")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			underlying := &fakeEventSession{result: tt.result, err: tt.err}
			session := newOfficialSession(underlying)

			result, err := session.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})

			if tt.wantError == nil {
				require.NoError(t, err)
				assert.Same(t, tt.result, result)
			} else {
				require.EqualError(t, err, tt.wantError.Error())
			}
			assert.Equal(t, 1, underlying.unsubscribeCalls)
			require.NoError(t, session.Disconnect())
			assert.Equal(t, 1, underlying.disconnectCalls)
		})
	}
}

func TestRetryDelayFailureStopsCurrentOperation(t *testing.T) {
	t.Parallel()

	delayErr := context.Canceled
	tests := []struct {
		name          string
		run           func(*testing.T, *Factory) error
		expectedError string
	}{
		{
			name: "create",
			run: func(t *testing.T, factory *Factory) error {
				t.Helper()

				factory.client = &fakeClient{createErrors: []error{
					provider.TransientError{Err: errors.New("runtime unavailable")},
				}}
				_, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 1, 0)})
				return err
			},
			expectedError: "create copilot session retry: context canceled",
		},
		{
			name: "send",
			run: func(t *testing.T, factory *Factory) error {
				t.Helper()

				underlying := &fakeSession{sendErrors: []error{
					provider.TransientError{Err: errors.New("model unavailable")},
				}}
				factory.client = &fakeClient{session: underlying}
				session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 0, 1)})
				require.NoError(t, err)
				_, err = session.SendAndWait(t.Context(), sdk.MessageOptions{Prompt: "research"})
				return err
			},
			expectedError: "send copilot message retry: context canceled",
		},
		{
			name: "close",
			run: func(t *testing.T, factory *Factory) error {
				t.Helper()

				underlying := &fakeSession{disconnectErrors: []error{
					provider.TransientError{Err: errors.New("runtime unavailable")},
				}}
				factory.client = &fakeClient{session: underlying}
				session, err := factory.Open(t.Context(), SessionConfig{Retry: retryPolicy(t, 1, 0)})
				require.NoError(t, err)
				return session.Close(t.Context())
			},
			expectedError: "close copilot session after 1 attempt: context canceled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			factory := newFactory(nil, nil, func(context.Context, time.Duration) error {
				return delayErr
			}, fixedRandom)
			err := tt.run(t, factory)
			require.EqualError(t, err, tt.expectedError)
			assert.ErrorIs(t, err, delayErr)
		})
	}
}

func TestSessionCloseRetriesAndReturnsOnlyExhaustedCleanupWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		disconnectError []error
		lifecycleRetry  int
		wantCalls       int
		wantWarning     string
	}{
		{
			name:            "transient close recovers",
			disconnectError: []error{provider.TransientError{Err: errors.New("busy")}},
			lifecycleRetry:  1,
			wantCalls:       2,
		},
		{
			name: "exhausted transient close becomes warning",
			disconnectError: []error{
				provider.TransientError{Err: errors.New("busy")},
				provider.TransientError{Err: errors.New("still busy")},
			},
			lifecycleRetry: 1,
			wantCalls:      2,
			wantWarning:    "close copilot session after 2 attempts: still busy",
		},
		{
			name:            "permanent close immediately becomes warning",
			disconnectError: []error{provider.PermanentError{Err: errors.New("destroy rejected")}},
			lifecycleRetry:  5,
			wantCalls:       1,
			wantWarning:     "close copilot session after 1 attempt: destroy rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			underlying := &fakeSession{disconnectErrors: tt.disconnectError}
			factory := newFactory(&fakeClient{session: underlying}, nil, noDelay, fixedRandom)
			session, err := factory.Open(t.Context(), SessionConfig{
				Retry: retryPolicy(t, tt.lifecycleRetry, 0),
			})
			require.NoError(t, err)

			warning := session.Close(t.Context())

			assert.Equal(t, tt.wantCalls, underlying.disconnectCalls)
			if tt.wantWarning == "" {
				assert.Nil(t, warning)
				return
			}
			require.NotNil(t, warning)
			assert.Equal(t, tt.wantWarning, warning.Error())
		})
	}
}

type fakeClient struct {
	configs      []*sdk.SessionConfig
	createErrors []error
	session      sdkSession
}

func (c *fakeClient) CreateSession(ctx context.Context, config *sdk.SessionConfig) (sdkSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.configs = append(c.configs, config)
	index := len(c.configs) - 1
	if index < len(c.createErrors) {
		return nil, c.createErrors[index]
	}
	if c.session == nil {
		c.session = &fakeSession{}
	}
	return c.session, nil
}

type fakeSession struct {
	messages         []sdk.MessageOptions
	sendErrors       []error
	event            *sdk.SessionEvent
	disconnectErrors []error
	disconnectCalls  int
}

type fakeEventSession struct {
	handler          sdk.SessionEventHandler
	event            sdk.SessionEvent
	result           *sdk.SessionEvent
	err              error
	unsubscribeCalls int
	disconnectCalls  int
}

func (s *fakeEventSession) SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error) {
	if s.event.Data != nil {
		s.handler(s.event)
	}
	return s.result, s.err
}

func (s *fakeEventSession) On(handler sdk.SessionEventHandler) func() {
	s.handler = handler
	return func() { s.unsubscribeCalls++ }
}

func (s *fakeEventSession) Disconnect() error {
	s.disconnectCalls++
	return nil
}

func (s *fakeSession) SendAndWait(ctx context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.messages = append(s.messages, options)
	index := len(s.messages) - 1
	if index < len(s.sendErrors) {
		return nil, s.sendErrors[index]
	}
	return s.event, nil
}

func (s *fakeSession) Disconnect() error {
	s.disconnectCalls++
	index := s.disconnectCalls - 1
	if index < len(s.disconnectErrors) {
		return s.disconnectErrors[index]
	}
	return nil
}

func retryPolicy(t *testing.T, lifecycle, model int) provider.RetryPolicy {
	t.Helper()

	interval := time.Duration(0)
	policy, err := provider.MergeRetry(provider.DefaultRetryPolicy(), provider.RetryOverride{
		LifecycleRetries: &lifecycle,
		ModelCallRetries: &model,
		Interval:         &interval,
	})
	require.NoError(t, err)
	return policy
}

func providerHeaders(headers map[string]string) cty.Value {
	values := make(map[string]cty.Value, len(headers))
	for name, value := range headers {
		values[name] = cty.StringVal(value)
	}
	return cty.MapVal(values)
}

func noDelay(context.Context, time.Duration) error { return nil }

func fixedRandom() float64 { return 0.5 }
