# Root Variable Schema Command

Status: implemented.

## 1. Purpose

`r42 schema --json` exposes the input-variable contract of an initialized r42
template in a machine-readable form. Its consumers include nib, which uses the
contract to generate run forms, identify unsatisfied required inputs, display
safe defaults, validate presets, and support custom input values.

r42 owns this contract so consumers do not need to reproduce r42's HCL, cty,
default-value, or sensitivity semantics.

The command is run from the directory containing the initialized project:

```text
template.zip
  -> unpack template
  -> r42 init <template directory>
  -> r42 schema --json
```

## 2. Scope and Execution Boundary

The command reads the root configuration directory recorded in `.r42/config`.
It returns only `variable` blocks declared in that directory. Variables from
child modules are deliberately excluded.

Schema inspection is static. It does not require values for required variables,
does not execute Plan or Apply, and does not reserve a run directory. It must
not start a Copilot session, compile or call a typed tool, invoke an external
program, or start a Starlark worker.

The command validates HCL syntax, variable declarations, declared types, and
defaults using the same Golden/r42 variable semantics used by normal
configuration loading. It parses the syntax of HCL `validation` blocks but does
not evaluate their conditions without a concrete input value or convert them
into frontend rules. It does not inspect nib presets or secrets, or validate a
concrete run's complete input set. `r42 plan` remains the final input validator.

## 3. Command I/O Contract

The initial command form is:

```text
r42 schema --json
```

On success, stdout contains exactly one JSON document and no human-oriented
text. Diagnostics and warnings are written only to stderr. A configuration
error returns a non-zero exit status with clear diagnostics.

Repeated invocations for identical configuration must produce byte-identical
stdout. Variables are sorted by name; object attributes in type expressions
are also sorted by name. JSON is encoded consistently without HTML escaping.

## 4. Versioned JSON Protocol

The schema protocol starts at version 1:

```json
{
  "schema_version": 1,
  "variables": []
}
```

`schema_version` is a compatibility contract. A later version may add optional
fields to version 1 output, but it must not remove a version 1 field or change
its established meaning. A breaking protocol change requires a new schema
version.

The complete version 1 variable record is:

```json
{
  "name": "language",
  "description": "Output language.",
  "type": "string",
  "required": false,
  "nullable": true,
  "sensitive": false,
  "has_default": true,
  "default": "zh-CN",
  "default_redacted": false
}
```

Every listed field is always present. `description` is `null` when no
description was declared. `default` is `null` when `has_default` is false or
when the declared default is redacted. `default_redacted` is false except for
a sensitive variable that declares a default.

The representation intentionally distinguishes these cases:

| Declaration state | `has_default` | `default` | `default_redacted` |
| --- | ---: | --- | ---: |
| no `default` attribute | false | `null` | false |
| non-sensitive `default = null` | true | `null` | false |
| non-sensitive non-null default | true | encoded declared value | false |
| sensitive variable with any default | true | `null` | true |

## 5. Variable Semantics

`required` is defined mechanically as:

```text
required = !has_default
```

`nullable` is distinct from `required` and preserves r42's input semantics:

- An omitted `nullable` declaration defaults to `true`, matching Terraform.
- `required: true` requires a caller-provided non-null value.
- For a non-required variable, `nullable: true` allows a caller to explicitly
  set the variable to `null`.
- For a non-required variable with `nullable: false`, an explicit `null` uses
  the default value. If no default exists, the final `r42 plan` input
  validation fails.
- `required: true` takes precedence over `nullable`: a required variable never
  accepts `null`, even if its declaration says `nullable = true`.

Schema inspection does not itself accept or reject a particular input value;
it reports the declaration contract needed by a caller to prepare that input.

## 6. Type Representation

`type` is a normalized HCL type-constraint expression encoded as a JSON string.
It is intended to be parsed by consumers using HCL's `typeexpr` package, not
by an r42-specific type parser. The expression must preserve all cty structure
that r42 accepts, including optional object attributes.

Examples:

```json
{
  "type": "string"
}
```

```json
{
  "type": "object({ api_key_ref = optional(string), endpoint = string })"
}
```

Version 1 supports normalized expressions for these cty forms:

- `string`
- `number`
- `bool`
- `list(<element type>)`
- `set(<element type>)`
- `map(<element type>)`
- `tuple([<element type>, ...])`
- `object({ <attribute> = <attribute type>, ... })`
- `optional(<attribute type>)` for an object attribute without a declared default
- `optional(<attribute type>, <default>)` for an object attribute with a declared default

The normalized formatter is semantic rather than source-preserving. Whitespace,
line breaks, and original attribute order from the source must not affect the
output. Object attribute names are emitted in lexical order. Strings and
attribute identifiers are rendered in a form accepted by HCL type expressions.
HCL `typeexpr` requires object keys to be valid attribute identifiers, so
declarations using other key forms are rejected during type validation.

Optional attribute defaults are normalized as HCL-compatible constant
expressions and retained in `type`. For example, `optional(string, "fallback")`
remains `optional(string,"fallback")`; it is not reduced to
`optional(string)`.

## 7. Defaults and Sensitive Values

Defaults are evaluated and type-checked using Golden/r42's established
declaration semantics. A non-sensitive default is encoded as JSON according to
the variable's declared cty type, preserving primitive values, collections,
tuples, objects, and explicit `null`.

For `sensitive = true`, metadata remains visible: `name`, `description`,
`type`, `required`, `nullable`, `sensitive`, and `has_default`. The default
value itself must never be returned. When such a variable declares a default,
the response sets `default: null` and `default_redacted: true`.

Sensitive default material must also never appear in diagnostics. Error paths
must report the variable name and the validation category without interpolating
the default's evaluated value or source text. This applies to optional object
attribute defaults inside `type` as well: for a sensitive variable, the type
retains the optional attribute marker but omits its default argument.

## 8. Example

```json
{
  "schema_version": 1,
  "variables": [
    {
      "name": "language",
      "description": null,
      "type": "string",
      "required": false,
      "nullable": true,
      "sensitive": false,
      "has_default": true,
      "default": "zh-CN",
      "default_redacted": false
    },
    {
      "name": "provider",
      "description": "Model provider configuration.",
      "type": "object({ api_key_ref = optional(string), endpoint = string })",
      "required": true,
      "nullable": true,
      "sensitive": true,
      "has_default": false,
      "default": null,
      "default_redacted": false
    },
    {
      "name": "topic",
      "description": "The topic to research.",
      "type": "string",
      "required": true,
      "nullable": true,
      "sensitive": false,
      "has_default": false,
      "default": null,
      "default_redacted": false
    }
  ]
}
```

## 9. Acceptance Criteria

The implementation is complete when tests demonstrate all of the following:

- Missing required values do not prevent `r42 schema --json` from succeeding.
- All and only root variables are returned; child-module variables are absent.
- Every supported cty type, including optional object attributes, round-trips
  through the normalized HCL type-expression protocol.
- Non-sensitive defaults are encoded according to their declared type.
- Sensitive default content appears in neither stdout nor stderr.
- Repeated execution emits byte-identical JSON.
- The command does not reach Plan, Apply, model-session, tool-compilation,
  tool-invocation, external-program, or Starlark-worker paths.
- Invalid syntax, declarations, types, or defaults return a non-zero exit and
  explicit diagnostics.
