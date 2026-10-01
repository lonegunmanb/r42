package spec

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zclconf/go-cty/cty"
)

const (
	StarlarkToolName              = "r42_starlark"
	defaultStarlarkDescription    = "Execute isolated, resource-bounded numerical Starlark programs."
	defaultStarlarkMaxSteps       = 1_000_000
	defaultStarlarkTimeout        = 5 * time.Second
	defaultStarlarkMaxSourceBytes = 65_536
	defaultStarlarkMaxDataBytes   = 1_048_576
	defaultStarlarkMaxResultBytes = 1_048_576
	defaultStarlarkMaxStdoutBytes = 16_384
	defaultStarlarkMemoryLimit    = 134_217_728
	maximumStarlarkMaxSteps       = 10_000_000
	maximumStarlarkTimeout        = 30 * time.Second
	maximumStarlarkMaxSourceBytes = 262_144
	maximumStarlarkMaxDataBytes   = 8_388_608
	maximumStarlarkMaxResultBytes = 8_388_608
	maximumStarlarkMaxStdoutBytes = 65_536
	maximumStarlarkMemoryLimit    = 268_435_456
)

// StarlarkConfig bounds the calculator built into one research unit.
type StarlarkConfig struct {
	Description    string        `json:"description"`
	MaxSteps       int           `json:"max_steps"`
	Timeout        time.Duration `json:"timeout"`
	MaxSourceBytes int           `json:"max_source_bytes"`
	MaxDataBytes   int           `json:"max_data_bytes"`
	MaxResultBytes int           `json:"max_result_bytes"`
	MaxStdoutBytes int           `json:"max_stdout_bytes"`
	MemoryLimit    int           `json:"memory_limit"`
}

// DefaultStarlarkConfig returns the calculator settings used when starlark is omitted.
func DefaultStarlarkConfig() StarlarkConfig {
	return StarlarkConfig{
		Description: defaultStarlarkDescription, MaxSteps: defaultStarlarkMaxSteps,
		Timeout: defaultStarlarkTimeout, MaxSourceBytes: defaultStarlarkMaxSourceBytes,
		MaxDataBytes: defaultStarlarkMaxDataBytes, MaxResultBytes: defaultStarlarkMaxResultBytes,
		MaxStdoutBytes: defaultStarlarkMaxStdoutBytes, MemoryLimit: defaultStarlarkMemoryLimit,
	}
}

// EffectiveStarlark returns the configured settings or defaults for programmatic configs.
func (c Config) EffectiveStarlark() StarlarkConfig {
	if c.Starlark == (StarlarkConfig{}) {
		return DefaultStarlarkConfig()
	}
	return c.Starlark
}

func (c StarlarkConfig) validate() error {
	if strings.TrimSpace(c.Description) == "" {
		return errors.New("research starlark description must not be empty")
	}
	if err := validateStarlarkLimit("max_steps", c.MaxSteps, maximumStarlarkMaxSteps); err != nil {
		return err
	}
	if c.Timeout <= 0 {
		return errors.New("research starlark timeout must be positive")
	}
	if c.Timeout > maximumStarlarkTimeout {
		return fmt.Errorf("research starlark timeout must not exceed %s", maximumStarlarkTimeout)
	}
	for _, limit := range []struct {
		name    string
		value   int
		maximum int
	}{
		{name: "max_source_bytes", value: c.MaxSourceBytes, maximum: maximumStarlarkMaxSourceBytes},
		{name: "max_data_bytes", value: c.MaxDataBytes, maximum: maximumStarlarkMaxDataBytes},
		{name: "max_result_bytes", value: c.MaxResultBytes, maximum: maximumStarlarkMaxResultBytes},
		{name: "max_stdout_bytes", value: c.MaxStdoutBytes, maximum: maximumStarlarkMaxStdoutBytes},
		{name: "memory_limit", value: c.MemoryLimit, maximum: maximumStarlarkMemoryLimit},
	} {
		if err := validateStarlarkLimit(limit.name, limit.value, limit.maximum); err != nil {
			return err
		}
	}
	return nil
}

func validateStarlarkLimit(name string, value, maximum int) error {
	if value <= 0 {
		return fmt.Errorf("research starlark %s must be a positive integer", name)
	}
	if value > maximum {
		return fmt.Errorf("research starlark %s must not exceed %d", name, maximum)
	}
	return nil
}

type StarlarkBlock struct {
	Description    *string `hcl:"description,optional"`
	MaxSteps       *int    `hcl:"max_steps,optional"`
	Timeout        *string `hcl:"timeout,optional"`
	MaxSourceBytes *int    `hcl:"max_source_bytes,optional"`
	MaxDataBytes   *int    `hcl:"max_data_bytes,optional"`
	MaxResultBytes *int    `hcl:"max_result_bytes,optional"`
	MaxStdoutBytes *int    `hcl:"max_stdout_bytes,optional"`
	MemoryLimit    *int    `hcl:"memory_limit,optional"`
}

var starlarkBlockType = cty.Object(map[string]cty.Type{
	"description": cty.String, "max_steps": cty.Number, "timeout": cty.String,
	"max_source_bytes": cty.Number, "max_data_bytes": cty.Number, "max_result_bytes": cty.Number,
	"max_stdout_bytes": cty.Number, "memory_limit": cty.Number,
})

func starlarkBlockValues(blocks []StarlarkBlock) cty.Value {
	if len(blocks) != 1 {
		return cty.ListValEmpty(starlarkBlockType)
	}
	block := blocks[0]
	return cty.ListVal([]cty.Value{cty.ObjectVal(map[string]cty.Value{
		"description": optionalStringValue(block.Description),
		"max_steps":   optionalIntValue(block.MaxSteps), "timeout": optionalStringValue(block.Timeout),
		"max_source_bytes": optionalIntValue(block.MaxSourceBytes), "max_data_bytes": optionalIntValue(block.MaxDataBytes),
		"max_result_bytes": optionalIntValue(block.MaxResultBytes), "max_stdout_bytes": optionalIntValue(block.MaxStdoutBytes),
		"memory_limit": optionalIntValue(block.MemoryLimit),
	})})
}

func (b StarlarkBlock) config() (StarlarkConfig, error) {
	result := DefaultStarlarkConfig()
	if b.Description != nil {
		result.Description = *b.Description
	}
	if b.MaxSteps != nil {
		result.MaxSteps = *b.MaxSteps
	}
	if b.Timeout != nil {
		timeout, err := time.ParseDuration(*b.Timeout)
		if err != nil {
			return StarlarkConfig{}, errors.New("research starlark timeout must be a positive duration")
		}
		result.Timeout = timeout
	}
	if b.MaxSourceBytes != nil {
		result.MaxSourceBytes = *b.MaxSourceBytes
	}
	if b.MaxDataBytes != nil {
		result.MaxDataBytes = *b.MaxDataBytes
	}
	if b.MaxResultBytes != nil {
		result.MaxResultBytes = *b.MaxResultBytes
	}
	if b.MaxStdoutBytes != nil {
		result.MaxStdoutBytes = *b.MaxStdoutBytes
	}
	if b.MemoryLimit != nil {
		result.MemoryLimit = *b.MemoryLimit
	}
	if err := result.validate(); err != nil {
		return StarlarkConfig{}, err
	}
	return result, nil
}
