package variableschema_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/r42/internal/variableschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type protocolVariable struct {
	Name            string          `json:"name"`
	Description     *string         `json:"description"`
	Type            string          `json:"type"`
	Required        bool            `json:"required"`
	Nullable        bool            `json:"nullable"`
	Sensitive       bool            `json:"sensitive"`
	HasDefault      bool            `json:"has_default"`
	Default         json.RawMessage `json:"default"`
	DefaultRedacted bool            `json:"default_redacted"`
}

func TestInspectReturnsStableRootVariableSchema(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeConfig(t, directory, "variables.r42.hcl", `
variable "z_required" {
  description = "Required input"
  type        = string
}

variable "bool_value" {
  type    = bool
  default = true
}

variable "number_value" {
  type    = number
  default = 2
}

variable "list_value" {
  type    = list(string)
  default = ["one", "two"]
}

variable "set_value" {
  type    = set(string)
  default = ["two", "one"]
}

variable "map_value" {
  type    = map(number)
  default = { b = 2, a = 1 }
}

variable "tuple_value" {
  type    = tuple([string, number, bool])
  default = ["one", 2, true]
}

variable "object_value" {
  type = object({
    z_required = string
    a_optional = optional(string)
    label = optional(string, "fallback")
    nested = optional(object({
      enabled = optional(bool, true)
    }), {})
  })
  default = {
    z_required = "present"
  }
}

variable "nullable_value" {
  type     = number
  default  = null
  nullable = true
}

variable "non_nullable_value" {
  type     = string
  default  = "fallback"
  nullable = false
}

variable "secret_value" {
  type      = string
  default   = "never-print-this-secret"
  sensitive = true
}
`)

	document, err := variableschema.Inspect(t.Context(), directory)
	require.NoError(t, err)

	first, err := variableschema.Marshal(document)
	require.NoError(t, err)
	second, err := variableschema.Marshal(document)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.NotContains(t, string(first), "never-print-this-secret")

	var decoded struct {
		SchemaVersion int                `json:"schema_version"`
		Variables     []protocolVariable `json:"variables"`
	}
	require.NoError(t, json.Unmarshal(first, &decoded))
	assert.Equal(t, 1, decoded.SchemaVersion)
	require.Len(t, decoded.Variables, 11)
	assert.Equal(t, []string{
		"bool_value", "list_value", "map_value", "non_nullable_value", "nullable_value", "number_value",
		"object_value", "secret_value", "set_value", "tuple_value", "z_required",
	}, variableNames(decoded.Variables))

	expectedTypes := map[string]string{
		"bool_value":         "bool",
		"list_value":         "list(string)",
		"map_value":          "map(number)",
		"non_nullable_value": "string",
		"nullable_value":     "number",
		"number_value":       "number",
		"object_value":       "object({a_optional=optional(string),label=optional(string,\"fallback\"),nested=optional(object({enabled=optional(bool,true)}),{\"enabled\":null}),z_required=string})",
		"secret_value":       "string",
		"set_value":          "set(string)",
		"tuple_value":        "tuple([string,number,bool])",
		"z_required":         "string",
	}
	for _, variable := range decoded.Variables {
		assert.Equal(t, expectedTypes[variable.Name], variable.Type)
		expression, diagnostics := hclsyntax.ParseExpression(
			[]byte(variable.Type), "type.hcl", hcl.InitialPos,
		)
		require.False(t, diagnostics.HasErrors(), diagnostics.Error())
		_, _, diagnostics = typeexpr.TypeConstraintWithDefaults(expression)
		assert.False(t, diagnostics.HasErrors(), diagnostics.Error())
	}

	byName := make(map[string]json.RawMessage, len(decoded.Variables))
	for _, variable := range decoded.Variables {
		encoded, marshalErr := json.Marshal(variable)
		require.NoError(t, marshalErr)
		byName[variable.Name] = encoded
	}
	assert.JSONEq(t, `{
  "name":"z_required","description":"Required input","type":"string",
  "required":true,"nullable":true,"sensitive":false,"has_default":false,
  "default":null,"default_redacted":false
}`, string(byName["z_required"]))
	assert.JSONEq(t, `{
  "name":"nullable_value","description":null,"type":"number",
  "required":false,"nullable":true,"sensitive":false,"has_default":true,
  "default":null,"default_redacted":false
}`, string(byName["nullable_value"]))
	assert.JSONEq(t, `{
  "name":"non_nullable_value","description":null,"type":"string",
  "required":false,"nullable":false,"sensitive":false,"has_default":true,
  "default":"fallback","default_redacted":false
}`, string(byName["non_nullable_value"]))
	assert.JSONEq(t, `{
  "name":"secret_value","description":null,"type":"string",
  "required":false,"nullable":true,"sensitive":true,"has_default":true,
  "default":null,"default_redacted":true
}`, string(byName["secret_value"]))
	assert.JSONEq(t, `{"a":1,"b":2}`, string(defaultFor(t, decoded.Variables, "map_value")))
	assert.JSONEq(t, `["one","two"]`, string(defaultFor(t, decoded.Variables, "set_value")))
	assert.JSONEq(t, `{
	  "a_optional":null,"label":"fallback","nested":{"enabled":true},"z_required":"present"
}`, string(defaultFor(t, decoded.Variables, "object_value")))
}

func TestInspectRedactsSensitiveOptionalAttributeDefaults(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeConfig(t, directory, "variables.r42.hcl", `
variable "credentials" {
  type = object({
    token = optional(string, "never-print-this-secret")
  })
  sensitive = true
}
`)

	document, err := variableschema.Inspect(t.Context(), directory)
	require.NoError(t, err)
	encoded, err := variableschema.Marshal(document)
	require.NoError(t, err)

	assert.NotContains(t, string(encoded), "never-print-this-secret")
	require.Len(t, document.Variables, 1)
	assert.Equal(t, "object({token=optional(string)})", document.Variables[0].Type)
}

func TestInspectSensitiveOptionalAttributeDefaultDiagnosticsDoNotLeakValues(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeConfig(t, directory, "variables.r42.hcl", `
variable "credentials" {
  type = object({
    token = optional(number, "never-print-this-secret")
  })
  sensitive = true
}
`)

	_, err := variableschema.Inspect(t.Context(), directory)

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "never-print-this-secret")
}

func TestInspectPreservesOptionalAttributeDefaultsThroughContainerTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		typeExpr string
		expected string
	}{
		{
			name:     "list",
			typeExpr: `list(object({ enabled = optional(bool, true) }))`,
			expected: `list(object({enabled=optional(bool,true)}))`,
		},
		{
			name:     "set",
			typeExpr: `set(object({ enabled = optional(bool, true) }))`,
			expected: `set(object({enabled=optional(bool,true)}))`,
		},
		{
			name:     "map",
			typeExpr: `map(object({ enabled = optional(bool, true) }))`,
			expected: `map(object({enabled=optional(bool,true)}))`,
		},
		{
			name:     "tuple",
			typeExpr: `tuple([string, object({ enabled = optional(bool, true) })])`,
			expected: `tuple([string,object({enabled=optional(bool,true)})])`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			directory := t.TempDir()
			writeConfig(t, directory, "variables.r42.hcl", `variable "input" { type = `+test.typeExpr+` }`)

			document, err := variableschema.Inspect(t.Context(), directory)
			require.NoError(t, err)
			require.Len(t, document.Variables, 1)
			assert.Equal(t, test.expected, document.Variables[0].Type)
		})
	}
}

func TestInspectRejectsInvalidVariableDeclarations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		source        string
		expectedError string
	}{
		{name: "missing type", source: `variable "input" {}`, expectedError: `variable "input" must declare type`},
		{name: "invalid type", source: `variable "input" { type = list() }`, expectedError: "Invalid type specification"},
		{name: "incompatible default", source: `variable "input" {
  type    = number
  default = true
}`, expectedError: `variable "input" default is incompatible with number`},
		{name: "invalid description", source: `variable "input" {
  type        = string
  description = 1
}`, expectedError: `variable "input" description must be a known string`},
		{name: "invalid nullable", source: `variable "input" {
  type     = string
  nullable = "true"
}`, expectedError: `variable "input" nullable must be a known bool`},
		{name: "invalid sensitive", source: `variable "input" {
  type      = string
  sensitive = "true"
}`, expectedError: `variable "input" sensitive must be a known bool`},
		{name: "duplicate variable", source: `variable "input" { type = string }
variable "input" { type = string }`, expectedError: `variable "input" is declared more than once`},
		{name: "syntax error", source: `variable "input" { type = }`, expectedError: "Invalid expression"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			directory := t.TempDir()
			writeConfig(t, directory, "variables.r42.hcl", test.source)

			_, err := variableschema.Inspect(t.Context(), directory)

			require.Error(t, err)
			assert.ErrorContains(t, err, test.expectedError)
		})
	}
}

func TestInspectSensitiveDefaultErrorsDoNotLeakValues(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeConfig(t, directory, "variables.r42.hcl", `
variable "token" {
  type      = number
  sensitive = true
  default   = "never-print-this-secret"
}
`)

	_, err := variableschema.Inspect(t.Context(), directory)

	require.Error(t, err)
	require.ErrorContains(t, err, `variable "token" default is incompatible with number`)
	assert.NotContains(t, err.Error(), "never-print-this-secret")
}

func TestInspectReportsNonSensitiveDefaultDiagnostics(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeConfig(t, directory, "variables.r42.hcl", `
variable "input" {
  type    = string
  default = var.other
}
`)

	_, err := variableschema.Inspect(t.Context(), directory)

	require.Error(t, err)
	assert.ErrorContains(t, err, "Variables not allowed")
}

func TestMarshalRejectsInvalidProtocolValues(t *testing.T) {
	t.Parallel()

	_, err := variableschema.Marshal(variableschema.Document{
		SchemaVersion: variableschema.Version,
		Variables: []variableschema.Variable{{
			Name:    "broken",
			Default: json.RawMessage(`{`),
		}},
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "encode variable schema")
}

func writeConfig(t *testing.T, directory, name, source string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(source), 0o600))
}

func variableNames(variables []protocolVariable) []string {
	names := make([]string, len(variables))
	for index := range variables {
		names[index] = variables[index].Name
	}
	return names
}

func defaultFor(t *testing.T, variables []protocolVariable, name string) json.RawMessage {
	t.Helper()
	for _, variable := range variables {
		if variable.Name == name {
			return variable.Default
		}
	}
	require.FailNow(t, "variable not found", name)
	return nil
}
