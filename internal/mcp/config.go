package mcp

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	DefaultTimeout = 30 * time.Second
	MinTimeout     = time.Second
	MaxTimeout     = 5 * time.Minute
)

type Transport string

const (
	TransportHTTP  Transport = "http"
	TransportStdio Transport = "stdio"
)

type Config struct {
	Name        string        `json:"name"`
	RuntimeName string        `json:"runtime_name,omitempty"`
	Transport   Transport     `json:"transport"`
	Tools       []string      `json:"tools"`
	Resources   []string      `json:"resources,omitempty"`
	Timeout     time.Duration `json:"timeout"`
	HTTP        *HTTPConfig   `json:"http,omitempty"`
	Stdio       *StdioConfig  `json:"stdio,omitempty"`
}

type HTTPConfig struct {
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers,omitempty"`
	BearerToken    *string           `json:"bearer_token,omitempty"`
	BearerTokenRef *string           `json:"bearer_token_ref,omitempty"`
}

type StdioConfig struct {
	Command          string            `json:"command"`
	Args             []string          `json:"args,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	EnvRefs          map[string]string `json:"env_refs,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
}

type Tool struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Server Config `json:"server"`
}

type Resource struct {
	ID     string `json:"id"`
	URI    string `json:"uri"`
	Server Config `json:"server"`
}

type ResourceReadRequest struct {
	ServerName string
	URI        string
}

type ResourceContent struct {
	URI      string         `json:"uri"`
	MIMEType *string        `json:"mimeType,omitempty"`
	Text     *string        `json:"text,omitempty"`
	Blob     *string        `json:"blob,omitempty"`
	Meta     map[string]any `json:"_meta,omitempty"`
}

type ResourceRegistry map[string]Resource

func (r ResourceRegistry) Clone() ResourceRegistry {
	result := make(ResourceRegistry, len(r))
	for id, resource := range r {
		resource.Server = resource.Server.Clone()
		result[id] = resource
	}
	return result
}

func (t Tool) SDKName() string {
	return "mcp:" + t.Server.RuntimeServerName() + "-" + t.Name
}

func (c Config) RuntimeServerName() string {
	if c.RuntimeName != "" {
		return c.RuntimeName
	}
	return c.Name
}

type ToolRegistry map[string]Tool

func (r ToolRegistry) Clone() ToolRegistry {
	result := make(ToolRegistry, len(r))
	for id, tool := range r {
		tool.Server = tool.Server.Clone()
		result[id] = tool
	}
	return result
}

type EnvLookup func(string) (string, bool)

func (c Config) Clone() Config {
	result := c
	result.Tools = slices.Clone(c.Tools)
	result.Resources = slices.Clone(c.Resources)
	if c.HTTP != nil {
		httpConfig := *c.HTTP
		httpConfig.Headers = maps.Clone(c.HTTP.Headers)
		httpConfig.BearerToken = clonePointer(c.HTTP.BearerToken)
		httpConfig.BearerTokenRef = clonePointer(c.HTTP.BearerTokenRef)
		result.HTTP = &httpConfig
	}
	if c.Stdio != nil {
		stdioConfig := *c.Stdio
		stdioConfig.Args = slices.Clone(c.Stdio.Args)
		stdioConfig.Env = maps.Clone(c.Stdio.Env)
		stdioConfig.EnvRefs = maps.Clone(c.Stdio.EnvRefs)
		result.Stdio = &stdioConfig
	}
	return result
}

func (c Config) Validate() error {
	if len(c.Tools) == 0 {
		return errors.New("mcp server tools must be a non-empty explicit list")
	}
	return c.ValidateSelection()
}

// ValidateSelection validates a task-scoped server projection. A projection
// may expose no tools when the task selected only resources.
func (c Config) ValidateSelection() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("mcp server name is required")
	}
	if len(c.Tools) == 0 && len(c.Resources) == 0 {
		return errors.New("mcp server selection must contain a tool or resource")
	}
	if err := validateTools(c.Tools); err != nil {
		return err
	}
	if err := validateResources(c.Resources); err != nil {
		return err
	}
	if c.Timeout < MinTimeout || c.Timeout > MaxTimeout {
		return errors.New("mcp server timeout must be between 1s and 5m")
	}
	switch c.Transport {
	case TransportHTTP:
		if c.HTTP == nil || c.Stdio != nil {
			return errors.New("mcp server must have exactly one http or stdio block")
		}
		return c.HTTP.Validate()
	case TransportStdio:
		if c.Stdio == nil || c.HTTP != nil {
			return errors.New("mcp server must have exactly one http or stdio block")
		}
		return c.Stdio.Validate()
	default:
		return errors.New("mcp server must have exactly one http or stdio block")
	}
}

func validateResources(resources []string) error {
	seen := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		if strings.TrimSpace(resource) == "" {
			return errors.New("mcp server resources must not contain empty values")
		}
		if _, exists := seen[resource]; exists {
			return fmt.Errorf("mcp server resource %q is declared more than once", resource)
		}
		seen[resource] = struct{}{}
	}
	return nil
}

func validateTools(tools []string) error {
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if strings.TrimSpace(tool) == "" {
			return errors.New("mcp server tools must not contain empty values")
		}
		if tool == "*" {
			return errors.New("mcp server tools must not contain wildcard")
		}
		if _, exists := seen[tool]; exists {
			return fmt.Errorf("mcp server tool %q is declared more than once", tool)
		}
		seen[tool] = struct{}{}
	}
	return nil
}

func (c HTTPConfig) Validate() error {
	parsed, err := url.Parse(c.URL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("mcp http url must be an absolute url")
	}
	if parsed.User != nil {
		return errors.New("mcp http url must not contain user information")
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopback(parsed.Hostname())) {
		return errors.New("mcp http url must use https unless it targets loopback")
	}
	for name, value := range c.Headers {
		if strings.EqualFold(name, "Authorization") {
			return errors.New("mcp http headers must not contain authorization")
		}
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return errors.New("mcp http headers must not contain empty names or newlines")
		}
	}
	if c.BearerToken != nil && c.BearerTokenRef != nil {
		return errors.New("mcp http bearer_token and bearer_token_ref are mutually exclusive")
	}
	if c.BearerToken != nil && strings.TrimSpace(*c.BearerToken) == "" {
		return errors.New("mcp http bearer_token must not be empty")
	}
	if c.BearerTokenRef != nil && strings.TrimSpace(*c.BearerTokenRef) == "" {
		return errors.New("mcp http bearer_token_ref must not be empty")
	}
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (c StdioConfig) Validate() error {
	if strings.TrimSpace(c.Command) == "" {
		return errors.New("mcp stdio command is required")
	}
	for name, reference := range c.EnvRefs {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(reference) == "" {
			return errors.New("mcp stdio env_refs must not contain empty names or references")
		}
		if _, exists := c.Env[name]; exists {
			return fmt.Errorf("mcp stdio environment variable %q is configured by both env and env_refs", name)
		}
	}
	return nil
}

func (c Config) Materialize(lookup EnvLookup) (Config, error) {
	result := c.Clone()
	if result.HTTP != nil {
		token := ""
		if result.HTTP.BearerToken != nil {
			token = *result.HTTP.BearerToken
		} else if result.HTTP.BearerTokenRef != nil {
			resolved, ok := lookupValue(lookup, *result.HTTP.BearerTokenRef)
			if !ok {
				return Config{}, fmt.Errorf("mcp server %q bearer token environment variable %q is not set", c.Name, *result.HTTP.BearerTokenRef)
			}
			token = resolved
		}
		if result.HTTP.BearerToken != nil || result.HTTP.BearerTokenRef != nil {
			if result.HTTP.Headers == nil {
				result.HTTP.Headers = map[string]string{}
			}
			result.HTTP.Headers["Authorization"] = "Bearer " + token
		}
	}
	if result.Stdio != nil {
		if result.Stdio.Env == nil {
			result.Stdio.Env = map[string]string{}
		}
		for name, reference := range result.Stdio.EnvRefs {
			value, ok := lookupValue(lookup, reference)
			if !ok {
				return Config{}, fmt.Errorf("mcp server %q environment variable %q is not set", c.Name, reference)
			}
			result.Stdio.Env[name] = value
		}
	}
	return result, nil
}

func lookupValue(lookup EnvLookup, name string) (string, bool) {
	if lookup == nil {
		return "", false
	}
	value, ok := lookup(name)
	return value, ok && value != ""
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
