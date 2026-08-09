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
				sdk:          session,
				factory:      f,
				sessionID:    sdkConfig.SessionID,
				resumeConfig: resumeSessionConfig(sdkConfig),
				retry:        config.Retry,
				delay:        f.delay,
				random:       f.random,
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

func (f *Factory) resume(
	ctx context.Context,
	sessionID string,
	config *sdk.ResumeSessionConfig,
	retry provider.RetryPolicy,
) (sdkSession, error) {
	for attempt := 0; ; attempt++ {
		session, err := f.client.ResumeSession(ctx, sessionID, cloneResumeSessionConfig(config))
		if err == nil {
			return session, nil
		}
		if attempt >= retry.LifecycleRetries || !retry.IsTransient(err) {
			return nil, fmt.Errorf("resume copilot session: %w", err)
		}
		if delayErr := f.delay(ctx, retry.Backoff(attempt, f.random())); delayErr != nil {
			return nil, fmt.Errorf("resume copilot session retry: %w", delayErr)
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

func resumeSessionConfig(config *sdk.SessionConfig) *sdk.ResumeSessionConfig {
	return &sdk.ResumeSessionConfig{
		Model:               config.Model,
		Tools:               slices.Clone(config.Tools),
		SystemMessage:       config.SystemMessage,
		AvailableTools:      slices.Clone(config.AvailableTools),
		ExcludedTools:       slices.Clone(config.ExcludedTools),
		Provider:            config.Provider,
		ReasoningEffort:     config.ReasoningEffort,
		OnPermissionRequest: config.OnPermissionRequest,
		Hooks:               config.Hooks,
		WorkingDirectory:    config.WorkingDirectory,
		EnableSkills:        config.EnableSkills,
		Streaming:           config.Streaming,
		CustomAgents:        slices.Clone(config.CustomAgents),
		Agent:               config.Agent,
		SkillDirectories:    slices.Clone(config.SkillDirectories),
		DisabledSkills:      slices.Clone(config.DisabledSkills),
		ContinuePendingWork: sdk.Bool(false),
	}
}

func cloneResumeSessionConfig(config *sdk.ResumeSessionConfig) *sdk.ResumeSessionConfig {
	clone := *config
	clone.Tools = slices.Clone(config.Tools)
	clone.AvailableTools = slices.Clone(config.AvailableTools)
	clone.ExcludedTools = slices.Clone(config.ExcludedTools)
	clone.CustomAgents = slices.Clone(config.CustomAgents)
	clone.SkillDirectories = slices.Clone(config.SkillDirectories)
	clone.DisabledSkills = slices.Clone(config.DisabledSkills)
	return &clone
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
	mu           sync.RWMutex
	sdk          sdkSession
	factory      *Factory
	sessionID    string
	resumeConfig *sdk.ResumeSessionConfig
	retry        provider.RetryPolicy
	delay        func(context.Context, time.Duration) error
	random       func() float64
}

func (s *Session) SendAndWait(ctx context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	current := s.current()
	if current == nil {
		return nil, fmt.Errorf("send copilot message: session is unavailable")
	}
	for attempt := 0; ; attempt++ {
		event, err := current.SendAndWait(ctx, options)
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
	current := s.current()
	if current == nil {
		return func() {}
	}
	return current.On(handler)
}

func (s *Session) Abort(ctx context.Context) error {
	current := s.current()
	if current == nil {
		return fmt.Errorf("abort copilot session: session is unavailable")
	}
	abortable, ok := current.(interface{ Abort(context.Context) error })
	if !ok {
		return fmt.Errorf("abort copilot session: SDK session does not support abort")
	}
	if err := abortable.Abort(ctx); err != nil {
		return fmt.Errorf("abort copilot session: %w", err)
	}
	return nil
}

func (s *Session) Resume(ctx context.Context) error {
	current := s.current()
	if current == nil {
		return fmt.Errorf("resume copilot session: session is unavailable")
	}
	if _, err := s.disconnect(ctx, current); err != nil {
		return fmt.Errorf("disconnect copilot session before resume: %w", err)
	}
	s.clear(current)
	resumed, err := s.factory.resume(ctx, s.sessionID, s.resumeConfig, s.retry)
	if err != nil {
		return err
	}
	s.replace(resumed)
	return nil
}

func (s *Session) Close(ctx context.Context) error {
	current := s.current()
	if current == nil {
		return nil
	}
	attempts, err := s.disconnect(ctx, current)
	if err == nil {
		s.clear(current)
		return nil
	}
	return &CleanupWarning{Attempts: attempts, Err: err}
}

func (s *Session) disconnect(ctx context.Context, current sdkSession) (int, error) {
	for attempt := 0; ; attempt++ {
		err := current.Disconnect()
		if err == nil {
			return attempt + 1, nil
		}
		if attempt >= s.retry.LifecycleRetries || !s.retry.IsTransient(err) {
			return attempt + 1, err
		}
		if delayErr := s.delay(ctx, s.retry.Backoff(attempt, s.random())); delayErr != nil {
			return attempt + 1, delayErr
		}
	}
}

func (s *Session) current() sdkSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sdk
}

func (s *Session) clear(current sdkSession) {
	s.mu.Lock()
	if s.sdk == current {
		s.sdk = nil
	}
	s.mu.Unlock()
}

func (s *Session) replace(session sdkSession) {
	s.mu.Lock()
	s.sdk = session
	s.mu.Unlock()
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
	ResumeSession(context.Context, string, *sdk.ResumeSessionConfig) (sdkSession, error)
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

func (c officialClient) ResumeSession(
	ctx context.Context,
	sessionID string,
	config *sdk.ResumeSessionConfig,
) (sdkSession, error) {
	session, err := c.client.ResumeSession(ctx, sessionID, config)
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

func (s *officialSession) Abort(ctx context.Context) error {
	abortable, ok := s.sdk.(interface{ Abort(context.Context) error })
	if !ok {
		return fmt.Errorf("SDK session does not support abort")
	}
	return abortable.Abort(ctx)
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
