package copilot

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/lonegunmanb/r42/internal/provider"
)

const researchAgentName = "r42_research"

type SessionConfig struct {
	Provider         *provider.Config
	Retry            provider.RetryPolicy
	Model            string
	Profile          string
	ReasoningEffort  string
	SystemPrompt     string
	WorkingDirectory string
	Tools            []sdk.Tool
	AvailableTools   []string
	ExcludedTools    []string
	SkillDirectories []string
	Skills           []string
	DisabledSkills   []string
	Hooks            *sdk.SessionHooks
}

type Factory struct {
	client sdkClient
	lookup provider.EnvLookup
	delay  func(context.Context, time.Duration) error
	random func() float64
}

func NewFactory(client *sdk.Client, lookup provider.EnvLookup) *Factory {
	return newFactory(officialClient{client: client}, lookup, provider.Delay, rand.Float64)
}

func newFactory(
	client sdkClient,
	lookup provider.EnvLookup,
	delay func(context.Context, time.Duration) error,
	random func() float64,
) *Factory {
	return &Factory{client: client, lookup: lookup, delay: delay, random: random}
}

func (f *Factory) Open(ctx context.Context, config SessionConfig) (*Session, error) {
	sdkConfig, err := f.sessionConfig(config)
	if err != nil {
		return nil, err
	}

	var session sdkSession
	for attempt := 0; ; attempt++ {
		session, err = f.client.CreateSession(ctx, sdkConfig)
		if err == nil {
			return &Session{
				sdk:    session,
				retry:  config.Retry,
				delay:  f.delay,
				random: f.random,
			}, nil
		}
		if attempt >= config.Retry.LifecycleRetries || !config.Retry.IsTransient(err) {
			return nil, fmt.Errorf("create copilot session: %w", err)
		}
		if delayErr := f.delay(ctx, config.Retry.Backoff(attempt, f.random())); delayErr != nil {
			return nil, fmt.Errorf("create copilot session retry: %w", delayErr)
		}
	}
}

func (f *Factory) sessionConfig(config SessionConfig) (*sdk.SessionConfig, error) {
	profile := config.Profile
	if profile == "" {
		profile = config.Model
	}
	providerConfig, err := f.providerConfig(config.Provider, profile, config.Model)
	if err != nil {
		return nil, err
	}

	result := &sdk.SessionConfig{
		SessionID:           cryptorand.Text(),
		Model:               config.Model,
		ReasoningEffort:     config.ReasoningEffort,
		SystemMessage:       &sdk.SystemMessageConfig{Mode: "append", Content: config.SystemPrompt},
		WorkingDirectory:    config.WorkingDirectory,
		Streaming:           sdk.Bool(true),
		Provider:            providerConfig,
		Tools:               slices.Clone(config.Tools),
		AvailableTools:      slices.Clone(config.AvailableTools),
		ExcludedTools:       slices.Clone(config.ExcludedTools),
		OnPermissionRequest: sdk.PermissionHandler.ApproveAll,
		EnableSkills:        sdk.Bool(true),
		SkillDirectories:    slices.Clone(config.SkillDirectories),
		DisabledSkills:      slices.Clone(config.DisabledSkills),
		Hooks:               config.Hooks,
	}
	if len(config.Skills) > 0 {
		result.CustomAgents = []sdk.CustomAgentConfig{{
			Name:            researchAgentName,
			Prompt:          config.SystemPrompt,
			Skills:          slices.Clone(config.Skills),
			Model:           profile,
			ReasoningEffort: config.ReasoningEffort,
		}}
		result.Agent = researchAgentName
	}
	return result, nil
}

func (f *Factory) providerConfig(config *provider.Config, profile, model string) (*sdk.ProviderConfig, error) {
	if config == nil {
		return nil, nil
	}
	materialized, err := config.Materialize(f.lookup)
	if err != nil {
		return nil, fmt.Errorf("materialize provider: %w", err)
	}
	result := &sdk.ProviderConfig{
		Type:      string(materialized.Type),
		BaseURL:   materialized.Endpoint,
		Headers:   materialized.Headers,
		ModelID:   profile,
		WireModel: model,
	}
	if materialized.WireAPI != nil {
		result.WireAPI = string(*materialized.WireAPI)
	}
	if materialized.Transport != nil {
		result.Transport = string(*materialized.Transport)
	}
	switch materialized.Auth.Kind {
	case provider.AuthAPIKey:
		result.APIKey = materialized.Auth.Value
	case provider.AuthBearerToken:
		result.BearerToken = materialized.Auth.Value
	}
	return result, nil
}

type Session struct {
	sdk    sdkSession
	retry  provider.RetryPolicy
	delay  func(context.Context, time.Duration) error
	random func() float64
}

func (s *Session) SendAndWait(ctx context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	for attempt := 0; ; attempt++ {
		event, err := s.sdk.SendAndWait(ctx, options)
		if err == nil {
			return event, nil
		}
		if attempt >= s.retry.ModelCallRetries || !s.retry.IsTransient(err) {
			return nil, fmt.Errorf("send copilot message: %w", err)
		}
		if delayErr := s.delay(ctx, s.retry.Backoff(attempt, s.random())); delayErr != nil {
			return nil, fmt.Errorf("send copilot message retry: %w", delayErr)
		}
	}
}

func (s *Session) On(handler sdk.SessionEventHandler) func() {
	return s.sdk.On(handler)
}

func (s *Session) Close(ctx context.Context) error {
	for attempt := 0; ; attempt++ {
		err := s.sdk.Disconnect()
		if err == nil {
			return nil
		}
		if attempt >= s.retry.LifecycleRetries || !s.retry.IsTransient(err) {
			return &CleanupWarning{Attempts: attempt + 1, Err: err}
		}
		if delayErr := s.delay(ctx, s.retry.Backoff(attempt, s.random())); delayErr != nil {
			return &CleanupWarning{Attempts: attempt + 1, Err: delayErr}
		}
	}
}

type CleanupWarning struct {
	Attempts int
	Err      error
}

func (w *CleanupWarning) Error() string {
	noun := "attempt"
	if w.Attempts != 1 {
		noun = "attempts"
	}
	return fmt.Sprintf("close copilot session after %d %s: %v", w.Attempts, noun, w.Err)
}

func (w *CleanupWarning) Unwrap() error {
	return w.Err
}

type sdkClient interface {
	CreateSession(context.Context, *sdk.SessionConfig) (sdkSession, error)
}

type sdkSession interface {
	SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error)
	On(sdk.SessionEventHandler) func()
	Disconnect() error
}

type officialClient struct {
	client *sdk.Client
}

func (c officialClient) CreateSession(ctx context.Context, config *sdk.SessionConfig) (sdkSession, error) {
	session, err := c.client.CreateSession(ctx, config)
	if err != nil {
		return nil, err
	}
	return newOfficialSession(session), nil
}

type eventSession interface {
	sdkSession
	On(sdk.SessionEventHandler) func()
}

type officialSession struct {
	sdk eventSession
}

func newOfficialSession(session eventSession) *officialSession {
	return &officialSession{sdk: session}
}

func (s *officialSession) SendAndWait(
	ctx context.Context,
	options sdk.MessageOptions,
) (*sdk.SessionEvent, error) {
	var sessionError *sdk.SessionErrorData
	var mutex sync.Mutex
	unsubscribe := s.sdk.On(func(event sdk.SessionEvent) {
		data, ok := event.Data.(*sdk.SessionErrorData)
		if !ok {
			return
		}
		dataCopy := *data
		mutex.Lock()
		sessionError = &dataCopy
		mutex.Unlock()
	})
	defer unsubscribe()

	event, err := s.sdk.SendAndWait(ctx, options)
	if err == nil {
		return event, nil
	}
	mutex.Lock()
	data := sessionError
	mutex.Unlock()
	if data == nil {
		return nil, err
	}
	return nil, &modelCallError{data: *data, err: err}
}

func (s *officialSession) Disconnect() error {
	return s.sdk.Disconnect()
}

func (s *officialSession) On(handler sdk.SessionEventHandler) func() {
	return s.sdk.On(handler)
}

type modelCallError struct {
	data sdk.SessionErrorData
	err  error
}

func (e *modelCallError) Error() string {
	return e.err.Error()
}

func (e *modelCallError) Unwrap() error {
	return e.err
}

func (e *modelCallError) HTTPStatusCode() int {
	if e.data.StatusCode == nil {
		return 0
	}
	return int(*e.data.StatusCode)
}

func (e *modelCallError) IsTransient() bool {
	return e.data.ErrorType == "rate_limit"
}

func (e *modelCallError) IsPermanent() bool {
	switch e.data.ErrorType {
	case "authentication", "authorization", "quota", "context_limit":
		return true
	default:
		return false
	}
}
