# r42 Research DAG DSL Design

Status: current contract plus approved `phase_mode`/`starlark_tool` and
`--ui=jsonl` targets

This document is the normative r42 execution contract. The `phase_mode`,
`starlark_tool`, and revised SecJury sections describe the approved target that
is not implemented yet; their delivery sequence is tracked in
[phase-modes-starlark-development-plan.md](phase-modes-starlark-development-plan.md).
The `--ui=jsonl` machine-progress target is tracked in
[jsonl-progress-implementation.md](jsonl-progress-implementation.md).
When this document and an example disagree, the explicit rules here win.

## 1. Purpose

r42 is a high-level HCL DSL and execution engine for research DAGs. Azure/golden
provides configuration evaluation, implicit dependency discovery, Plan hooks,
and the Apply block protocol. A leaf `research` block owns the persistent
sessions selected by its `phase_mode` and, when that mode permits it and `qc` is
configured, one persistent Final QC session.

The Go implementation must use the official upstream
`github.com/github/copilot-sdk/go` module directly. It must not introduce a
third-party fork or adapter.

### Goals

- Describe static research DAGs with typed inputs, outputs, tools, and artifacts.
- Produce an immutable Plan before Apply starts.
- Keep every selected workflow phase session persistent for the lifetime of one
  block.
- Support reusable, statically planned modules with Terraform-like variables and
  outputs.
- Run independent research blocks concurrently within global and module limits.
- Make model-repairable failures visible to the model without hiding
  infrastructure failures.

### Non-goals for the first version

- Mutating a DAG after Plan.
- Creating a module directory during Apply or planning a module dynamically.
- Resuming an interrupted Apply or reusing successful blocks from an older run.
- Hashing skills, tools, programs, or external files into the Plan.
- Sandboxing paths, child-process environments, or `..`/absolute path access.
- Go plugins or Python inline tools.
- Modifying Golden's scheduler to implement r42 concurrency controls.
- Encrypting `.r42plan` files or enforcing plan-format compatibility gates.

## 2. Execution Model

The lifecycle is:

```text
runtime.Config -> ResearchConfig -> RunResearchPlan -> ResearchPlan
runtime.ConfigFromPlan -> ResearchConfig -> ResearchPlan
ResearchPlan.Apply -> ResearchConfig.Outputs / Warnings
```

Plan recursively parses every referenced module directory. Apply consumes the
saved nested Plans and never reparses module source. A parameterless `r42 apply`
performs a complete in-memory Plan of the initialized configuration snapshot
before Apply.

Golden owns source-configuration graph construction and dependency evaluation.
References create implicit dependencies. `depends_on` is available for
dependencies that cannot be expressed through values, matching Terraform's
model. r42 serializes the resulting node addresses and dependency lists into its
immutable Plan.

The pinned Golden version mutates its live block graph during Plan and exposes
no Plan serialization or nested executor API. r42 owns the immutable
serializable Plan and persists nested Plans. `RunResearchPlan` delegates source
planning to `ResearchConfig.RunPlan()` and freezes the result. The CLI retains
that Config; `loadOrPlan` returns either the source Config or a Config restored
from a saved plan. `ResearchPlan.Apply` therefore reads its Apply behavior from
the owning Config instead of accepting another runtime options object.

Apply reconstructs saved nodes as native `research` and `module` blocks and
runs Golden Plan to decode and validate them without running any Apply work. The
r42 saved-plan scheduler then uses the immutable node dependency lists and
configured parallelism to invoke `ApplyBlock.Apply()` for ready blocks. This
separation is deliberate: `ExecuteDuringPlan` validates and snapshots, while
`Apply()` is the only entry point for Copilot sessions, typed tool processes,
artifact mutation, and nested-module execution. Apply never reparses module
source.

Any block failure or timeout triggers fail-fast cancellation of the entire DAG.
An interrupted Apply cannot resume; another Apply creates a new run and starts
from the DAG roots.

## 3. Blocks and Values

The first version has these root-level declarations:

- `model_provider`: model API transport and retry policy, but not a model name.
- `s3_provider`: S3-compatible object storage endpoint, credentials, and upload
  retry policy.
- `s3_folder`: a fail-fast folder upload with best-effort rollback.
- `mcp_server`: a native Copilot SDK MCP server with explicit tool and resource
  declarations.
- `go_tool`: a typed tool implemented by inline Go source.
- `external_tool`: a typed tool implemented by a child process.
- `starlark_tool`: an isolated, resource-bounded numerical scratchpad.
- `research`: a `full`, `collection_only`, or `research_only` workflow, with
  optional Final QC where the selected mode permits it.
- `module`: a statically planned child directory.
- `variable`: a module input.
- `locals`: Plan-time names for derived expressions.
- `output`: a module output.

There is no root `r42 {}` block. Global parallelism and overall timeout are CLI
settings.

## 4. End-to-end Example

```hcl
model_provider "primary" {
  type      = "azure"
  endpoint  = "https://example.openai.azure.com"
  wire_api  = "responses"
  transport = "http"

  api_key_ref = "AZURE_OPENAI_API_KEY"

  headers = {
    "x-project" = env("R42_PROJECT")
  }

  retry {
    lifecycle_retries  = 10
    model_call_retries = 5
    interval_seconds     = 10
    max_interval_seconds = 180
    error_message_regex  = ["temporarily unavailable"]
  }
}

external_tool "search_catalog" {
  description = "Search the local research catalog"
  program     = ["catalog-search", "--json"]

  input_type = object({
    query = string
    limit = optional(number, 20)
  })

  output_type = object({
    matches = list(object({
      title = string
      path  = string
    }))
  })
}

go_tool "finish" {
  description = "Submit the final research result"
  source = <<-GO
    import "context"

    type Input struct {
        Summary string `json:"summary"`
    }

    type Output string

    func Invoke(ctx context.Context, input Input) (ToolResponse[Output], error) {
        output := Output(input.Summary)
        return ToolResponse[Output]{Accepted: true, Output: &output}, nil
    }
  GO
}

research "static" "market" {
  model_provider  = model_provider.primary
  model           = "gpt-5.6-sol"
  reasoning_effort = "max"
  system_prompt   = "Act as a rigorous market researcher."
  prompt          = "Research ${var.topic}."
  timeout         = "2h"

  collection_tool_ids = [
    external_tool.search_catalog.id,
  ]
  terminate_tool_id = go_tool.finish.id

  allowed_tools = [
    "web_search",
    "web_fetch",
    tool_name(external_tool.search_catalog),
    tool_name(go_tool.finish),
  ]
  disallowed_tools = ["ask_user"]
  permission       = "approve_all"

  skill_directories = ["${path.module}/skills"]
  skills            = ["source-evaluation"]
  disabled_skills   = []

  max_protocol_attempts = 10

  artifact "report" {
    type      = "file"
    path      = "report.md"
    required  = true
    non_empty = true
  }

  qc {
    criteria = {
      factual_accuracy = "Every factual claim must be supported."
      completeness     = "All requested topics must be covered."
    }
    max_qc_rounds = 10
  }
}

output "summary" {
  value     = research.static.market.result
  sensitive = false
}

output "report_path" {
  value = one([for item in research.static.market.artifact : item if item.name == "report"]).path
}
```

The example fixes the public shape of the DSL. Provider-specific combinations
may be narrowed by Plan validation as the SDK and provider APIs evolve.

Every nested block keeps its singular HCL block name and is exposed as
`list(object)`, following Terraform and Golden conventions. This applies to
`model_provider.retry`, `research.static.<name>.retry`,
`research.static.<name>.artifact`, `research.static.<name>.qc`, and
`research.static.<name>.qc[*].retry`; r42 does not remap labeled nested blocks into
name-keyed objects.

### Dynamic research

`research "dynamic" "name"` is one Golden DAG node whose `tasks` attribute is a
list of research configuration objects. Unlike `for_each`, task members are not
planned DAG nodes and therefore do not acquire independent DAG addresses. r42
materializes the complete list at the start of the block's Apply operation,
runs its members under the same research concurrency scope as static blocks,
and publishes the completed list as `research.dynamic.<name>.tasks`.

The optional `serial` attribute defaults to `false`. When true, the block runs
its materialized members one at a time in list order while each member still
acquires the shared research concurrency scope. The setting does not serialize
unrelated ready DAG nodes.

The list may be unknown in a saved Plan. r42 preserves the cty unknown value and
the HCL expression; it does not require a narrower element type merely for plan
serialization. At Apply, the list must be wholly known before any member starts.
An empty list succeeds, while a member failure after retry and QC repair fails
the containing block and cancels its siblings.

Each member uses the static session fields described above. `artifacts` is a
list of artifact objects; `retry` and `qc` are each one object or `null`, not a
list. A completed member retains the original object fields and overlays the
resolved `artifacts` and optional terminate-tool `result`. All members share the
dynamic block's `block_wd()` and the HCL author is responsible for adding an
index or key when task files need separate directories.

Static and dynamic research blocks expose a plan-known `path` attribute. For a
static block it is the block workspace returned by `block_wd()`. For a dynamic
block it is the parent workspace shared by its materialized task directories.
The path is available for downstream references such as `s3_folder.source` and
creates the normal implicit DAG dependency when referenced.

### S3-compatible artifact upload

`s3_provider` is a configuration-only root block. `s3_folder` is an executable
root block that uploads a local directory into one bucket and key prefix. A
relative `source` is resolved below the active run root
(`.r42/runs/<run-id>`); an absolute source is accepted only when its cleaned
path remains below that same run root. A research `path` reference is absolute
and is therefore the normal way to upload research output.

The folder walk includes hidden files and supports `**` exclude matching,
and skips symbolic links and special files. An empty directory after excludes
is a successful no-op. Object keys always use `/` separators and are formed by
joining the validated prefix with the relative file path.

Uploads are streamed and may use multipart transfer. Files are uploaded in a
deterministic order. The first failed PUT or multipart operation stops further
uploads. When the target bucket has versioning `Enabled`, previously uploaded
objects are deleted in reverse order by the exact version created by each PUT.
For an unversioned or suspended bucket, automatic rollback is not attempted:
deleting by key could destroy an older object that this block does not own.
The block still fails and its error identifies the local source root and remote
root (`s3://<bucket>/<prefix>`) so an administrator can clean up manually.
Existing objects may be overwritten.

## 5. Model Providers

`model_provider` contains endpoint and wire configuration only. The `model`
belongs to each session configuration.

Provider fields:

- `type` is required and is one of `openai`, `azure`, or `anthropic`.
- `endpoint` identifies the model API endpoint.
- `wire_api` is one of `completions` or `responses`.
- `transport` is one of `http` or `websockets`.
- Unsupported provider/wire/transport combinations fail during Plan.
- `headers` is `map(string)`. Environment-derived values use `env()`.

At most one authentication field may be set; all may be absent when the
environment or endpoint does not require one:

- `api_key`: literal API key.
- `api_key_ref`: name of an environment variable read during Apply.
- `bearer_token`: literal bearer token.
- `bearer_token_ref`: name of an environment variable read during Apply.

Literal credentials and sensitive expressions are sensitive values. `env()` is
evaluated during Plan, so its actual result is stored in the Plan. A `*_ref`
stores the environment variable name and reads its value during Apply.

### Retry policy

The provider supplies defaults. A research block may override individual retry
fields, and QC inherits the effective research policy unless it overrides a
field.

- `lifecycle_retries` defaults to 10.
- `model_call_retries` defaults to 5.
- Both values count additional attempts; `0` disables retries.
- Initial retry interval defaults to 10 seconds.
- Maximum interval defaults to 180 seconds.
- Backoff multiplier is fixed at 1.5 and jitter is fixed at 0.5.
- `error_message_regex` adds transient classifications and never removes built-in
  classifications.

Built-in transient classes include temporary SDK/runtime errors, HTTP 408, 409,
425, 429 and 5xx responses, network timeouts, connection reset/refused errors,
and transient EOF/transport failures. HTTP 400, 401, and 403, cancellation,
deadline expiry, authentication failures, and invalid model/schema errors are
permanent. In particular, HTTP 400 fails immediately rather than consuming ten
retries.

All retry delays respect the active context deadline.

## 6. Session Configuration

Every `research` block is one workflow. `phase_mode` selects which logical
sessions exist:

- `full` is the default and preserves the Collection, Collection QC, closed
  Research, and optional Final QC workflow.
- `collection_only` runs one Collection session and completes through its
  terminating `tool_use`. It is intended for tasks that must acquire data and
  calculate against it in one evolving context.
- `research_only` starts directly in closed Research and may optionally run
  Final QC. It is intended for downstream work over already frozen inputs.

Static research and every materialized dynamic task support all three modes.
They use the same coordinator, but every dynamic member owns isolated sessions,
artifact registry, quotas, protocol-attempt budget, and, in `full` mode,
information-need and Collection-round state.

Research session fields include:

- `phase_mode`: optional `full`, `collection_only`, or `research_only`;
  omission means `full`.
- `model_provider`: optional provider reference; omission uses the SDK's
  default provider behavior. It is the default provider for all workflow
  phases.
- `model`: required.
- `profile`: optional Copilot runtime model identity, defaulting to `model`.
  With BYOK, r42 sends `profile` as `ProviderConfig.ModelID` for capability and
  built-in-tool selection, while `model` remains `ProviderConfig.WireModel` and
  is sent to the inference provider.
- `reasoning_effort`: an arbitrary non-empty string passed through unchanged.
- `system_prompt`: required.
- `prompt`: optional.
- `final_qc_strictness`: optional Final-QC semantic policy, one of `strict`,
  `balanced`, or `brief`; omission defaults to `balanced`. The configured policy
  is authoritative if a later task prompt, candidate instruction, or custom
  criterion conflicts with it.
- `collection_model_provider`: optional Collection-only provider override;
  omission reuses `model_provider`.
- `collection_tool_ids`: typed acquisition or snapshot-producing tool IDs used
  only by Collection.
- `collection_mcp_tool_ids`: native MCP tool IDs from
  `mcp_server.<name>.tool_ids`, attached only to Collection.
- `collection_mcp_resource_ids`: declared MCP resource IDs from
  `mcp_server.<name>.resource_ids`, attached only to Collection. A session with
  one or more selected resources automatically receives the typed
  `r42_read_mcp_resource` reader; it can read only those declared IDs.
- `collection_allowed_builtin_tools`: built-in tool names to remove from
  Collection's fixed denylist only. It applies in `full` and
  `collection_only`.
- `collection_qc_allowed_builtin_tools`: built-in tool names to remove from
  Collection QC's fixed denylist only. It applies in `full`.
- `tool_ids`: trusted typed tool IDs used only by closed Research.
- `research_allowed_builtin_tools`: built-in tool names to remove from
  Research's fixed denylist only. It applies in `full` and `research_only`.
- `final_qc_allowed_builtin_tools`: built-in tool names to remove from Final
  QC's fixed denylist only. It applies when Final QC exists in `full` or
  `research_only`.
- `tool_call_quota`: optional call limits. Keys may name configured Collection
  or Research typed tools, the terminate tool, or Copilot built-ins. Collection
  and Research maintain separate counters.
- `terminate_tool_id`: optional typed tool ID.
- `allowed_tools` and `disallowed_tools`: SDK tool-name strings for ordinary
  tools and generated `mcp_tool_...` IDs for MCP tools.
- `collection_skill_directories`, `collection_skills`, and
  `collection_disabled_skills`, used only by Collection.
- `skill_directories`, `skills`, and `disabled_skills`, used only by Research.
- `collection_batch_size`, defaulting to 10.
- `max_collection_rounds`; omission uses the default hard cap of 10 rounds.
- zero or one `collection_qc` block. Collection QC is still mandatory when the
  block is absent; the block only overrides criteria, provider/model/reasoning,
  retry, and permission.
- `permission`, defaulting to `approve_all`.
- `max_protocol_attempts`, defaulting to 10.
- `timeout`, with no default.
- retry overrides.

The mode-specific configuration contract is:

| Configuration | `full` | `collection_only` | `research_only` |
| --- | --- | --- | --- |
| `collection_tool_ids`, `collection_mcp_tool_ids`, `collection_mcp_resource_ids`, `collection_model_provider`, Collection skills | Collection | Sole session | Forbidden |
| `collection_allowed_builtin_tools` | Collection | Sole session | Forbidden |
| `collection_qc_allowed_builtin_tools` | Collection QC | Forbidden | Forbidden |
| `tool_ids`, `terminate_tool_id`, Research skills | Research | Forbidden | Research |
| `research_allowed_builtin_tools` | Research | Forbidden | Research |
| `final_qc_allowed_builtin_tools` | Final QC | Forbidden | Final QC |
| `tool_use` | Research | Sole session | Research |
| `collection_batch_size`, `max_collection_rounds`, `collection_qc` | Allowed | Forbidden | Forbidden |
| `qc` | Optional | Forbidden | Optional |
| Information-needs and checkpoint protocol | Required | Not created | Not created |

`collection_only` requires one or more `tool_use` blocks and exactly one with
`terminate = true`. Other non-terminating tool uses are allowed. It rejects
`tool_ids` and `terminate_tool_id`; the terminal contract must be explicit so
HCL-owned arguments, agent-owned arguments, validation, and declared artifacts
remain available. `full` and `research_only` retain the existing choice between
`terminate_tool_id` and `tool_use`.

Static and dynamic syntax use the same string field:

```hcl
research "static" "builder" {
  phase_mode = "collection_only"
  # ...
}

research "dynamic" "jurors" {
  tasks = [
    for juror in local.jurors : {
      id         = juror.id
      phase_mode = "research_only"
      # ...
    }
  ]
}
```

r42 prepends a fixed protocol for each session that exists in the selected
mode. In `full`, the optional `prompt` starts both the initial Collection turn
and the initial Research turn. In either single-phase mode it starts the sole
initial turn. When it is absent, r42 sends the applicable fixed start message.
r42 does not validate whether a model supports a given reasoning effort;
unsupported parameters are surfaced by the provider, with HTTP 400 failing
immediately.

Provider selection is resolved per phase. Collection uses
`collection_model_provider` when present, Collection QC uses its nested
`model_provider` when present, and Final QC uses its nested `model_provider`
when present. Every omitted phase override reuses the research block's
top-level `model_provider`; closed Research always uses that top-level provider.
The sole `collection_only` session follows Collection provider selection, while
the initial `research_only` session follows Research provider selection.
Retry composition starts from the selected phase provider, then applies the
research-level retry override and, for either QC phase, its nested retry
override.

### Tool policy

There is no default allowlist. When present, `allowed_tools` first narrows the
available set. `disallowed_tools` is then applied and always wins.
`*_allowed_builtin_tools` does not participate in the allowlist: it may name
only a built-in that is in that phase's fixed denylist, and removes that name
from the fixed portion for that one session. An explicit `disallowed_tools`
entry still wins. Collection in `full`, and the sole session in
`collection_only`, deny by default `bash`, `powershell`, `read_powershell`,
`list_powershell`, `shell`, `edit`, `create`, `glob`, `task`, `ask_user`, and
`curl`. Collection QC, Research, and Final QC deny by default `web_search`,
`web_fetch`, `bash`, `powershell`, `read_powershell`, `list_powershell`,
`shell`, `edit`, `create`, `glob`, `task`, and `ask_user`. Read-only `view`,
`grep`, `head`, and `tail` file tools remain available. Explicit custom typed
tools remain an author trust boundary.

`approve_all` automatically approves every otherwise valid tool call.
Registration, checkpoint, terminate, and verdict tools are protocol tools. r42
registers only those applicable to the selected mode, and configuration cannot
exclude them. `collection_only` has registration and save-artifact helpers but
does not receive information-needs, checkpoint, or QC-verdict tools.

An `mcp_server` requires a non-empty explicit `tools` list and exactly one
transport. `resources` is an optional list of exact resource URIs. HTTP maps
`url`, optional `headers`, `bearer_token_ref`, tools, and timeout to
`sdk.MCPHTTPServerConfig`. Stdio maps `command`, `args`, literal `env`,
environment-backed `env_refs`, `working_directory`, tools, and timeout to
`sdk.MCPStdioServerConfig`. The SDK owns protocol negotiation; r42 uses its
experimental host-side resource read RPC only through the generated typed reader.
The configured label is display-only for runtime routing: every planned MCP
server receives its canonical block address as its runtime name. Native MCP
tool filters and typed resource reads use that same canonical name, so identical
labels in different module paths remain separate SDK servers.

MCP IDs use `mcp_tool_<server>__<tool>_<uuid>` and
`mcp_resource_<server>__<uri>_<uuid>` and are separate from the typed tool
registry. Tool IDs select tools through `collection_mcp_tool_ids` and may appear
in the shared filters. Resource IDs select resources through
`collection_mcp_resource_ids` and are forbidden in tool filters. Neither can be
attached to Collection QC, Research, or Final QC or used by `tool_use`,
`terminate_tool_id`, or typed-tool quotas.

When `allowed_tools` is configured, MCP availability is the intersection of
that allowlist and `collection_mcp_tool_ids`. r42 converts only those explicit
MCP IDs to the SDK's `mcp:<server>-<tool>` filter names; it does not implicitly
allow every connected MCP tool. Omitted `allowed_tools` leaves the SDK allowlist
unset. Protocol-mandatory r42 tools are appended independently so a user filter
cannot break checkpoint, artifact, terminal, or verdict protocols.
When a Collection session selects resources, r42 likewise appends
`r42_read_mcp_resource` to any non-nil SDK allowlist and removes it from the
denylist. The typed reader accepts only the session's declared resource IDs.

Each typed tool receives a deterministic, SDK-safe ID from its canonical block
address. Workflow snapshots store only those IDs; the Plan stores the complete
ID-to-definition registry used during Apply. String-only SDK filter fields can
use the `id` attribute directly or the compatibility function:

```hcl
go_tool.finish.id
tool_name(go_tool.finish) # same generated tool_go_tool_finish_<uuid> value
```

Tools declared in a module remain private unless the module exposes the tool ID
as a direct string output. A parent can pass that output to
`collection_tool_ids` or `tool_ids`; Plan then imports only the corresponding
exported definition into the parent registry.

Collection, Research, and Final QC configure typed-tool quotas independently. A
successful call consumes one unit only after its arguments pass schema
validation and the tool returns an accepted response. Execution errors and
`accepted = false` responses roll back the reservation. A zero quota disables
that tool for the session. In `collection_only`, `tool_call_quota` belongs to
the sole Collection session. Collection rounds are a separate `full`-mode
control: each `needs_more` return to Collection after the initial phase consumes
a round, and an empty checkpoint does not consume an extra round.

`one(collection)` follows Terraform's zero-or-one convention: an empty list,
set, or tuple returns null, one element returns that element, and more than one
element is an error.

The generated ID is also the SDK registration name. It has the form
`tool_<go_tool|external_tool|starlark_tool>_<name>_<uuid>`, is at most 64
characters, and is derived from the canonical address so module instances
cannot collide.

### Working directory

`cwd()` returns the r42 process working directory at expression evaluation
time, equivalent to `os.Getwd()`. It does not return the directory containing a
`*.r42.hcl` file. The result always uses `/` path separators, including on
Windows, so it can be interpolated into paths consistently:

```hcl
path = "${cwd()}/research-output/report.md"
```

`block_wd()` takes no arguments and returns the absolute workspace assigned to
the current block. Plan evaluates the same deterministic value without creating
the directory; Apply creates the workspace before the block runs. Each expanded
block address receives a different path, and the result uses `/` separators on
every operating system.

`path.module` is the absolute directory containing the current block's
initialized `*.r42.hcl` source. It points to the root configuration directory
for root blocks and the canonical `.r42/modules/...` directory for child-module
blocks. Use `path.module` for source-owned files and `block_wd()` for files
created by one task.

### Skills

r42 uses Copilot's native skill mechanism. The evaluated `skill_directories`
strings go into `SkillDirectories`, and selected skill names go into the custom
agent configuration. r42 records those strings in the Plan but does not copy,
hash, or check the directories during Plan. Authors should use
`"${path.module}/skills"` for module-owned skills so the SDK receives a stable
absolute path. The directories and their `SKILL.md` files must be readable when
Apply opens the session. With no allowlist, a configured skill is usable unless
disabled or excluded by tool policy.

If the SDK exposes a native session feature needed for these fields, r42 uses
it; unsupported optional SDK features are not emulated unless required by the
protocol above.

## 7. Typed Tool Contract

All r42 typed tools return the same logical envelope:

```go
type Issue struct {
    Code       string  `json:"code"`
    Message    string  `json:"message"`
    Path       *string `json:"path,omitempty"`
    RepairHint *string `json:"repair_hint,omitempty"`
}

type ToolResponse[T any] struct {
    Accepted bool    `json:"accepted"`
    Output   *T      `json:"output,omitempty"`
    Issues   []Issue `json:"issues,omitempty"`
}
```

Invariants:

- `accepted = true`: `issues` is empty; `output` is optional.
- `accepted = false`: at least one issue is present; `output` is absent.
- Partial output together with issues is invalid.
- A business or argument rejection uses `accepted = false` and is returned to
  the calling session for repair.
- Process startup, protocol corruption, cancellation, panic, I/O, and other
  infrastructure failures fail the block.

Plan rejects dynamic, capsule, `any`, or finally unknown tool types. Supported
cty shapes are string, number, bool, list, set, map, tuple, object, and optional
object attributes. JSON conversion must preserve the declared shape and reject
values that cannot be represented by the implementation type.

### Inline `go_tool`

`source` is a Go file fragment without a `package` clause or `main` function. It
may import Go standard-library packages only and must declare:

```go
type Input ...
type Output ...
func Invoke(context.Context, Input) (ToolResponse[Output], error)
```

During Plan, r42 uses Go parser/AST/type checking to verify the declarations,
signature, imports, ToolResponse invariants that can be checked statically, and
cty compatibility. During Apply, r42 injects the package, shared response types,
and a JSON stdin/stdout `main`, then compiles an executable into an r42 temporary
directory. It does not run `go install` or download non-standard dependencies.

Compiled tools are cached within one r42 process, invoked by absolute path as a
fresh child process per call, and deleted best-effort before r42 exits.

### `external_tool`

Fields:

- `description`: required model-facing description.
- `program`: required non-empty `list(string)` containing executable and args.
- `working_dir`: optional; defaults to the calling research block workspace.
- `input_type`: required cty type constraint.
- `output_type`: required cty type constraint for `ToolResponse.output`.

There are no `inputs` or `inputs_default` fields. Defaults come only from
`optional(T, default)` in `input_type`. r42 fills missing optional defaults,
validates the complete LLM argument object, and writes exactly one JSON object to
stdin.

A zero exit code requires stdout to contain exactly one JSON document matching
`ToolResponse[output_type]`. Successful stderr is diagnostic output that is
persisted only in debug mode. A non-zero exit is an infrastructure error: stdout
is ignored and stderr is the error. Programs are not checked for existence or
executability during Plan.

Each call creates a new child process. A relative explicit `working_dir` is based
on the block workspace; authors use `path.module` to refer to source-module
files. Child processes inherit the complete r42 environment.

stdout and stderr are each limited to 100 MiB. Exceeding either limit terminates
the process and fails the block. Cancellation or timeout cancels I/O and
best-effort terminates the complete process tree. Cleanup warnings do not replace
the original error.

Without `--debug`, a failed tool includes at most the final 64 KiB of stderr in
the CLI diagnostic and does not persist full stderr. Debug mode persists it.

### Isolated `starlark_tool`

`starlark_tool` is a generic numerical scratchpad for LLM-generated programs.
It is not a DCF engine and has no business-specific input or output schema. It
uses `github.com/google/starlark-go` through the `go.starlark.net` module and is
declared as follows:

```hcl
starlark_tool "calculator" {
  description      = "Execute isolated numerical Starlark programs."
  max_steps        = 1000000
  timeout          = "5s"
  max_source_bytes = 65536
  max_data_bytes   = 1048576
  max_result_bytes = 1048576
  max_stdout_bytes = 16384
  memory_limit     = 134217728
}
```

The registered typed-tool input is fixed:

```json
{
  "code": "def main(data):\n    return stats.mean(data[\"values\"])\n\nresult = main(data)",
  "data_json": "{\"values\":[1,2,3]}"
}
```

Both fields are strings. `data_json` must contain exactly one valid JSON value;
callers that need no input pass `"null"`. The parsed value is converted to a
frozen Starlark value and predeclared as `data`. The script must assign a
top-level `result`. A successful response has this fixed output:

```json
{
  "result_json": "2.0",
  "stdout": ""
}
```

`result_json` is canonical JSON text rather than a statically declared business
type. A caller may pass it as a later call's `data_json`, or persist it through
an explicitly declared artifact. Every invocation is otherwise stateless.
JSON null, booleans, strings, arrays, and objects map to Starlark `None`, bool,
string, list, and dictionary values. Integral JSON numbers map to arbitrary
precision Starlark integers; other JSON numbers map to finite Starlark floats.
The reverse conversion follows the same distinction; arbitrary-precision
integers are emitted as base-10 JSON numbers in the returned JSON text.

r42 appends the following facts and links to the author-supplied model-facing
description so an agent can recover from unfamiliar syntax without guessing:

```text
Write Starlark, a deterministic Python-like language. Define top-level result
as a JSON-compatible value. Available host values: data, math, stats, matrix,
and fail. No import, load, filesystem, network, process, clock, or randomness
is available.

Exact implementation specification:
https://github.com/google/starlark-go/blob/master/doc/spec.md

Getting started and examples:
https://github.com/google/starlark-go

Go API reference:
https://pkg.go.dev/go.starlark.net/starlark

Secondary language introduction (may include Bazel-specific behavior):
https://bazel.build/rules/language
```

When a Collection session has an author-configured `web_fetch`, `pplx_fetch`,
or equivalent acquisition tool, the fixed Collection prompt permits temporary
access to those documentation URLs to resolve a Starlark syntax or API problem.
This does not inject a network tool into the session, bypass its configured
allowlist/quota, or give the Starlark worker network access. Documentation
lookups are operational help and need not be registered as business artifacts.
The exact `starlark-go` specification takes precedence; the Bazel introduction
is secondary because it can describe Bazel-specific behavior.

Each call launches the current r42 executable as a fresh hidden worker process.
The parent sends one request on stdin and receives one response on stdout. Code
and data never become temporary source files; diagnostics use the virtual name
`calculator.star`. The worker receives no arbitrary module loader and exposes
no filesystem, environment, network, subprocess, clock, randomness, or mutable
host object. Cancellation and timeout terminate the worker process tree.

Final QC always has a numerical calculator available. If the QC configuration
does not include a `starlark_tool`, r42 injects `r42_final_qc_calculator` with
the bounded defaults above (1,000,000 steps, 5 seconds, 64 KiB source, 1 MiB
input and result, 16 KiB stdout, and 128 MiB memory). If a QC configuration
already provides a Starlark tool, that configured tool is reused instead of
adding a second calculator.

The execution environment predeclares:

- `data`, the frozen value decoded from `data_json`;
- `math`, the read-only `starlark-go` math module;
- `stats`, a read-only module with `mean`, `median`, sample `variance`,
  population `pvariance`, sample `stdev`, population `pstdev`, and sample
  `covariance`;
- `matrix`, a read-only module with `shape`, `transpose`, and `matmul`;
- `fail`, which terminates evaluation with the supplied message.

The normal Starlark `print` function writes only to the worker's bounded
captured `stdout`; it never writes directly to the r42 terminal or tool protocol
stream.

The first version accepts only numeric Starlark `int` and `float` elements in
`stats` and `matrix`; `bool` is not numeric. It rejects non-finite values, empty
vectors, ragged or non-two-dimensional matrices, and matrix products with
incompatible dimensions or excessive output size. Sample variance, sample
standard deviation, and covariance require at least two observations;
covariance vectors must have equal length. `stats` and `matrix` are bundled
in-memory Starlark modules, rather than opaque Go loops, so their loops consume
the same execution-step budget as user code. The first version deliberately
omits inverse, regression, eigenvalue, SVD, finance, and DCF-specific helpers.

Only JSON-compatible `result` values are accepted: null, bool, finite number,
string, list/tuple, and dictionaries with string keys. Functions, sets, bytes,
non-string dictionary keys, cycles, NaN, and infinity are rejected. Output
validation happens before the accepted `ToolResponse` is returned.

Resource settings use these defaults and hard caps:

| Resource | Default | Hard cap |
| --- | ---: | ---: |
| Evaluation steps | 1,000,000 | 10,000,000 |
| Wall-clock timeout | 5 seconds | 30 seconds |
| Source bytes | 64 KiB | 256 KiB |
| Input data bytes | 1 MiB | 8 MiB |
| Result JSON bytes | 1 MiB | 8 MiB |
| Captured stdout bytes | 16 KiB | 64 KiB |
| Worker memory target | 128 MiB | 256 MiB |

Plan validates positive values, duration syntax, and hard caps. The worker calls
`Thread.SetMaxExecutionSteps`, reports `Thread.ExecutionSteps`, and uses
`Thread.Cancel` for timeout/cancellation. It also applies Go's memory limit and
reliable OS process limits where available. Go's memory limit is not a portable
strict RSS ceiling, so process isolation remains the correctness boundary and
the documentation does not promise exact cross-platform RSS enforcement.

Parse, name, runtime, limit, and result-validation failures are normal
repairable typed-tool rejections, not Go errors that fail the research block.
They use these stable issue codes:

| Code | Meaning |
| --- | --- |
| `starlark_code_required` | `code` is empty |
| `starlark_parse_error` | Source cannot be parsed |
| `starlark_name_error` | A referenced name is unavailable |
| `starlark_runtime_error` | Evaluation failed, including explicit `fail` |
| `starlark_step_limit` | Evaluation exceeded `max_steps` |
| `starlark_timeout` | Evaluation exceeded the tool timeout |
| `starlark_data_json` | `data_json` is invalid or cannot be converted |
| `starlark_result_missing` | No top-level `result` was assigned |
| `starlark_result_type` | `result` is not JSON-compatible |
| `starlark_result_non_finite` | `result` contains NaN or infinity |
| `starlark_output_limit` | Result JSON or stdout exceeded its configured limit |
| `starlark_worker_exited` | Worker exited before returning a valid response |

Issues include bounded line/column parser diagnostics, an `EvalError`
backtrace where available, the relevant bounded stdout tail, and an actionable
repair hint. The response should expose all diagnostics useful to repairing one
call without leaking unbounded output. Parent/block cancellation and failures
to start or communicate with the worker remain infrastructure errors. An
unexpected worker exit is reported with `starlark_worker_exited` when the
parent can still return a valid rejection envelope; protocol corruption or a
parent-side I/O failure fails the block.

## 8. Research Completion Protocol

Without `terminate_tool_id`, or a terminating `tool_use`, a normal assistant
completion ends Research and the block exposes no direct `result` value.
Artifacts remain available. This form is valid only in `full` and
`research_only`; `collection_only` always requires exactly one terminating
`tool_use`.

With `terminate_tool_id` or a terminating `tool_use`, the active phase completes
only after an accepted call. The terminal tool's output type must be
string-compatible. Its optional output becomes the block's optional string
`result`; complex results belong in files in the block workspace.

When a turn ends without the required terminal call, r42 sends a user message
that explicitly requires the model to call it. An `accepted = false` response is
returned as issues so the same session can repair its arguments and call again.
Every rejected terminal call and every completed turn without a terminal call
consumes one protocol attempt. `max_protocol_attempts` defaults to 10.

Required artifact failures are also repairable issues returned to the active
Collection or Research session. A new QC revision round resets the
protocol-attempt budget.

## 9. Research Workflow and QC Protocols

### Mode selection and state machines

The `full` state machine is:

```text
Collection --checkpoint--> Collection QC --sufficient--> Research
    ^                            |                           |
    |--------- needs_more -------|                           | candidate
                                                             v
                                      complete <---pass--- Final QC
                                                              |
                                                              | revise_research
                                                              v
                                                             Research
```

`collection_only` has one phase and no QC transition:

```text
Collection --accepted terminating tool_use--> complete
```

`research_only` starts from frozen inputs:

```text
Research --accepted completion-------------------------> complete
    |
    | candidate, when qc is configured
    v
Final QC --pass----------------------------------------> complete
    |
    | revise_research
    v
Research
```

The coordinator creates only the sessions reachable in the selected mode. A
skipped phase does not open an SDK session, receive a prompt, register protocol
tools, consume retry/round budgets, or emit a synthetic verdict.

### Full mode

Collection in `full` is the open-world phase. It uses only
its effective provider, `collection_tool_ids`, Collection skills, the shared
built-in policy, and the
mandatory `r42_set_information_needs`, `r42_save_artifact`,
`r42_register_artifact`, and
`r42_collection_checkpoint` tools. Before any collection or artifact-writing
tool call, Collection must call `r42_set_information_needs` once to freeze the
complete search plan: 1-10 information needs, each with 1-5 objective stop
conditions, assigned canonical IDs such as `NEED-001` and `NEED-001-SC-001`.
The plan is permanently frozen after submission: later rounds may not add,
edit, rename, delete, or split needs or conditions. Before the plan is frozen,
every non-read-only Collection tool is rejected. `r42_save_artifact` accepts
complete Markdown content plus a required source identifier, which may be a URL
or a non-URL value, writes the identifier into the artifact header, registers
the evidence artifact, and returns its path and artifact ID. The returned
artifact ID is ready to use; Collection must not register the returned path
again. A configured typed acquisition result is retained by tool-call ID so
registration can materialize it as a managed file; a tool that already wrote a
file can register that workspace path instead. `r42_register_artifact` accepts
an optional `source`; when supplied and the target has no non-empty
`- Source:` or legacy `- URL:` header, registration prepends `- Source:
<source>`. Registration validates source exclusivity, existence, non-empty
content, and ownership. Each saved artifact receives a run-scoped artifact ID.
Path-based source immutability is not guaranteed.

Each Collection round must call `r42_collection_checkpoint` exactly once with
one `continue` or `stalled` disposition for every active need; it is the final
valid tool call of the round, after which Collection's non-read-only tools
lock. `stalled` means Collection made a genuine search effort for that need and
found no productive next search action. A checkpoint includes every newly
registered evidence artifact. An empty checkpoint is valid only with a
non-empty `empty_reason`. Reaching `collection_batch_size` sets
`checkpoint_pending`: new acquisition calls are rejected, but already in-flight
work may finish and registration/checkpoint remain callable. The default batch
size is 10.

There is no global "collection exhausted" declaration. Collection's inability
to find more is expressed per need through the `stalled` disposition. Collection
QC is mandatory even when no `collection_qc` block is declared. It uses a
persistent session with fixed read-only snapshot/artifact projections and the
mandatory `r42_collection_qc_verdict` tool. It performs semantic sufficiency
review only; the registration and checkpoint tools are authoritative for
mechanical validation. Each QC round submits one assessment per active need:
`sufficient` lists no unsatisfied conditions, or `needs_more` lists only
remaining frozen condition IDs. Remaining condition IDs may only shrink between
rounds; a satisfied condition is never reopened. Malformed verdicts do not
advance the reviewed cursor; valid verdicts do.

Each need keeps its own lifecycle:

```text
ACTIVE -> SATISFIED
        -> UNRESOLVED / SEARCH_STALLED
        -> UNRESOLVED / BUDGET_EXHAUSTED
```

A need becomes `search_stalled` after two consecutive rounds where Collection
reports `stalled`, QC reports `needs_more`, and QC reports `none` evidence
progress. A need becomes `budget_exhausted` at the tenth Collection round (the
default hard cap). Terminal needs are frozen and never reopened.

`max_collection_rounds` counts actual entries into Collection, including the
initial phase. The default is 10. When the limit is exhausted, every remaining
active need becomes `budget_exhausted` and Research proceeds with the full
`information_need_outcomes`. The same no-reopen rule applies to Final QC, which
has no `reopen_collection` decision at all. Merely revising Research or
retrying a verdict does not consume a Collection round.

Research is closed-world synthesis. It can read all registered evidence artifacts by ID,
read declared candidate artifacts, write only declared Markdown file artifacts,
use explicitly configured trusted `tool_ids`, and call its optional termination
tool. Obvious network, shell, write/edit, glob, task/sub-agent, and user-input
built-ins are always denied; read-only `view`, `grep`, `head`, and `tail`
remain available. Protocol and artifact failures are repairable in the same
persistent Research session. Research receives the complete
`information_need_outcomes`; unresolved needs must be represented as
uncertainty, never as proven absence.

### Collection-only mode

`collection_only` uses the Collection provider, `collection_tool_ids`,
Collection skills, applicable built-ins, `tool_use` tools, and artifact
save/registration helpers in one persistent session. It deliberately does not
create information needs, stop conditions, Collection checkpoints, Collection
QC, Research, or Final QC. The agent may interleave acquisition and calculation
until it can submit its final structured result; discovering a missing input
during calculation therefore does not require reopening another phase.

All retained business data is an artifact. There is no separate evidence
pending/reviewed/approved lifecycle in this mode. Acquisition results are not
automatically materialized merely because a tool returned them: the agent must
explicitly save/register data that needs to persist, or submit it through a
configured typed tool that writes declared artifacts. Documentation fetched to
repair Starlark code is operational context and does not need registration.

Exactly one `tool_use` must have `terminate = true`; non-terminating tool uses
may provide progress checkpoints or other task-specific operations. Ordinary
assistant completion without an accepted terminal call is invalid and consumes
the same bounded completion-protocol attempt budget as the existing Research
terminal protocol. A rejected terminal call returns all mechanical issues to
the same session for repair. Declared required artifacts are validated before
the workflow completes.

### Research-only mode

`research_only` opens the existing closed-world Research session directly. It
uses `tool_ids`, Research skills, imported and declared artifacts, and
`tool_use` exactly as Research in `full` does. Collection fields and Collection
protocols are absent, rather than emulated with an empty checkpoint. With no
`qc` block, accepted Research completion ends the workflow. With `qc`, the
candidate follows the existing Research/Final-QC revision loop.

### Final QC semantic boundary

Final QC exists in `full` or `research_only` only when `qc` is configured. It
receives the original task, non-empty semantic `criteria`, candidate result,
declared artifact metadata, and read-only artifact projections, but not the
Research transcript. In `full`, it also does not receive information-need
outcomes or stop conditions. Collection QC exclusively owns evidence
sufficiency and primary-coverage decisions; Final QC reviews only the claims
actually present in the candidate.

The research-level `final_qc_strictness` setting controls the semantic threshold:
`strict` requires source facts to materially match evidence and interpretations
to be strictly derivable; `balanced` permits a reasonable one-step inference;
`brief` accepts concise, plausible analysis while retaining checks for material
contradictions, invented premises, misleading certainty, and unsupported
precision. This setting defaults to `balanced` and is authoritative over later
task prompts, candidate instructions, and custom criteria.

Its mandatory `r42_qc_verdict` decision is one of:

- `pass`, with no issues, completes the workflow;
- `revise_research`, with one or more issues, requests another Final QC
  confirmation after direct repairs.

Final QC can never reopen Collection or hand work back to Research, and must
not reject a candidate for missing coverage. After a material finding, it must
patch the smallest exact artifact text, reread it, and submit a confirmation
verdict. `max_qc_rounds` defaults to 5; a non-pass decision on the last review
fails with the unresolved issues.

The host exposes `r42_qc_open_issues` as a read-only lookup for the current
stable issue baseline. If a revision verdict uses an unknown issue ID, the
verdict is rejected immediately and the response identifies the current IDs.
Closed Research can use `r42_patch_markdown` for one exact
unique replacement outside the protected `Sources` section, while
`r42_write_markdown` remains available for full rewrites.

The deep-research example declares a `generate_source_table` Go tool. Its only
model-visible argument is the opaque report artifact ID; the tool resolves cited
quote IDs against the validated knowledge artifacts bound by that example and
rebuilds the `Sources` section after each report revision. Other research
workflows do not receive or depend on this deep-research-specific tool.

Final QC inherits only effective provider/model/reasoning/retry/permission
values. Its tools, skills, quotas, allowlist, and denylist are explicitly scoped
to `qc`; custom typed tools remain an author trust boundary. Fixed r42 evidence
readers are read-only, and the mandatory verdict tool cannot be filtered out.

### SecJury composition

The SecJury example uses the new modes and calculator without adding a special
SecJury runtime:

```text
collection_only build_dcf
  search + artifact registration + Starlark calculation + submit DCF
       |
       v
research_only dynamic jurors (serial = false, same frozen DCF)
       |
       v
research_only synthesis --> report.md
```

`build_dcf` is one LLM session and preserves the earlier Go SecJury
`dcf-model.v2` generation process one-for-one: 3-5 historical periods, 5-10
projection periods, explicit UFCF construction, WACC, mid-year discounting
unless source data justifies another convention, perpetuity-growth terminal
value, equity bridge, implied return, and a complete odd square WACC/terminal
growth sensitivity grid. It is not upgraded to a different DCF version.

Acquisition and calculation happen in the same Collection context. All derived
numeric work must be performed through the configured `starlark_tool`; the LLM
may write the program required by the data available at that moment, inspect a
repairable rejection, and retry. Raw source retrieval and extraction are not
numerical calculations. If a calculation exposes a missing raw input, the same
session resumes acquisition and then recalculates. The calculator has no DCF
formula API, no network, and no hidden independent valuation engine.

The DCF builder retains the non-terminating monotonic progress tool and finishes
only through `submit_dcf_model`. That terminal tool remains authoritative for
mechanical schema checks, period counts, sensitivity-grid shape and base-case
alignment, progress completion, and artifact writes. It does not independently
recompute every DCF formula or maintain a second calculation ledger. The frozen
model, source declarations, progress state, and other retained business data
are declared artifacts.

With `use_pplx = false`, the builder uses configured `web_search` and
`web_fetch`. With `use_pplx = true`, it uses `pplx_finance_search` first for
financial data, `pplx_pro_search` for filings/general discovery and fallback,
and `pplx_fetch` for selected URLs instead of the built-in web tools. In either
mode, the session may fetch the Starlark documentation URLs from section 7 when
necessary; those lookups are not DCF source artifacts unless explicitly saved.

Every dynamic juror uses `research_only`, receives the identical frozen DCF,
runs concurrently under the normal global and module parallelism limits, and
submits one structured opinion from its configured investor/operator persona.
Jurors may challenge evidence quality, assumptions, formula interpretation,
reinvestment, terminal dependence, sensitivity, and margin of safety, but do
not acquire new data or modify the DCF. The synthesizer also uses
`research_only`, consumes the frozen model and ordered opinions, and writes the
deterministically rendered `report.md`. SecJury configures no Final QC for the
builder, jurors, or synthesis because those phases could not repair missing
acquisition and the typed terminal tools already enforce mechanical contracts.

## 10. Artifacts and Paths

An artifact has a name, `type` (`file` or `directory`), `path`, `required`, and
`non_empty`. Globs are not supported.

- Relative paths are based on the research block workspace.
- Absolute paths and paths containing `..` are allowed deliberately.
- A required artifact must exist before the candidate advances to QC or block
  completion.
- A non-empty file has a byte size greater than zero.
- A non-empty directory recursively contains at least one regular file. Empty
  subdirectories alone do not satisfy it.
- For `required = false`, `.path` still returns the expected absolute path when
  the artifact does not exist.

`research.static.name.artifact` is a `list(object)`, matching Golden's nested-block
representation. Referencing it creates a Golden implicit dependency. A plain
string containing the same filesystem path creates no dependency; use
`depends_on` when needed.

## 11. Modules

A module points to an existing directory and is Terraform-like:

```hcl
module "sector" {
  source      = "./modules/sector"
  parallelism = 4
  timeout     = "1h"

  topic = var.topic
}

output "sector_report" {
  value     = module.sector.report
  sensitive = true
}
```

`module.source` must be a literal string because it is resolved before Plan.
Both that attribute and the root argument to `r42 init [SOURCE]` accept local
filesystem locations or go-getter source locators. Explicit local paths (`./`,
`../`, or absolute paths) are resolved directly; Init also recognizes an
existing bare relative root path as local. Other strings are passed unchanged to
go-getter v2.2.3, which supports forms such as GitHub shorthand, forced `git::`
URLs, repository subdirectories, and supported HTTP archives. Terraform
Registry addresses and version negotiation are outside this contract.

The root source is downloaded to a private temporary directory when necessary,
then the complete resolved package is copied to `<cwd>/.r42/config`, excluding
`.r42` and `.git` directories. Init recursively discovers module blocks in that
package's `*.r42.hcl` files. Installed modules live under
`<cwd>/.r42/modules` according to their canonical module address:

```text
module.a                      -> <cwd>/.r42/modules/a
module.a.module.b.module.c    -> <cwd>/.r42/modules/a/b/c
```

Every `r42 init [SOURCE]` refreshes installed modules through a staging
directory, so a failed copy or download leaves the previous installation
intact. Source cycles fail Init.

Successful Init writes `<cwd>/.r42/config/.initialized.json`, recording the
initialization format and a SHA-256 identity of the canonical local path or
remote locator. It also atomically replaces `<cwd>/.r42/state.json`, which
records the active local source path or sanitized remote locator, the active
snapshot directory, and no outputs. Remote URL credentials, fragments, and
query parameters other than `ref` are not persisted. Plan and Apply fail fast
when the marker and state are absent, invalid, or inconsistent.
Init also uses
`<cwd>/.r42/.initializing` as a transaction marker: if a process is interrupted
after module activation but before configuration activation, Plan and Apply
refuse the potentially inconsistent state until Init is run again. Recovery
forces a complete module refresh before clearing the transaction marker.

Plan and unsaved-plan Apply accept no source-directory argument. They parse only
`<cwd>/.r42/config` and the canonical installed module directories, so edits to
the source package have no effect until Init runs again. Saved-plan Apply still
requires a valid initialized project because tools and module-owned resources
may depend on the active module installation. Apply creates a new nested r42
executor over the saved Plan and never reparses module source.

Every block can read `path.module`. It is an absolute `/`-normalized path to the
directory containing that block: `<cwd>/.r42/config` for root blocks, or the
corresponding `.r42/modules/a/b/c` directory for nested blocks. `cwd()` remains
the absolute directory where the r42 CLI process started.

`variable` uses Terraform's basic semantics: `type` is required, `default` is
optional, and a variable without a default must be supplied by its caller during
Plan. Root variable loading and precedence are delegated to Golden.

`output.value` is required. Its type is inferred and must be fully known during
the top-level Plan. `description`, `sensitive`, and `depends_on` follow Terraform
semantics. Output names and types cannot be generated dynamically. A parent can
address module outputs but cannot address child internal blocks.

All outputs are published atomically only after the entire nested Apply succeeds.
Failure, cancellation, or timeout publishes no partial outputs.

## 12. Parallelism and Timeouts

CLI `--parallelism` defaults to 10. It counts only running `research` blocks;
module orchestration nodes consume no permit. A research block acquires its
permit before Apply begins and holds it until all workflow phase sessions have been
closed or close retries have been exhausted.

A module may set `parallelism`. A leaf research block must acquire the global
permit plus every explicit ancestor-module permit in root-to-leaf order. Its
effective concurrency is therefore bounded by the global setting and every
enclosing module.

Golden `RunPlan` traverses independent ready vertices concurrently while
decoding and validating a source or reconstructed configuration. Golden's
generic `Traverse[ApplyBlock]` is serial, so r42's saved-plan scheduler performs
the Apply traversal from the serialized dependency lists. The research Apply
wrapper also acquires the global and ancestor-module permits. Module
orchestration callbacks therefore do not consume research permits, while ready
research blocks can execute concurrently up to all effective limits.

Research blocks and modules may set `timeout` using Go duration strings such as
`30m` and `2h`. CLI overall `--timeout` has no practical limit unless supplied;
when omitted, r42 uses a long-lived internal deadline so the Copilot SDK does
not apply its 60-second default. Research and module timeouts also have no
default. The effective deadline is the earliest of any configured overall
deadline, ancestor module deadlines, and the research block deadline.

Every active workflow phase session also has one inactivity timer. It defaults
to `15m` and is configurable for the complete root-and-module Apply with
`--session-stall-timeout`. Each SDK event, including reasoning/message deltas
and tool progress, moves the deadline forward. Starting or finishing a local
typed-tool handler does the same. This is an inactivity limit, not a maximum
model-turn duration.

## 13. Cancellation and Session Cleanup

Fail-fast cancellation follows this order:

1. Cancel the root context and stop scheduling new research blocks.
2. Propagate cancellation through nested module executors.
3. Terminate active external, inline Go, and Starlark worker process trees.
4. Close every session created by the selected `phase_mode`.
5. Release parallelism permits and flush debug files.

The original error remains primary; cleanup errors are additional diagnostics.
After a session has been created, lifecycle retry repeats the current operation
on that same logical session.

If the inactivity deadline expires, r42 uses the following recovery sequence:

1. Abort the current turn.
2. Cancel its send context and wait for `SendAndWait`, SDK-observed tool or
   subagent work, and locally executing typed-tool handlers to stop.
3. Reuse the SDK session only when Abort restored `session.idle`; otherwise
   disconnect and resume the same session ID with no pending-event replay.
4. Subscribe to the recovered SDK session and send one continuation prompt that
   tells the model to inspect existing artifacts before repeating work.

Abort, termination, and any required resume share a fixed 10-second bounded
recovery window. If that window expires, the session is tainted: r42 does not
resume it or issue a concurrent close, and the block fails so the CLI can exit.
The continuation uses the same inactivity timer. If that continuation also
stalls, it is aborted and cleaned up but is not recovered again; it fails the
block. Ordinary parent context cancellation never sends a continuation. It
first aborts native agent work, then cancels the waiter and gives the same
termination barrier at most 10 seconds. Session Close is also bounded by 10
seconds. The final shared-client `Stop()` has the same bound and falls back to
the SDK's `ForceStop()` when graceful shutdown does not return, so a stuck
`Disconnect()` cannot keep the CLI alive indefinitely.

Only an exhausted `Session.Disconnect()` failure receives special treatment: it
is recorded as a cleanup warning and does not turn an otherwise successful
block into failure. This exception does not apply to session creation, messages,
model calls, tools, or QC.

## 14. Runs, Plans, Debugging, and Sensitive Data

Every Apply creates a unique run. Block workspaces and artifacts persist under
the r42 CLI process working directory, independently of the directory containing
the applied `*.r42.hcl` files:

```text
.r42/runs/<run-id>/...
```

The first version never cleans old runs automatically. Inline Go compilation
artifacts live in process-temporary storage and are deleted best-effort on r42
exit.

By default r42 does not persist complete workflow transcripts, prompts, tool
arguments, or tool results. With CLI `--debug`, files in the run directory
contain:

- Complete r42 and user system prompts.
- Every user and assistant message.
- Complete transcripts for every session created by the selected `phase_mode`.
- Tool arguments, results, stdout, and stderr.

Raw SDK payloads and complete tool arguments/results are written to files only.
The live Apply UI displays normalized assistant text, available reasoning text,
tool names/progress, and errors so users can observe long-running research.
Transport credentials are not deliberately emitted, but prompt/tool content may
contain secrets; the whole run directory and any terminal recording are
sensitive.

When `--debug` is enabled, lifecycle and transcript events are appended to
`.r42/runs/<run-id>/events.jsonl`. Each JSON object has a monotonic `sequence`,
UTC `timestamp`, event `kind`, and the fields relevant to that event. Lifecycle
events include an `action` and one of `started`, `completed`, `failed`, or
`skipped`; terminal events include `duration_ms`, and failures include `error`.
Each event is flushed immediately so another process can inspect progress while
r42 is still running.

Lifecycle actions cover the complete CLI execution path:

- directory scanning, source-file collection, HCL syntax/hclwrite parsing, and
  extracted block addresses and source ranges;
- Golden config initialization and `RunPlan`, every successful r42 block decode
  and Plan, and immutable plan snapshot construction. Decode failures are
  recorded by the failed Golden initialization event until Golden exposes a
  per-block decode lifecycle hook;
- plan display/save, Apply-time r42 Config construction, Golden initialization
  and traversal, block factory/Apply/cleanup, and output resolution;
- nested module planning and Apply through the same actions; and
- Copilot session open/send/close with block address, session role, model,
  workspace, and typed-tool names.

Message and tool events contain the complete system, user, and assistant
payloads plus tool arguments/results/stdout/stderr. Copilot SDK tool-call input
deltas, tool search/request, execution start/progress/partial-result, and
execution-complete events are preserved as `sdk_event`, including built-in
tools and their tool-call/turn correlation IDs. Assistant usage events record
input, output, reasoning, and cache token counts. The CLI prints the debug log
path and a sensitive-data warning to stderr. `skipped` means an operation was
deliberately not attempted because a
prerequisite failed; it is distinct from a failure in the operation itself.

Variables and outputs support `sensitive = true`, and sensitivity propagates
through expressions and module boundaries. Plan display, normal Apply display,
and normal logs redact known sensitive values.

`.r42plan` is persisted unencrypted and may contain actual sensitive values.
r42 applies the strictest practical current-user file permissions and warns the
user. The first version adds no explicit format-version compatibility rejection;
an unreadable plan fails as a normal decoding error.

No skill, program, tool, or external-file content hash is recorded. Apply may
therefore observe external content changed after Plan. The planned DAG structure
and planned configuration values themselves remain immutable.

## 15. CLI Contract

Required workflows:

```text
r42 init [<source>]
r42 plan [--out <file.r42plan>]
r42 apply [<file.r42plan>]
r42 output
```

`init` defaults its source to `.`. It refreshes the active configuration
snapshot and installed modules on every invocation through a staging directory.

`plan` reads only `<cwd>/.r42/config`. It always prints the Plan to stdout and
only writes a saved Plan file when `--out` is present.

`apply` without an argument plans the active configuration snapshot in memory;
with one argument it loads that saved Plan. Configuration directories are not
valid Apply arguments. Both forms require a successfully initialized current
working directory. In `auto`, `tui`, and `repl` UI modes, Apply prints the
immutable Plan JSON to stdout before execution starts. After a successful Apply,
it atomically publishes the output values and run metadata to
`<cwd>/.r42/state.json`, then prints the output values as a pretty second JSON
document. Apply failure does not replace outputs from the previous successful
Apply. A subsequent successful Init clears them. Progress for these three modes
is written to stderr. `--ui=auto` selects the Bubble Tea TUI when stdin and
stderr are interactive terminals of at least 50x12 and falls back to the
line-oriented REPL renderer for redirected output, CI, `TERM=dumb`, or smaller
terminals. `--ui=tui` requires those capabilities; `--ui=repl` forces stable
text events. The JSONL mode has the different I/O contract in section 15.1.

`output` accepts no arguments or formatting flags. It reads the values saved by
the latest successful Apply and writes one pretty JSON object to stdout. It
writes no diagnostics to stdout, so `r42 output | jq ...` is the stable pipeline
interface. It fails when the current configuration has no saved outputs. Empty
but successfully published outputs are represented by `{}`.

Both renderers consume the same in-memory event stream used by the debug
recorder. The stream exists even without `--debug`; that flag controls sensitive
event persistence, not live state production. The projector builds its initial
state from the fully expanded saved Plan, including module child Plans, then
tracks block lifecycle, workflow phase, assistant reasoning/message deltas,
tool execution, and usage. Usage is deduplicated by provider API-call ID and the
displayed total is input plus output tokens; reasoning tokens are shown as a
breakdown and are not counted twice.

The TUI header reports research completion as `Tasks <done>/<total>`, while its
running and failed counts and overall status cover every node in the expanded
DAG. A failed module therefore fails the displayed run even when nested research
nodes remain waiting. At widths of 100 columns or more, DAG, detail, and timeline
are shown side by side; narrower supported terminals show only the focused
panel. Every `WindowSizeMsg` recomputes panel widths, viewport heights, and both
scroll bounds, then requests a full redraw. A live resize below 50x12 replaces
the layout with a terminal-size warning until enough space is restored.

The REPL renderer prints the initial expanded DAG and concise research activity,
tool calls, and module `START`, `DONE`, or `FAILED` transitions. Nested module
events use their canonical addresses. Both renderers strip terminal control
sequences from display copies of model, tool, path, and error text; debug JSONL
keeps the raw SDK event for diagnosis.

### 15.1 JSONL machine progress UI

`r42 apply --ui=jsonl` is a fourth, mutually exclusive UI mode. It is intended
for a local worker process that supervises r42 and forwards progress to a web
backend. In this mode stdout is an NDJSON protocol: every complete line is one
JSON frame, and r42 does not print the pretty Plan or outputs JSON documents to
stdout. stderr remains the human-readable diagnostic stream. `--debug` may be
used at the same time, but it only controls the existing sensitive debug file;
it never makes the stdout protocol less restrictive.

The worker reads stdout and stderr through separate pipes. It decodes stdout
line by line, records node state and timeline events, and may relay those records
to browsers using its own transport. stderr is diagnostic data and must not be
parsed as part of the progress protocol. Report identification and S3 upload
remain ordinary DAG behavior; an `s3_folder` block publishes its lifecycle
through the same progress stream without putting credentials or object contents
on stdout.

#### Negotiation

stdin is reserved for negotiation in JSONL mode. Before planning or applying,
r42 writes and flushes `hello`, the worker selects the highest schema major it
supports from r42's advertised set, and r42 replies with `ready`:

```json
{"type":"hello","handshake_version":1,"protocol":"r42.progress","supported_schema_versions":[1,2],"r42_version":"0.8.0"}
{"type":"select","handshake_version":1,"schema_version":2}
{"type":"ready","handshake_version":1,"schema_version":2}
```

The middle line is written by the worker to r42's stdin; the other two lines are
written by r42 to stdout. Negotiation must finish within 5 seconds. Malformed
input, an unsupported handshake or schema version, EOF, timeout, or failure to
flush `hello` or `ready` fails the command before Plan or Apply starts. After
`ready`, stdin carries no progress commands and is otherwise ignored.
Cancellation continues to use process signaling through the supervising worker;
pause, resume, and checkpoint recovery are not supported.

Handshake version 1 is stable independently of event schema majors. r42
advertises every event schema major it can encode, and accepts only a selected
value from that exact list. For event schemas, additive optional fields do not
require a major bump. Consumers must ignore unknown fields and unknown events
whose `critical` field is false or absent. An unknown critical event makes the
consumer's progress view incomplete. When a breaking event-schema change is
introduced, r42 must retain the immediately preceding major alongside the new
major, with a separate encoder and contract fixtures for each advertised major.

#### Event envelope and records

After `ready`, every event frame includes `type`, `critical`, `protocol`,
`schema_version`, `run_id`, `sequence`, and `timestamp`, except where a record
definition says a field is not applicable. `protocol` is `r42.progress`;
`run_id` is opaque and identifies one Apply invocation; `sequence` increases at
the producer and may have gaps at the consumer; and `timestamp` is UTC RFC 3339.
Consumers must not require contiguous delivery or cross-node event ordering.
They may use sequence as a last-write-wins guard when their own ingestion is
concurrent.

The schema defines these records:

| Type | Critical | Required payload | Meaning |
| --- | --- | --- | --- |
| `run_snapshot` | yes | `nodes` | Initial sanitized projection of the expanded DAG. It is not the saved Plan. |
| `dynamic_tasks_materialized` | yes | `parent_address`, `nodes` | Announces nodes that become known during dynamic materialization. |
| `node_upsert` | yes | `node` | Replaces the current projection for one canonical block address. It is self-contained, not a delta. |
| `timeline_append` | no | `block_address`, `activity`, `summary` | Adds a best-effort, human-readable progress item for one block. |
| `run_completed` | yes | `status`, aggregate counts | Reports successful completion. It does not contain output values or report paths. |
| `run_failed` | yes | `status`, sanitized `summary` | Reports command failure after successful negotiation. |
| `run_canceled` | yes | `status`, sanitized `summary` | Reports cancellation observed by r42. |

`hello` and `ready` are handshake frames, not versioned event records. `select`
is the only worker-to-r42 frame.

A node projection contains the fields that are applicable from
`block_address`, `block_kind`, `parent_address`, `dependencies`, `phase`,
`status`, `activity`, `tool_name`, and aggregate token `usage`. Status values are
`waiting`, `running`, `succeeded`, `failed`, and `canceled`. The projection must
let a consumer reproduce the TUI's expanded DAG, select a block or dynamic task,
show its current phase and activity, and maintain a per-block timeline. Dynamic
members use their canonical block addresses.

`summary` is a short display string, not a debug payload. It is produced without
an additional model call. Deterministic templates are preferred. Any permitted
assistant-derived text is normalized, stripped of terminal control characters,
and truncated; this sanitization does not claim to detect semantic secrets.
Every summary is valid UTF-8 and at most 4096 bytes. Tool arguments are excluded
unless a future schema explicitly allowlists an individual field as safe.

#### Projection and privacy boundary

The JSONL stream is a stable progress projection over the internal event bus;
it is not serialization of `debuglog.Event`. It may expose only:

- DAG metadata: canonical address, kind, parent, dependencies, and status.
- Current workflow phase and deterministic activity.
- Tool name without arguments or results.
- Aggregate token usage.
- Sanitized, bounded summaries allowed by the selected schema major.

It must not expose system or user prompts, HCL variable values, the complete
Plan, raw tool arguments or results, raw stdout or stderr, raw SDK payloads, or
the sensitive debug record. The JSONL projection remains subject to these rules
when `--debug` is enabled.

#### State, delivery, and termination

The JSONL projector retains only the latest projection for each known node, so
its state is `O(number of blocks)`. It does not retain timeline history. The web
backend owns any durable or process-lifetime timeline; a browser keeps only the
most recent 200 timeline entries per block. Reconnection replay and Apply resume
are out of scope.

Internal event-bus observers are synchronous, so stdout encoding must run behind
a bounded asynchronous publisher. Research execution must never wait
indefinitely for the JSONL consumer. Under pressure, the publisher preserves the
latest `node_upsert` for each `block_address` by coalescing older pending values,
drops older `timeline_append` records first, and gives structural and terminal
records priority over timeline records. These priorities improve the view but do
not turn delivery into a success condition: all post-negotiation progress frames,
including terminal frames, remain best effort.

After successful negotiation, a closed or unwritable stdout disables further
progress publication and produces at most one warning on stderr. It must not
cancel, block indefinitely, or change the result of research. Shutdown may make
one bounded attempt to flush a terminal record and then abandon it. Consequently
the supervising worker treats process exit as authoritative: exit zero means the
Apply succeeded, a nonzero exit means it failed unless the worker itself requested
cancellation, and a missing terminal frame marks `progress_incomplete` rather
than changing that outcome.

The CLI also exposes:

- `--parallelism`, default 10.
- Overall `--timeout`, with no practical limit by default.
- Per-session inactivity `--session-stall-timeout`, default `15m`.
- `--debug`, disabled by default.
- `--ui`, default `auto`; accepted values are `auto`, `tui`, `repl`, and `jsonl`.
- Golden's existing root-variable input mechanisms without an r42-specific
  duplicate implementation.

CLI failures must identify the block address, preserve the root cause, and show
cleanup diagnostics separately. An Apply exits non-zero for any DAG failure,
except the explicitly downgraded post-success `Session.Disconnect()` cleanup
warning.

## 16. Validation Boundaries

Plan validates all information that is structurally available:

- HCL/cty types and references.
- Required fields and mutually exclusive authentication fields.
- Provider/wire/transport combinations.
- Initialized module existence, variable assignment, and output types.
- Inline Go syntax, imports, declarations, signature, and cty compatibility.
- ToolResponse shape and static invariants.
- Typed-tool IDs, registry membership, and `tool_name()` argument kind.
- Duration syntax and non-negative retry/attempt/concurrency values.
- `phase_mode` and all mode-specific required/forbidden field combinations.
- `starlark_tool` resource values and hard caps.

Plan deliberately does not validate:

- External program existence or executability.
- Whether a model supports a reasoning-effort value.
- Provider-specific tool-name restrictions beyond r42's generated 64-character IDs.
- External content hashes or future file contents.
- Exact cross-platform Starlark worker RSS enforcement.

Apply validates runtime values, child-process protocol, artifacts, SDK behavior,
credentials, and external resources. Business rejection is repairable only when
expressed through a valid `ToolResponse{accepted:false}`; unexpected errors are
never converted into model issues.

## 17. Accepted Risks and Deferred Work

- Tools inherit all environment variables and can escape their workspaces.
- Plan and debug files can contain secrets and are not encrypted.
- Changed external programs, skills, or files can alter Apply behavior after Plan.
- Interrupted runs leave retained workspaces and restart from scratch.
- Plan-format migrations, resume, hashing, automatic cleanup, stronger sandboxing,
  plugins, and dynamic runtime modules are future work only.
