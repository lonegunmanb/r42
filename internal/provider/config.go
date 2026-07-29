package provider

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lonegunmanb/r42/internal/spec"
	"github.com/zclconf/go-cty/cty"
)

type Type string

const (
	TypeOpenAI    Type = "openai"
	TypeAzure     Type = "azure"
	TypeAnthropic Type = "anthropic"
)

type WireAPI string

const (
	WireAPICompletions WireAPI = "completions"
	WireAPIResponses   WireAPI = "responses"
)

type Transport string

const (
	TransportHTTP       Transport = "http"
	TransportWebSockets Transport = "websockets"
)

type Config struct {
	Type           Type
	Endpoint       string
	WireAPI        *WireAPI
	Transport      *Transport
	Headers        cty.Value
	APIKey         *string
	APIKeyRef      *string
	BearerToken    *string
	BearerTokenRef *string
	Retry          RetryOverride
}

func (c Config) Validate() error {
	switch c.Type {
	case TypeOpenAI, TypeAzure, TypeAnthropic:
	default:
		return errors.New("provider type must be one of openai, azure, or anthropic")
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return errors.New("provider endpoint is required")
	}
	if c.WireAPI != nil && *c.WireAPI != WireAPICompletions && *c.WireAPI != WireAPIResponses {
		return errors.New("wire api must be completions or responses")
	}
	if c.Transport != nil && *c.Transport != TransportHTTP && *c.Transport != TransportWebSockets {
		return errors.New("transport must be http or websockets")
	}
	if c.Type == TypeAnthropic && (c.WireAPI != nil || c.Transport != nil) {
		return errors.New("anthropic provider does not use wire api or transport")
	}
	if c.Type != TypeAnthropic {
		wireAPI := WireAPICompletions
		if c.WireAPI != nil {
			wireAPI = *c.WireAPI
		}
		if c.Transport != nil && *c.Transport == TransportWebSockets && wireAPI != WireAPIResponses {
			return errors.New("websockets transport requires responses wire api")
		}
	}
	if err := validateAuthentication(c); err != nil {
		return err
	}
	if err := ValidateHeaders(c.Headers); err != nil {
		return err
	}
	_, err := MergeRetry(DefaultRetryPolicy(), c.Retry)
	return err
}

type AuthKind string

const (
	AuthNone        AuthKind = ""
	AuthAPIKey      AuthKind = "api_key"
	AuthBearerToken AuthKind = "bearer_token"
)

type Auth struct {
	Kind  AuthKind
	Value string
}

func (a Auth) Sensitive() bool {
	return a.Kind != AuthNone
}

type Materialized struct {
	Type      Type
	Endpoint  string
	WireAPI   *WireAPI
	Transport *Transport
	Headers   map[string]string
	Auth      Auth
}

type EnvLookup func(name string) (string, bool)

func (c Config) Materialize(lookup EnvLookup) (Materialized, error) {
	if err := c.Validate(); err != nil {
		return Materialized{}, err
	}

	headers, err := MaterializeHeaders(c.Headers)
	if err != nil {
		// note: untested because Validate has already checked the same immutable cty value.
		return Materialized{}, err
	}
	auth, err := c.materializeAuth(lookup)
	if err != nil {
		return Materialized{}, err
	}

	result := Materialized{
		Type:     c.Type,
		Endpoint: c.Endpoint,
		Headers:  headers,
		Auth:     auth,
	}
	if c.Type != TypeAnthropic {
		wireAPI := WireAPICompletions
		if c.WireAPI != nil {
			wireAPI = *c.WireAPI
		}
		transport := TransportHTTP
		if c.Transport != nil {
			transport = *c.Transport
		}
		result.WireAPI = &wireAPI
		result.Transport = &transport
	}
	return result, nil
}

func validateAuthentication(config Config) error {
	values := []*string{
		config.APIKey,
		config.APIKeyRef,
		config.BearerToken,
		config.BearerTokenRef,
	}
	configured := 0
	for _, value := range values {
		if value == nil {
			continue
		}
		configured++
		if strings.TrimSpace(*value) == "" {
			return errors.New("configured authentication field must not be empty")
		}
	}
	if configured > 1 {
		return errors.New("at most one authentication field may be set")
	}
	return nil
}

func (c Config) materializeAuth(lookup EnvLookup) (Auth, error) {
	switch {
	case c.APIKey != nil:
		return Auth{Kind: AuthAPIKey, Value: *c.APIKey}, nil
	case c.BearerToken != nil:
		return Auth{Kind: AuthBearerToken, Value: *c.BearerToken}, nil
	case c.APIKeyRef != nil:
		return resolveAuth(AuthAPIKey, *c.APIKeyRef, lookup)
	case c.BearerTokenRef != nil:
		return resolveAuth(AuthBearerToken, *c.BearerTokenRef, lookup)
	default:
		return Auth{}, nil
	}
}

func resolveAuth(kind AuthKind, name string, lookup EnvLookup) (Auth, error) {
	if lookup != nil {
		if value, found := lookup(name); found && value != "" {
			return Auth{Kind: kind, Value: value}, nil
		}
	}
	return Auth{}, fmt.Errorf("environment variable %q is not set or empty", name)
}

func ValidateHeaders(headers cty.Value) error {
	if headers.Type().Equals(cty.NilType) {
		return nil
	}
	unmarked, _ := headers.UnmarkDeep()
	if !unmarked.IsWhollyKnown() {
		return errors.New("headers must be wholly known during plan")
	}
	if !unmarked.Type().Equals(cty.Map(cty.String)) {
		return errors.New("headers must be map of string")
	}
	if !unmarked.IsNull() {
		for _, value := range unmarked.AsValueMap() {
			if value.IsNull() {
				return errors.New("header values must not be null")
			}
		}
	}
	return nil
}

func HeadersSensitive(headers cty.Value) bool {
	if headers.Type().Equals(cty.NilType) {
		return false
	}
	return spec.IsSensitive(headers)
}

func MaterializeHeaders(headers cty.Value) (map[string]string, error) {
	if err := ValidateHeaders(headers); err != nil {
		return nil, err
	}
	result := map[string]string{}
	if headers.Type().Equals(cty.NilType) {
		return result, nil
	}
	unmarked, _ := headers.UnmarkDeep()
	if unmarked.IsNull() {
		return result, nil
	}
	for name, value := range unmarked.AsValueMap() {
		result[name] = value.AsString()
	}
	return result, nil
}
