# r42

r42 is an HCL-based configuration and execution engine for ~~the world, the universe and everything~~ reproducible AI research
workflows. A configuration describes a directed acyclic graph (DAG) of research
workflows, quality-control sessions, typed tools, artifacts, modules, variables,
and outputs. r42 plans the complete graph before it starts any model session,
then applies the immutable plan through the official GitHub Copilot SDK.

## Why r42

A serious deep-research run is rarely one prompt followed by one answer. It is
usually decomposed into many stages that search and reason from different
perspectives, reconcile conflicting findings, and assemble a knowledge graph
with a traceable chain from each conclusion back to its evidence and citations.

Many of those stages need their own quality gate. An independent session may
audit every claim and inference against the captured sources, return specific
issues to closed Research, and repeat that exchange through several rounds
of revision. The files produced along the way also matter: reports, snapshots,
datasets, and other artifacts must exist, be non-empty, and satisfy the contract
expected by downstream stages.

Some handoffs need stronger guarantees than a natural-language response. A
session can be given typed functions and required to finish a stage by calling
one of them with a structured completion payload. Schema mismatches, missing
fields, and invalid enum values are rejected and sent back for repair, just like
failed research QC. Those functions and research stages must also be reusable
across different investigations rather than copied into every workflow.

r42 makes this process explicit. Its Plan phase resolves the complete research
DAG, validates typed tool schemas, and captures module boundaries before paid or
stateful work begins. Apply executes only that immutable graph, runs independent
stages concurrently, gives each stage an isolated workspace, and fails fast when
a block cannot complete. QC loops, typed tools, artifacts, and whole subgraphs
can be packaged as modules and reused by other research configurations.

This design provides:

- reviewable JSON plans before execution;
- explicit and implicit dependencies between research tasks;
- persistent Collection, Collection QC, and closed Research sessions, plus an
  optional Final QC session, per block;
- typed inline Go and external-process tools with stable IDs;
- Terraform-style modules installed with `r42 init`;
- isolated per-block workspaces for reports, snapshots, and other artifacts;
- live TUI or line-oriented progress for the expanded DAG, session activity,
  tool calls, and token usage;
- opt-in JSONL debug logs containing prompts, reasoning, messages, and tool events.

## Install

r42 requires Go 1.25 or newer and the GitHub Copilot CLI executable. r42 uses
the official GitHub Copilot SDK, which starts Copilot CLI as its local agent
runtime, so the CLI must be installed even when the model is supplied through a
bring-your-own-key (BYOK) provider. Follow the
[official Copilot CLI installation guide](https://docs.github.com/en/copilot/how-tos/copilot-cli/set-up-copilot-cli/install-copilot-cli),
or install it through npm with Node.js 22 or newer:

```powershell
npm install -g @github/copilot
```

Then install r42:

```powershell
go install github.com/lonegunmanb/r42/cmd/r42@latest
```

Authentication depends on the provider configuration:

- When a research block omits `model_provider`, the SDK uses GitHub Copilot's
  default provider. Copilot CLI must then be authenticated with a GitHub account
  that has access to GitHub Copilot.
- When a research block references a BYOK `model_provider`, r42 passes that
  provider's endpoint and API key to the SDK. Copilot CLI is still the local
  runtime, but no GitHub account or GitHub Copilot subscription is required for
  the model call.

## Model providers

A `model_provider "name"` block describes how the Copilot SDK connects to a
model API. It does not select a model: each research or QC session supplies its
own `model` and references the provider as `model_provider.<name>`. Omitting the
reference uses GitHub Copilot's default provider; setting it enables BYOK.

| Field | Required | Purpose |
| --- | --- | --- |
| label `name` | Yes | Local provider name used by references such as `model_provider.openrouter`. |
| `type` | Yes | Protocol family: `openai`, `azure`, or `anthropic`. Use `openai` for an OpenAI-compatible API. |
| `endpoint` | Yes | Provider base URL passed to the SDK. Supply the API root, not a request endpoint such as `/responses` or `/chat/completions`. |
| `wire_api` | No | `completions` or `responses`; defaults to `completions`. Not valid for `anthropic`. |
| `transport` | No | `http` or `websockets`; defaults to `http`. WebSockets requires `wire_api = "responses"`. Not valid for `anthropic`. |
| `headers` | No | Additional request headers as `map(string)`. Values must be known during Plan. |
| `api_key` | No | Literal API key passed through the SDK's `APIKey` field. Prefer `api_key_ref`. |
| `api_key_ref` | No | Name of an environment variable containing an API key. r42 reads it during Apply. |
| `bearer_token` | No | Literal token passed through the SDK's `BearerToken` field. Prefer `bearer_token_ref`. |
| `bearer_token_ref` | No | Name of an environment variable containing a bearer token. r42 reads it during Apply. |
| `retry` | No | At most one nested retry policy. Omitted fields use r42's provider retry defaults. |

At most one of `api_key`, `api_key_ref`, `bearer_token`, and
`bearer_token_ref` may be set. Choose the authentication field required by the
provider instead of manually constructing an `Authorization` header. In
particular, an OpenAI-compatible API key should normally use `api_key_ref`,
which maps to the SDK's API-key setting rather than its bearer-token setting.

> [!WARNING]
> Literal `api_key` and `bearer_token` values are marked sensitive and redacted
> from displayed plans, but saved `.r42plan` files contain those values
> unencrypted. Prefer `api_key_ref` or `bearer_token_ref`; these store only the
> environment variable name in the Plan and resolve the secret during Apply.

### BYOK example

This example uses OpenRouter's OpenAI-compatible API. Set the key in the
environment that will run `r42 apply`:

```powershell
$env:OPENROUTER_API_KEY = Read-Host -MaskInput "OpenRouter API key"
```

Then declare the provider and reference it from the research block:

```hcl
model_provider "openrouter" {
  type        = "openai"
  endpoint    = "https://openrouter.ai/api/v1"
  wire_api    = "completions"
  transport   = "http"
  api_key_ref = "OPENROUTER_API_KEY"

  headers = {
    "HTTP-Referer"       = "https://github.com/lonegunmanb/r42"
    "X-OpenRouter-Title" = "r42"
  }

  retry {
    lifecycle_retries    = 3
    model_call_retries   = 3
    interval_seconds     = 2
    max_interval_seconds = 30
  }
}

research "static" "summary" {
  model_provider  = model_provider.openrouter
  model           = "openai/gpt-4o"
  system_prompt   = "Act as a rigorous research analyst."
  prompt          = "Summarize the most important design tradeoffs in this repository."
}
```

`api_key_ref` is an environment-variable name, not the key itself. Plan can
therefore validate and save this configuration without reading the credential.
Apply fails fast if `OPENROUTER_API_KEY` is missing or empty. The OpenRouter
headers shown above are optional; other OpenAI-compatible providers generally
need only their base URL, model identifier, and authentication setting. See
[OpenRouter's OpenAI SDK documentation](https://openrouter.ai/docs/guides/community/openai-sdk)
for its current endpoint and optional headers.

The nested `retry` block accepts the same five fields documented for research
retries: `lifecycle_retries`, `model_call_retries`, `interval_seconds`,
`max_interval_seconds`, and `error_message_regex`. Provider values become the
base retry policy for every research or QC session that references the provider;
session-level `retry` blocks can override individual fields.

## Example

`r42 init` resolves a local directory or go-getter source and copies every
`*.r42.hcl` file plus its supporting resources into the active configuration
snapshot. The following configuration creates one research workflow:

```hcl
research "static" "summary" {
  model            = "gpt-5.6-sol"
  reasoning_effort = "medium"
  system_prompt    = "Act as a rigorous research analyst. Distinguish evidence from inference."
  prompt           = "Summarize the most important design tradeoffs in this repository."
  permission       = "approve_all"
}
```

Initialize referenced modules, inspect and save the plan, then apply it:

```powershell
r42 init .

# Inspect the initialized template's root input contract without planning
r42 schema --json

r42 plan --out research.r42plan
r42 apply research.r42plan

# Or plan and apply the initialized snapshot directly
r42 apply

# Read the outputs saved by the latest successful Apply
r42 output
```

`r42 schema --json` emits one versioned JSON document describing only the root
variables exposed by the initialized snapshot. It succeeds without values for
required variables, redacts sensitive defaults, and does not enter Plan, Apply,
model-session, or typed-tool execution paths. The protocol is specified in
[Root Variable Schema Command](docs/root-variable-schema.md).

`r42 apply` is the convenience form that plans `<cwd>/.r42/config`, prints the
plan as JSON, and immediately applies it. Source changes do not affect the
active snapshot until `r42 init` is run again. The overall Apply timeout is
unset unless specified with `--timeout`. Each active Copilot
session also has a 15-minute inactivity watchdog, configurable with
`--session-stall-timeout`. Every SDK event and typed-tool handler start or finish
resets that one per-session deadline. If it expires, r42 aborts the stalled turn,
waits up to 10 seconds for the old send and all tracked tool, handler, or
subagent work to stop, resumes the same logical session when needed, and sends
one continuation message. If that continuation also stalls, or if an Abort or
termination barrier exceeds the bounded cleanup window, r42 fails the research
block instead of starting overlapping work. Session Close is bounded separately
so a stuck SDK disconnect cannot prevent the CLI from exiting.

Apply selects its progress UI with `--ui=auto|tui|repl|jsonl` (default `auto`). An
interactive terminal at least 50 columns by 12 rows uses the Bubble Tea TUI;
redirected output, CI, and unsupported terminals use the line-oriented REPL
renderer. The TUI header shows the run directory, expanded research-task
counts, active and failed counts across the complete DAG, and cumulative input
plus output tokens across all workflow phase sessions. A module failure therefore
sets the overall status to `FAILED` even when its child research blocks never
started.

At 100 columns or wider, the TUI renders DAG, selected-node detail, and event
timeline panels together. Below 100 columns it renders the focused panel at the
full terminal width. Every terminal resize immediately recomputes this layout,
clamps vertical and horizontal scroll positions to the new viewport, and
requests a complete redraw. If a running TUI is resized below 50 columns or 12
rows, it shows a size warning until the window is enlarged again.

Navigate with Tab/arrow keys, PgUp/PgDn, Home/End, and horizontal
Alt+Left/Alt+Right scrolling. Enter folds a module and `f` toggles live timeline
following. Press `q` twice to cancel a run. The REPL renderer prints the initial
DAG, research activity, tool transitions, and parent or nested module
`START`/`DONE`/`FAILED` transitions.

Apply prints the pretty Plan JSON before execution and pretty outputs JSON after
success. These are two consecutive JSON documents intended for immediate human
inspection. Both progress renderers write to stderr. For a single JSON document
that is safe to pipe, use `r42 output`; for example,
`r42 output | jq -r '.report_path'`. Use `--ui=repl` to force stable
line-oriented progress or `--ui=tui` to require an interactive terminal.

### JSONL worker mode

`r42 apply --ui=jsonl` is a bidirectional local-process protocol for a
supervising worker. Its stdout contains JSONL protocol frames only; it never
contains the pretty Plan or Apply outputs. stderr remains the diagnostic stream.

The worker reads the initial `hello` frame, writes exactly one selection on
stdin, then waits for `ready` before treating subsequent stdout lines as
progress records:

```json
{"type":"select","handshake_version":1,"schema_version":1}
```

After `ready`, r42 emits an initial `run_snapshot`, complete `node_upsert`
records, dynamic-node announcements, and best-effort `timeline_append` records.
It may coalesce, reorder, or drop progress records under consumer backpressure.
`run_completed`, `run_failed`, and `run_canceled` are also best effort; the
worker must use the r42 process exit code as the final outcome and mark progress
incomplete if no terminal record arrives. The worker should retain its own
durable history and cap a browser timeline at 200 entries per block.

## Variables, locals, and outputs

A `variable` block declares a typed input to the root configuration or a module.
Its value is read as `var.<name>`. r42 requires `type`; `description`, `default`,
and `sensitive` are optional, and nested `validation` blocks can reject invalid
values during Plan. A variable without a default must be supplied by the caller.

`locals` gives names to expressions derived inside the configuration. Local
values are read as `local.<name>` and are not caller-settable inputs. An `output`
publishes a value after Apply. Root outputs are printed after Apply and saved in
`<cwd>/.r42/state.json`; `r42 output` prints the saved values as one JSON object.
Module outputs form the module's public interface and are read by its caller as
`module.<module_name>.<output_name>`.

```hcl
variable "topic" {
  type        = string
  description = "Subject to investigate."

  validation {
    condition     = length(trimspace(var.topic)) > 0
    error_message = "topic must not be empty."
  }
}

locals {
  normalized_topic = trimspace(var.topic)
}

research "static" "summary" {
  model         = "gpt-5.6-sol"
  system_prompt = "Produce a concise, evidence-based summary and save it to ${artifact(\"summary\").path}."
  prompt        = "Research ${local.normalized_topic}; write the Markdown report with r42_write_markdown."

  artifact "summary" {
    type        = "file"
    path        = "summary.md"
    description = "Final Markdown research summary"
    required    = true
    non_empty   = true
  }
}

output "summary_path" {
  description = "Validated Markdown summary."
  value       = research.static.summary.artifact.summary.path
}
```

A research block exposes `.result` only when it configures
`terminate_tool_id`; the value is the accepted string-compatible output of that
typed tool. A normal assistant completion without a terminate tool can publish
artifacts, as above, but has no `.result` attribute.

Root variables can be assigned on `plan` or on an unsaved-plan `apply`. Both
Terraform-style single-dash flags and conventional double-dash flags are
accepted, and each flag can be repeated:

```powershell
r42 plan -var 'topic="USD/JPY"' -var-file inputs.r42vars --out research.r42plan
r42 apply -var 'topic="USD/JPY"'

# These spellings are equivalent
r42 plan --var 'topic="USD/JPY"' --var-file inputs.r42vars --out research.r42plan
```

`-var` values are HCL expressions, so collection values retain their types, for
example `-var 'regions=["us","jp"]'`. A non-JSON variable file uses ordinary
HCL assignments:

```hcl
topic   = "USD/JPY"
regions = ["us", "jp"]
```

Golden resolves variable sources during Plan and later sources override earlier
ones: `R42_VAR_<name>` environment variables, `r42.r42vars`, sorted
`*.auto.r42vars` files, and explicit CLI assignments. Within repeated CLI
assignments, later values from the same flag kind win. Applying a saved
`.r42plan` does not re-evaluate variables because their planned values are
already captured in that file.

## Research blocks

Each `research "static" "name"` block defines one known unit of work in the DAG
and owns persistent Collection, Collection QC, and closed Research sessions for
that unit's initial and repair turns, plus Final QC when configured. The subtype
forms part of the block address. Value references create
implicit dependencies between blocks; `depends_on` adds an explicit dependency
when no value is exchanged. `for_each` can expand one declaration into
independently addressed instances such as `research.static.name["key"]`.

A `research "dynamic" "name"` block is still one planned DAG node, but its
`tasks` attribute is a list of complete research configurations materialized
when that node begins Apply. Use it when an upstream result determines how many
isolated research workflows are needed:

```hcl
research "dynamic" "followups" {
  tasks = [
    for index, question in jsondecode(research.static.plan.result).questions : {
      model         = var.model
      system_prompt = "Investigate one accepted follow-up question."
      prompt        = question
      artifact = {
        report = {
          type        = "file"
          path        = "${block_wd()}/${index}/report.md"
          description = "Markdown answer for this follow-up question"
          required    = true
          non_empty   = true
        }
      }
      retry = null
      qc    = null
    }
  ]
}
```

There are no block-level defaults for dynamic tasks: each object carries the
same Collection, Collection QC, Research, tool, quota, artifact, retry, and
Final-QC fields needed by that task, including `final_qc_strictness` and all
`collection_*` fields.
Within a task, `retry` and `qc` are an object or `null`, while `artifact` is a
map of named objects. The `tasks` expression may be unknown in the saved Plan, but it
must be wholly known when the block starts Apply. Empty tasks succeed
immediately. Materialized tasks run concurrently under the same global and
module parallelism budgets as static research; one exhausted task failure fails
the dynamic block.

Set the optional block-level `serial = true` attribute to run the materialized
tasks one at a time in their original list order. Its default is `false`. This
setting prevents tasks within that dynamic block from overlapping; it does not
serialize the whole DAG, so other ready research blocks may still run under the
shared parallelism budget.

All task objects remain available through `research.dynamic.<name>.tasks` and
retain their declared fields plus resolved `artifact` values and, when a terminate
tool is configured, `result`. Tasks share the parent `block_wd()`; use a `for`
index or another stable key when separate subdirectories are required. The TUI
keeps the dynamic block as one DAG node but expands its task rows and research
count when materialization occurs.

### Dependencies

Dependencies determine when a block becomes ready during Apply. A block starts
only after all of its dependencies have completed successfully. Independent
ready blocks can run concurrently, subject to the configured parallelism.

| Kind | How it is declared | When to use it |
| --- | --- | --- |
| Implicit | Reference another block in an HCL expression. | The downstream block consumes an upstream value such as a result, artifact path, module output, provider, or tool ID. |
| Explicit | Set `depends_on` to a list of block traversals. | The downstream block must wait for an upstream side effect or completion, but does not consume one of its values. |

#### Implicit dependencies

Golden walks HCL expressions during Plan. A traversal such as
`research.static.collect.artifact` identifies the referenced block and
automatically adds it as a dependency. In this example,
`research.static.summarize` cannot start until `research.static.collect`
succeeds because its prompt consumes the collected artifact
path:

```hcl
research "static" "collect" {
  model         = "gpt-5.6-sol"
  system_prompt = "Collect primary evidence and preserve its source URLs."
  prompt        = "Write the evidence to ${block_wd()}/evidence.md."

  artifact "evidence" {
    type        = "file"
    path        = "evidence.md"
    description = "Collected primary evidence"
    required    = true
    non_empty   = true
  }
}

research "static" "summarize" {
  model         = "gpt-5.6-sol"
  system_prompt = "Summarize only the supplied evidence."
  prompt        = "Read ${research.static.collect.artifact.evidence.path} and summarize it."
}
```

The reference serves two purposes: it passes the absolute artifact path into the
prompt and creates the DAG edge `research.static.collect -> research.static.summarize`.
The same rule applies to expressions such as `research.static.collect.result`,
`module.sources.report`, `external_tool.fetch.id`, or
`model_provider.primary`. Merely writing an address or filesystem path as plain
text does not create a dependency because it is not an HCL traversal.

#### Explicit dependencies

Use `depends_on` when ordering matters but there is no value to reference. Its
value is a list of block traversals, not a list of strings:

```hcl
research "static" "notify" {
  model         = "gpt-5.6-sol"
  system_prompt = "Record workflow completion in the external audit system."
  prompt        = "Record that evidence collection completed successfully."

  depends_on = [research.static.collect]
}
```

Here `research.static.notify` waits for `research.static.collect`, but r42 does
not inject the collect block's result, artifacts, or transcript into the notify
session. If it needs any of those values, reference them directly and let r42
infer the dependency instead of adding a redundant `depends_on` entry.

Both forms are validated during Plan. A reference to an unknown block or a cycle
such as `research.static.a -> research.static.b -> research.static.a` fails
before any model session starts. If an upstream block fails during Apply, blocks
that depend on its successful completion are not run.

### Session fields

| Field | Required | Purpose |
| --- | --- | --- |
| `model_provider` | No | Default provider for every phase in this research workflow. It contains endpoint, authentication, transport, and retry defaults. When omitted, the Copilot SDK uses its default provider behavior. |
| `model` | Yes | Model identifier passed to the provider, for example `gpt-5.6-sol`. |
| `profile` | No | Copilot runtime profile used for model capabilities and built-in tools. Defaults to `model`; set it separately when a BYOK provider model should use another known model's runtime profile. |
| `reasoning_effort` | No | Non-empty provider-specific reasoning level. r42 passes it through without restricting the allowed names. |
| `system_prompt` | Yes | Instructions appended to r42's fixed research protocol system prompt. |
| `prompt` | No | Initial user task. When omitted, r42 sends a fixed start message. |
| `final_qc_strictness` | No | Final-QC semantic strictness: `strict`, `balanced`, or `brief`; defaults to `balanced`. This policy is authoritative over later task prompts, candidate instructions, and custom criteria. |
| `collection_model_provider` | No | Collection provider override. When omitted, Collection reuses `model_provider`. |
| `collection_tool_ids` | No | IDs of acquisition or snapshot-producing typed tools available only during Collection. This is where search and fetch tools belong. |
| `collection_mcp_tool_ids` | No | IDs from `mcp_server.<name>.tool_ids`, attached only to Collection. MCP tools cannot be used by QC, Research, `tool_use`, `terminate_tool_id`, or `tool_call_quota`. |
| `collection_mcp_resource_ids` | No | IDs from `mcp_server.<name>.resource_ids`, attached only to Collection. Selecting one automatically mounts the restricted `r42_read_mcp_resource` typed tool. |
| `tool_ids` | No | IDs of typed tools available only to the closed Research synthesis session. |
| `tool_call_quota` | No | `map(number)` of non-negative per-session call limits. It may name configured Collection or Research typed tools, the terminate tool, or Copilot built-ins. Collection and Research keep separate counters. |
| `terminate_tool_id` | No | Typed tool that must return an accepted response before the stage can finish. Its output must be string-compatible and becomes `research.static.<name>.result`. Without it, a normal assistant completion ends the stage. |
| `allowed_tools` | No | Tool allowlist shared by Collection and Research. Use SDK names for ordinary tools and `mcp_server.<name>.tool_ids[...]` for MCP tools. Mandatory r42 protocol tools are always added. |
| `disallowed_tools` | No | Tool denylist shared by Collection and Research. MCP IDs selected for Collection are translated to SDK MCP filter names. Research additionally blocks obvious network, shell, write/edit, glob, task, and user-input built-ins; read-only `view`, `grep`, `head`, and `tail` remain available. |
| `collection_skill_directories` | No | Skill roots available only during Collection. |
| `collection_skills` | No | Skills eagerly loaded only during Collection. |
| `collection_disabled_skills` | No | Skills disabled only during Collection. |
| `skill_directories` | No | Skill roots available only to closed Research; use `path.module` for module-owned skills. |
| `skills` | No | Skills eagerly loaded only during Research. Names come from `SKILL.md` frontmatter or the skill directory name. |
| `disabled_skills` | No | Skills disabled only during Research. |
| `collection_batch_size` | No | Number of newly registered unique snapshots that triggers `checkpoint_pending`. Defaults to `10`. |
| `max_collection_rounds` | No | Maximum acquisition rounds, including the initial Collection phase. Defaults to `10`. |
| `permission` | No | Tool permission policy. The current supported value and default is `approve_all`, which approves each otherwise valid tool request. |
| `max_protocol_attempts` | No | Maximum repair budget for rejected terminal calls or completed turns that omit the required terminal call. Defaults to `10`; a new QC revision round resets the budget. |
| `timeout` | No | Per-block deadline expressed as a Go duration such as `30m` or `2h`. It is bounded by the CLI and ancestor-module deadlines. |

Ordinary tool filters use SDK names. A typed tool's read-only `.id` is also its
SDK name, so the same ID can appear in `collection_tool_ids` or `tool_ids` and
in the filters. MCP is the exception: filters use its generated `mcp_tool_...`
ID in HCL, and r42 translates selected IDs to the SDK's
`mcp:<server>-<tool>` form. The mandatory registration, checkpoint, terminal,
and verdict tools cannot be disabled.

Every research block now starts in Collection, even when
`collection_tool_ids` is empty. Before any collection or artifact-writing tool
call, Collection must call `r42_set_information_needs` once to freeze the
complete search plan: 1-10 information needs, each with 1-5 objective stop
conditions. R42 assigns canonical IDs such as `NEED-001` and
`NEED-001-SC-001`. The plan is permanently frozen after submission: later
rounds may not add, edit, rename, delete, or split needs or conditions.

Collection registers useful material through `r42_register_artifact`, either
from a workspace file path or from the retained result of a configured typed
tool call, and ends each round through `r42_collection_checkpoint`. Each
Collection round must call `r42_collection_checkpoint` exactly once with one
`continue` or `stalled` disposition for every active need; it is the final
valid tool call of the round. `stalled` means Collection made a genuine search
effort for that need and found no productive next search action. A checkpoint
always contains every newly registered evidence artifact; an empty checkpoint
must explain why no new evidence is needed. After `collection_batch_size`
unique registrations, new acquisition calls pause until the checkpoint is
submitted, while in-flight completion, registration, and checkpoint calls
remain available.

After the checkpoint is accepted, Collection's non-read-only tools lock and
Collection QC runs. Collection QC assesses each active need against its frozen
stop-condition IDs and returns `sufficient` or `needs_more` with the remaining
unsatisfied condition IDs. Remaining IDs may only shrink between rounds; a
satisfied condition is never reopened. A need becomes terminal as
`search_stalled` after two consecutive rounds with no evidence progress, or as
`budget_exhausted` when `max_collection_rounds` is reached. Terminal needs are
frozen and never reopened. Once every need is terminal, closed Research begins
with the full `information_need_outcomes`; unresolved needs must be represented
as uncertainty, never as proven absence.

By default, Collection, Collection QC, closed Research, and Final QC all reuse
the research block's `model_provider`. Collection can override it with
`collection_model_provider`; Collection QC and Final QC can override it with
their nested `model_provider` fields. An omitted override never clears the
top-level provider.

This changes the meaning of `tool_ids`: acquisition tools that previously
appeared there must move to `collection_tool_ids`. Research uses only registered
evidence artifacts through r42's typed readers, its explicitly configured trusted typed
tools, controlled Markdown output, and an optional termination tool.

### `skill_directories`

A skill is a reusable set of instructions stored in a named directory. Each
entry in `skill_directories` points to a parent directory, and Copilot discovers
skills from its immediate subdirectories:

```text
skills/
  source-evaluation/
    SKILL.md
  citation-checking/
    SKILL.md
  experimental-browser/
    SKILL.md
```

For example, `skills/source-evaluation/SKILL.md` can contain:

```markdown
---
name: source-evaluation
description: Evaluate the authority, recency, and independence of research sources.
---

# Source evaluation

For every material claim:

1. Prefer primary sources over summaries.
2. Record the publication date and publisher.
3. Separate independent confirmation from repeated reporting of one source.
4. State any conflict, uncertainty, or missing evidence explicitly.
```

Use `path.module` to construct a module-owned absolute path. This remains valid
when the same module is installed below `.r42/modules`:

```hcl
research "static" "market" {
  model         = "gpt-5.6-sol"
  system_prompt = "Research the requested market and cite every material claim."
  prompt        = "Summarize the current market conditions."

  skill_directories = ["${path.module}/skills"]
  skills            = ["source-evaluation"]
  disabled_skills   = ["experimental-browser"]

  qc {
    criteria = {
      citations = "Every material claim must be supported by the captured sources."
    }

    skill_directories = ["${path.module}/skills"]
    skills            = ["citation-checking"]
    disabled_skills   = ["experimental-browser"]
  }
}
```

`skill_directories` makes skills discoverable. `skills` selects the named skills
that r42 eagerly loads into that session's custom agent, while
`disabled_skills` prevents a discovered skill from being used. Names must match
the `name` in the skill's YAML frontmatter; when `name` is absent, Copilot uses
the skill directory name.

Collection, Research, and Final QC are independent sessions. Collection uses
the `collection_*` skill fields, Research uses the unprefixed fields, and Final
QC uses fields nested in `qc`. Collection QC deliberately has fixed read-only
capabilities and no skill fields. r42 records evaluated skill paths in the Plan
but does not copy or validate their contents, so the directories and
`SKILL.md` files must remain readable when Apply starts.

### `retry`

A research block accepts at most one `retry` block. Its values override the
referenced model provider's retry policy; omitted fields continue to inherit the
provider value.

| Field | Purpose |
| --- | --- |
| `lifecycle_retries` | Additional attempts for session lifecycle operations. Provider default: `10`; `0` disables retries. |
| `model_call_retries` | Additional attempts for transient model-call failures. Provider default: `5`; `0` disables retries. |
| `interval_seconds` | Initial retry delay in seconds. Provider default: `10`. |
| `max_interval_seconds` | Maximum backoff delay in seconds. Provider default: `180`. |
| `error_message_regex` | Additional regular expressions that classify matching errors as transient. Built-in transient classifications remain active. |

Authentication errors, invalid schemas, unsupported model parameters, explicit
cancellation, and deadline expiry are permanent failures and are not retried.

### `artifact "name"` and `artifact("name")`

An artifact declares a file or directory that the session is expected to
produce. A research block may declare multiple uniquely named artifacts.
The declaration is also the source of the artifact metadata exposed to the
block's sessions and to downstream blocks.

| Field | Required | Purpose |
| --- | --- | --- |
| label `name` | Yes | Unique name used as `research.static.<block>.artifact.<name>` and by `artifact("name")`. |
| `type` | Yes | Either `file` or `directory`. |
| `path` | Yes | Expected path. Relative paths are based on `block_wd()`; absolute paths and `..` are allowed. |
| `description` | Yes | Semantic description of the artifact's contents. It is shown to models and helps them choose what to read. |
| `required` | No | When `true`, the path must exist before QC or block completion. Defaults to `false`. |
| `non_empty` | No | When `true`, a file must contain bytes or a directory must recursively contain a regular file. Defaults to `false`. |

Missing required artifacts are repairable issues sent back to the same research
session. Artifact metadata and normalized paths are also provided to QC.

> [!IMPORTANT]
> An `artifact` block declares a postcondition; it does not instruct the model to
> create the file or directory. Every required artifact must also be explicitly
> requested in `system_prompt` or `prompt`, including its path and expected
> format, or be created by a typed tool that the stage is required to call through
> `terminate_tool_id`. Otherwise the session may finish without producing it.
> r42 then reports the artifact problem back to the same session for repair; if
> the model continues to ignore it, this can look like a loop until
> `max_protocol_attempts` is exhausted and the block fails.

Inside a static research block, `artifact("name")` returns the declared
artifact object. Its fields are `id`, `name`, `kind`, `type`, `path`,
`description`, `required`, and `non_empty`:

```hcl
research "static" "collect" {
  model = "gpt-5.6-sol"
  system_prompt = "Save source material under ${artifact("sources").path}."

  artifact "sources" {
    type        = "directory"
    path        = "sources"
    description = "Markdown copies of retained primary sources"
    required    = true
  }
}
```

Use `.path` for prompt text or a typed-tool input that expects a path, and
`.id` for a typed-tool input that expects an artifact ID. The `type` and
`description` fields tell the model whether it should read a file directly or
enumerate a directory first. `artifact("name")` refers only to an artifact
declared by the current block or dynamic task; use
`research.static.<block>.artifact.<name>` (or an `import_artifact` declaration)
when consuming another block's artifact. A reference to another block creates
the normal implicit DAG dependency. The function is resolved during Plan for
static blocks and after dynamic task materialization for dynamic tasks; its
run-scoped ID is assigned during Apply.

### Built-in artifact typed tools

r42 mounts these typed tools automatically. They are available in addition to
user-configured `go_tool` and `external_tool` tools, and they never accept
filesystem paths where an artifact ID is required. The current block or task's
authorization, including imported artifacts and discovered files inside
authorized directories, is enforced by every read operation.

| Tool | Available in | Purpose |
| --- | --- | --- |
| `r42_list_artifacts` | Collection, Collection QC, Research, Final QC | List authorized artifacts and their IDs, paths, types, and descriptions. Call this when an ID is uncertain. |
| `r42_list_artifact_files` | Collection, Collection QC, Research, Final QC | Enumerate regular files inside an authorized directory; use each returned child ID with a reader. |
| `r42_read_artifact` | Collection, Collection QC, Research, Final QC | Read a bounded page by ID. Use `offset_bytes` and `next_offset_bytes` to continue when `truncated` is true. |
| `r42_search_artifact` | Collection, Collection QC, Research, Final QC | Search one authorized artifact with a Go RE2 regular expression and return matched text plus a submit-ready `quote_ref` covering the requested context. |
| `r42_search_artifacts` | Collection, Collection QC, Research, Final QC | Search all authorized readable artifacts, including imported artifacts and directory children; each match includes its artifact ID and submit-ready `quote_ref`. |
| `r42_read_artifact_json_schema` | Collection, Collection QC, Research, Final QC | Infer the JSON shape of one complete JSON artifact. |
| `r42_query_artifact_json` | Collection, Collection QC, Research, Final QC | Run a read-only jq query such as `.claims[0].id` against a JSON artifact. |
| `r42_write_markdown` | Collection and Research | Write content to a declared file artifact using `artifact_id`; it does not accept a path or artifact name. |
| `r42_save_artifact` | Collection | Save Markdown source material, add its `source` header, register it, and return `path` plus `artifact_id`. The source can be a URL or any non-empty identifier. |
| `r42_register_artifact` | Collection | Register an existing workspace file or retained typed-tool result; optional `source` and `description` can supply missing metadata. Do not call it after `r42_save_artifact`. |
| `r42_collection_checkpoint` | Collection | Submit newly registered evidence and one continue/stalled disposition for every active need to Collection QC. |
| `r42_collection_qc_verdict` | Collection QC | Assess each active need as `sufficient` or `needs_more` with only remaining unsatisfied condition IDs. |
| `r42_qc_expand_quote` | Final QC | Expand a trusted quote by exactly one line before and after for read-only semantic review; returns a new submit-ready `quote_ref`. |
| `r42_qc_verdict` | Final QC, when configured | Return `pass` or `revise_research` with semantic QC issues. |

Collection is the only open-world phase: it can acquire evidence through the
configured collection tools and save/register it. Collection QC, Research, and
Final QC can use the read-only `view`, `grep`, `head`, and `tail` file tools in
addition to the built-in artifact tools. Closed Research still cannot use
shell, PowerShell, write/edit tools, or network acquisition. Built-in typed
tools return structured rejection issues so the model can correct an
invocation without guessing paths or IDs.

### `collection_qc`

Collection QC is mandatory and persistent. An optional `collection_qc` block
overrides its model settings and semantic sufficiency criteria; when the block
is absent, Collection QC inherits the Research model settings and uses r42's
default sufficiency criterion.

| Field | Required | Purpose |
| --- | --- | --- |
| `criteria` | No | Non-empty `map(string)` of semantic evidence-sufficiency checks. Omission uses the default criterion. |
| `model_provider` | No | Collection-QC provider override; otherwise reuses the research block's top-level `model_provider`. |
| `model` | No | Collection-QC model override; otherwise inherits Research. |
| `reasoning_effort` | No | Collection-QC reasoning override; otherwise inherits Research. |
| `permission` | No | Permission override; otherwise inherits Research. |
| `retry` | No | One retry block layered over the selected Collection-QC provider policy and then the research-level retry override. |

Collection QC can list and read registered evidence artifacts but cannot acquire or
modify evidence. It reviews the current checkpoint and every active need's
frozen stop-condition IDs and calls `r42_collection_qc_verdict` with one
per-need assessment: `sufficient` lists no unsatisfied conditions, or
`needs_more` lists the remaining IDs. Remaining condition IDs may only shrink
between rounds; a satisfied condition is never reopened. A valid verdict
advances the reviewed cursor. `needs_more` starts another Collection round when
budget remains; when the configured limit is exhausted, the remaining needs
become `budget_exhausted` and the full outcomes are carried into closed
Research instead.

### `qc` (Final QC)

A research block accepts at most one `qc` block. It creates a persistent Final
QC session that reviews the closed Research candidate, artifacts, and registered
snapshots. Omitting `qc` completes the block after Research succeeds.

| Field | Required | Purpose |
| --- | --- | --- |
| `criteria` | Yes | Non-empty `map(string)`. Each key is a stable criterion ID and each value is the concrete review instruction given to QC. |
| `model_provider` | No | Final-QC provider override; otherwise reuses the research block's top-level `model_provider`. |
| `model` | No | QC model override; otherwise inherits the research model. |
| `reasoning_effort` | No | QC reasoning override; otherwise inherits the research value. |
| `tool_ids` | No | Typed tools available only to QC. Research tools are not inherited. |
| `tool_call_quota` | No | QC-only `map(number)` of non-negative call limits. Typed-tool ID keys must also appear in this QC block's `tool_ids`; ordinary keys limit Copilot built-in tools. |
| `allowed_tools` | No | QC SDK tool allowlist. The research allowlist is not inherited. |
| `disallowed_tools` | No | Additional Final-QC denylist. Final QC always blocks obvious network, shell, write/edit, glob, task, and user-input built-ins; read-only `view`, `grep`, `head`, and `tail` remain available. |
| `skill_directories` | No | Skill roots available only to QC; research skill roots are not inherited. |
| `skills` | No | Skills selected only for QC. |
| `disabled_skills` | No | Skills disabled only for QC. |
| `permission` | No | QC permission override; otherwise inherits the research permission. |
| `max_qc_rounds` | No | Maximum number of QC evaluations, including the first evaluation. Defaults to `5`; at most `max_qc_rounds - 1` QC-triggered research revisions can occur. |
| `retry` | No | One retry block using the same fields as research. It is layered after the selected Final-QC provider policy and the research-level retry override. |

`final_qc_strictness` defaults to `balanced`, permitting a reasonable one-step
inference grounded in cited facts. `strict` requires source facts to materially
match their evidence and analysis to be strictly derivable, while `brief` is
intended for concise reports and focuses on material contradictions, invented
premises, misleading certainty, and unsupported precision. The configured
strictness is authoritative over any later task prompt, candidate instruction,
or custom criterion.

Collection, Research, and Final QC quotas use independent counters, even when
sessions use the same tool. Typed-tool calls consume quota only after their arguments pass schema
validation and the tool returns an accepted response; execution errors and
`accepted = false` responses release the reservation. Built-in calls reserve
quota immediately before execution and release it when the SDK reports tool
failure. A limit of `0` disables the named tool for the session. r42 adds typed
limits to the typed-tool descriptions and built-in limits to the session system
prompt. Once a limit is exhausted, r42 denies the call before execution and
tells the model not to retry it during that session.

For each QC round, r42 sends one JSON context document to the QC session. It
contains the original task, the complete `criteria` map, the current candidate
result, and every declared artifact's normalized path and constraints. It does
not contain the research transcript. For example, the criteria in the example
below reach QC in this form:

```json
{
  "criteria": {
    "value": "Read the report and verify that its USD/JPY value matches the cited source.",
    "date": "Verify that the observation date is identified correctly.",
    "citations": "Verify that every reported rate is supported by a source URL."
  }
}
```

The map does not create three QC sessions or three independently executed
checks. One Final QC session receives the whole map and assesses every entry
before calling the mandatory `r42_qc_verdict` typed tool. It returns one of
two decisions:

- `pass`: no issues; complete the block.
- `revise_research`: one or more issues; revise from existing snapshots.

Final QC can never reopen Collection or adjudicate whether information needs
have sufficient coverage. Collection QC owns that decision, and the complete
`information_need_outcomes` are passed only to Research. Final QC reviews only
claims actually present in the candidate; when one exceeds or misrepresents its
cited evidence, it may return the candidate so Research deletes or narrows that
claim. It cannot reject missing claims or demand additional evidence.

For example:

```json
{
  "decision": "revise_research",
  "issues": [
    {
      "code": "value",
      "message": "The report says 151.2, but the cited snapshot says 150.8.",
      "path": "D:/project/r42/.r42/runs/run-.../blocks/.../report.md",
      "repair_hint": "Replace the rate with 150.8 and preserve the snapshot URL."
    }
  ]
}
```

Use short, stable criterion keys such as `value`, `date`, and `citations`, and
write values as observable pass/fail requirements rather than broad goals. An
issue should normally reuse the failed criterion key as its `code`, but this is
currently a convention: r42 requires a non-empty issue `code` and `message` but
does not enforce that the code appears in `criteria`. `path` and `repair_hint`
are optional.

#### When Final QC finds issues

r42 validates every non-pass verdict contains at least one issue and every
issue has a non-empty `code` and `message`. `revise_research` sends those issues
to the persistent closed Research session so an unsupported existing claim can
be deleted or narrowed. Final QC cannot reject absent coverage, reopen
Collection, or request additional evidence. If a non-pass decision arrives on
the `max_qc_rounds` evaluation, r42 starts no unreviewable follow-up work and
fails the block with `final qc rounds exhausted`.

The following example asks closed Research to write an exchange-rate report and
gives Final QC three explicit checks:

```hcl
research "static" "exchange_rate" {
  model = "gpt-5.6-sol"
  system_prompt = <<-EOT
    Use current, authoritative sources. Cite the source URL for every reported
    exchange-rate value and distinguish the observation date from publication
    dates.
  EOT
  prompt = <<-EOT
    Find the latest USD/JPY exchange rate and write a Markdown report to
    ${block_wd()}/report.md. Include the observation date, the rate, and source
    URLs.
  EOT

	collection_batch_size = 10
	# max_collection_rounds is omitted, so Collection uses the default 10-round cap.

  collection_qc {
    criteria = {
      source_coverage = "The snapshots contain a current rate, observation date, and authoritative source URL."
    }
  }

  artifact "report" {
    type        = "file"
    path        = "report.md"
    description = "USD/JPY exchange-rate research report"
    required    = true
    non_empty   = true
  }

  qc {
    criteria = {
      value = "Read ${block_wd()}/report.md and verify that its USD/JPY value matches the cited source."
      date  = "Verify that the report states when the rate was observed and does not present a publication date as the observation date."
      citations = "Verify that every reported rate has a source URL and that the source supports the claim."
    }

    max_qc_rounds = 3
  }
}
```

After Collection and Collection QC finish, closed Research writes and validates
`report.md`, then Final QC receives the candidate, registered evidence artifacts, and the
artifact's normalized path. With
`max_qc_rounds = 3`, the sequence is bounded as follows:

| QC evaluation | Candidate being checked | Result when QC rejects it |
| --- | --- | --- |
| 1 | Initial research result | Issues trigger research revision 1. |
| 2 | Revision 1 | Issues trigger research revision 2. |
| 3 | Revision 2 | The block fails; there is no revision 3. |

Thus the setting allows at most three QC evaluations and two QC-triggered
research revisions. A pass during any evaluation completes the block
immediately.

## Modules

A module is a reusable directory of `*.r42.hcl` configuration and its supporting
files. It can package research blocks, QC policies, typed tools, variables,
outputs, and nested modules behind one declared boundary. Its variables are the
inputs supplied by the caller; its outputs are the public values that the caller
can reference. Internal research blocks are still included in the complete Plan
and executed during Apply, while internal tools remain private unless the module
explicitly exports their IDs.

```hcl
module "source_review" {
  source = "./modules/source_review"
  topic  = "evidence quality"
}

output "review" {
  value = module.source_review.report
}
```

Modules keep repeated research stages and tool definitions in one place. A team
can reuse the same source-gathering, claim-verification, or report-generation
subgraph in many investigations without copying its prompts, schemas, QC rules,
and helper programs. The boundary also makes dependencies explicit and limits
what callers can access to declared outputs.

`r42 init <source>` resolves the complete root configuration package and copies
it to `<cwd>/.r42/config`, excluding nested `.r42` and `.git` directories. A
local directory is read directly. Any non-local source locator is downloaded
through [go-getter v2.2.3](https://github.com/hashicorp/go-getter/tree/v2.2.3),
following Terraform's
[getter-backed module source conventions](https://developer.hashicorp.com/terraform/language/block/module#specify-the-location-of-module-source-files).
This includes GitHub shorthand, `git::` URLs, repository subdirectories, and
supported HTTP archives:

```powershell
r42 init 'github.com/acme/research-config//r42?ref=v1.2.3'

# Initialize the chokepoint example directly from its Git repository subdirectory
r42 init 'git::https://github.com/lonegunmanb/r42.git//docs/examples/chokepoint'
```

Terraform Registry address and version negotiation are not implemented; use a
concrete go-getter locator.

The same source rules apply to each module's literal `source` attribute. Modules
are installed under `<cwd>/.r42/modules`. Remote root sources are fetched into a
private staging directory before activation, so a failed download leaves the
current initialized project available. `<cwd>/.r42/state.json` records the
active local source path or a sanitized remote locator, the active snapshot
directory, and the outputs from the latest successful Apply. Its source identity
is always a SHA-256 value; URL credentials, fragments, and query parameters
other than `ref` are not persisted. A successful Init replaces this state and
invalidates outputs from the previous configuration.
Plan and unsaved-plan Apply accept no configuration directory and read only the
initialized snapshot. Root `path.module` is therefore `<cwd>/.r42/config`;
module blocks use their canonical installed directories. `cwd()` remains the
directory where the CLI was started. Run `r42 init` again after changing the
source configuration.

## MCP servers

`mcp_server` attaches native MCP tools through the official GitHub Copilot SDK.
Every server requires an explicit non-empty tool allowlist and exactly one
`http` or `stdio` transport:

```hcl
mcp_server "jin10" {
  tools   = ["get_quote", "get_kline"]
  resources = ["quote://codes"]
  timeout = "30s"

  http {
    url              = "https://mcp.jin10.com/mcp"
    bearer_token_ref = "J10_API_KEY"
  }
}

research "static" "market" {
  model         = "gpt-5.6-sol"
  system_prompt = "Collect current market evidence."
  collection_mcp_tool_ids = [mcp_server.jin10.tool_ids["get_quote"]]
  collection_mcp_resource_ids = [mcp_server.jin10.resource_ids["quote://codes"]]
  allowed_tools             = [mcp_server.jin10.tool_ids["get_quote"]]
}
```

HTTP supports `url`, optional `headers`, and `bearer_token_ref`. Stdio supports
`command`, optional `args`, literal `env`, environment-backed `env_refs`, and
`working_directory`. Timeout defaults to `30s` and must be between `1s` and
`5m`. `resources` contains exact MCP resource URIs and is optional. r42 leaves
MCP protocol negotiation to the SDK; the selected resources are read through
the SDK's experimental host RPC by the generated `r42_read_mcp_resource` tool.
The display name (`jin10` above) is separate from the runtime identity: r42 uses
the MCP block's canonical path (for example `mcp_server.jin10`, including its
module path) as the SDK server name. This keeps same-named blocks from different
modules distinct for both native MCP tools and resources, even when their
endpoints happen to be identical.

Each selected tool has a deterministic `mcp_tool_<server>__<tool>_<uuid>` ID.
These IDs are deliberately distinct from `go_tool`, `external_tool`, and
`starlark_tool` IDs. They select connected tools through
`collection_mcp_tool_ids` and may also appear in tool filters.

`collection_mcp_tool_ids` controls which MCP tools are connected to Collection.
When `allowed_tools` is omitted, those selected tools remain available. When
`allowed_tools` is configured, it is a strict allowlist: an MCP tool is callable
only if its generated ID appears in both fields. r42 translates that explicit
intersection to the SDK's `mcp:<server>-<tool>` filter name. Mandatory r42
protocol tools are still added automatically; selected MCP tools are not.

Each selected resource has a deterministic
`mcp_resource_<server>__<uri>_<uuid>` ID. A resource ID is valid only in
`collection_mcp_resource_ids`; it is never accepted in `allowed_tools`. When a
session has selected resources, `r42_read_mcp_resource` is automatically added
to its tool declarations and, when `allowed_tools` is non-nil, to that SDK
allowlist as well. The tool accepts only the declared resource IDs and returns
the MCP `contents` array, preserving text/blob and MIME metadata.

## Typed tools

r42 supports two kinds of typed tool: `go_tool` and `external_tool`. Both expose
a JSON Schema to the model, receive validated structured arguments, and return a
common `ToolResponse` envelope. A rejected call contains actionable issues and
is returned to the session for repair; a process, I/O, cancellation, or protocol
failure fails the block instead of being disguised as a model mistake.

Every typed tool has a deterministic read-only `id` derived from its canonical
block address. Collection selects acquisition tools through
`collection_tool_ids`; closed Research and Final QC select their trusted tools
through `tool_ids`. Research can require an accepted call to one tool before
completion by setting `terminate_tool_id`:

```hcl
collection_tool_ids = [external_tool.search.id]
tool_ids = [go_tool.build_report.id]
terminate_tool_id = go_tool.submit_report.id
```

### `tool_use`

`tool_use` binds one configured typed tool to a research stage. It is the
preferred form when a tool needs a mixture of workflow-owned inputs and values
that the model must construct from authorized artifacts. A configured
`tool_use` replaces `tool_ids` and `terminate_tool_id` for that research block:
do not combine the two forms.

In a static research block, declare one nested block per tool. `input` fixes
fields from HCL and removes them from the JSON Schema shown to the model.
`input_from_agent` leaves named fields for the model and supplies a semantic
description plus the authorized artifact sources it should read. A field has
exactly one owner: it cannot appear in both maps.

```hcl
research "static" "write_report" {
  model         = "gpt-5.6-sol"
  system_prompt = "Write the report from the supplied evidence."

  artifact "report" {
    type        = "file"
    path        = "report.md"
    description = "Final Markdown report"
    required    = true
    non_empty   = true
  }

  import_artifact "evidence" {
    desc    = "Primary evidence collected by the upstream block"
    sources = [research.static.collect.artifact.evidence]
  }

  tool_use "submit_report" {
    tool_id   = go_tool.submit_report.id
    terminate = true

    input = {
      report_id = artifact("report").id
      topic     = var.topic
    }

    input_from_agent = {
      summary = {
        desc    = "Evidence-based report summary"
        sources = [research.static.collect.artifact.evidence]
      }
    }

    validation {
      condition     = length(trimspace(input.summary)) > 0
      error_message = "summary must not be empty"
    }
  }
}
```

`tool_id` is required. `terminate = true` is optional, but at most one
`tool_use` can terminate a stage. Its accepted string-compatible output becomes
the block's `.result`. `validation` blocks are optional; they can reference
only the special `input` object, run after fixed values are injected, and fail
the invocation when their condition is false.

Dynamic tasks use the same singular name and field semantics, expressed as a
map because each task is an HCL object rather than a block body:

```hcl
tool_use = {
  submit_report = {
    tool_id   = go_tool.submit_report.id
    terminate = true
    input = {
      report_id = artifact("report").id
      topic     = var.topic
    }
    input_from_agent = {
      summary = {
        desc    = "Evidence-based report summary"
        sources = [research.static.collect.artifact.evidence]
      }
    }
    validation = [{
      condition     = "length(trimspace(input.summary)) > 0"
      error_message = "summary must not be empty"
    }]
  }
}
```

The dynamic spelling is `tool_use`, not `tool_uses`. Its `validation` entries
are objects because dynamic task configuration is data. When a source is a
directory artifact, r42's generated prompt tells the model to call
`r42_list_artifact_files` and then read child IDs; for file artifacts it tells
the model to use the artifact readers or JSON query tools. Fields not listed in
`input_from_agent` are constructed from the current task's declared artifacts
when the tool schema requires them.

### `go_tool`

A `go_tool` keeps its implementation directly in HCL as an inline Go source
fragment. It declares `Input`, `Output`, and a typed `Invoke` function. Plan
parses and type-checks the source, validates its signature and cty compatibility,
and permits only Go standard-library imports. Apply generates a small executable,
compiles it once per r42 process, and starts a fresh child process for each tool
call.

Use a `go_tool` for portable validation and transformation logic that should be
planned together with the research contract: validating a citation record,
normalizing a structured result, enforcing allowed enum combinations, or
accepting the final payload that ends a stage. Its implementation has no
third-party Go dependencies and needs no separately deployed script.

### `external_tool`

An `external_tool` invokes an existing executable and declares its types with
HCL `input_type` and `output_type` constraints. For each call, r42 starts the
configured `program`, writes one JSON argument object to stdin, and expects one
JSON `ToolResponse` document on stdout. Its working directory defaults to the
calling block's workspace, so the process can create task-specific artifacts;
module-owned programs can be located with `path.module`.

The HCL declarations and the program's JSON protocol are one contract:

- `input_type` describes the complete JSON value that the Python program reads
  from stdin. Every field name, nested object, array, and primitive value must
  have the declared shape. r42 applies `optional(...)` defaults and validates
  the value before starting the program.
- `output_type` describes the JSON value inside the stdout response's `output`
  field. It does not describe the surrounding `ToolResponse` envelope. For a
  successful call, the program writes
  `{"accepted":true,"output":<value matching output_type>}`. For a repairable
  rejection it writes `{"accepted":false,"issues":[...]}` and omits `output`.
- stdout must contain exactly one JSON document. Missing fields, additional
  incompatible fields, wrong primitive types, or a different nested structure
  violate the contract and fail the tool call.

The equivalent Python runtime types are:

| HCL type | JSON representation | Python value |
| --- | --- | --- |
| `object(...)`, `map(...)` | object | `dict` |
| `list(...)`, `set(...)`, `tuple(...)` | array | `list` |
| `string` | string | `str` |
| `number` | number | `int` or `float` |
| `bool` | `true` or `false` | `bool` |

In the example below, `json.load(sys.stdin)` must therefore produce a Python
`dict` with `claim`, `sources`, and `minimum_confidence` matching `input_type`.
The `dict` assigned to `response["output"]` must contain `supported`, `verdict`,
`confidence`, and `citations` matching `output_type`. The two HCL schemas and
these two Python data structures must be changed together.

An external tool can be implemented in any language. The only requirement is
that the program can read and write JSON and constructs values that exactly
match its declared schemas. r42 uses the same primitive and collection types
documented in HashiCorp's
[Terraform type system](https://developer.hashicorp.com/terraform/language/expressions/types),
including `string`, `number`, `bool`, `list`, `set`, `map`, `tuple`, and nested
`object` values.

For example, a claim-checking program can accept one claim plus a list of source
excerpts and return a verdict with typed citations:

```hcl
external_tool "check_claim" {
  description = "Check one claim against supplied source excerpts."
  program     = ["python", "${path.module}/check_claim.py"]

  input_type = object({
    claim = string
    sources = list(object({
      url     = string
      excerpt = string
    }))
    minimum_confidence = optional(number, 0.8)
  })

  output_type = object({
    supported  = bool
    verdict    = string
    confidence = number
    citations = list(object({
      url   = string
      quote = string
    }))
  })
}
```

The following `check_claim.py` is a minimal implementation. Its lexical matcher
is intentionally simple; a production tool can replace `check()` with a model,
database, or domain-specific verifier without changing the r42 protocol.

```python
#!/usr/bin/env python3

import json
import re
import sys
from typing import Any


def tokens(value: str) -> set[str]:
    return set(re.findall(r"[a-z0-9]+", value.lower()))


def reject(code: str, message: str, path: str) -> dict[str, Any]:
    return {
        "accepted": False,
        "issues": [
            {
                "code": code,
                "message": message,
                "path": path,
                "repair_hint": "Provide a non-empty claim and at least one source excerpt.",
            }
        ],
    }


def check(arguments: dict[str, Any]) -> dict[str, Any]:
    claim = arguments["claim"].strip()
    sources = arguments["sources"]
    minimum = float(arguments.get("minimum_confidence", 0.8))
    if not claim:
        return reject("empty_claim", "claim must not be empty", "claim")
    if not sources:
        return reject("missing_sources", "at least one source is required", "sources")

    claim_tokens = tokens(claim)
    if not claim_tokens:
        return reject("invalid_claim", "claim must contain searchable terms", "claim")
    ranked = sorted(
        (
            (len(claim_tokens & tokens(source["excerpt"])) / len(claim_tokens), source)
            for source in sources
        ),
        key=lambda item: item[0],
        reverse=True,
    )
    best_score, best_source = ranked[0]
    confidence = round(min(0.99, 0.5 + 0.49 * best_score), 2) if best_score else 0.0
    supported = confidence >= minimum
    citations = []
    if best_score:
        citations.append({"url": best_source["url"], "quote": best_source["excerpt"]})

    return {
        "accepted": True,
        "output": {
            "supported": supported,
            "verdict": "supported" if supported else "not_supported",
            "confidence": confidence,
            "citations": citations,
        },
    }


def main() -> int:
    try:
        arguments = json.load(sys.stdin)
        response = check(arguments)
        json.dump(response, sys.stdout, separators=(",", ":"))
        sys.stdout.write("\n")
        return 0
    except (KeyError, TypeError, ValueError, json.JSONDecodeError) as error:
        print(f"invalid tool request: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
```

For one call, the program receives this JSON on stdin:

```json
{
  "claim": "The release added parallel DAG execution.",
  "sources": [
    {
      "url": "https://example.com/release-notes",
      "excerpt": "The release added parallel DAG execution for independent ready vertices."
    }
  ],
  "minimum_confidence": 0.8
}
```

It must write one response matching `ToolResponse[output_type]` to stdout:

```json
{
  "accepted": true,
  "output": {
    "supported": true,
    "verdict": "supported",
    "confidence": 0.99,
    "citations": [
      {
        "url": "https://example.com/release-notes",
        "quote": "The release added parallel DAG execution for independent ready vertices."
      }
    ]
  }
}
```

Use an `external_tool` when the implementation belongs in Python, another
language, an existing CLI, or a program with dependencies that an inline
`go_tool` cannot import. Designing the schema and generating the small JSON
protocol adapter are well-suited to an AI coding agent: provide the desired
research contract and target language, then let the agent implement the parser,
response envelope, and tests. r42 still validates the declared types during Plan
and every actual value during Apply. The executable itself is required only at
Apply time, keeping runtime dependencies explicit.

## Workspaces

Each applied research block gets a workspace below
`<cwd>/.r42/runs/<run-id>/blocks`; `block_wd()` returns that block-specific
absolute path, while `path.module` returns the absolute directory containing the
block's initialized configuration. Both functions use `/` path separators on
every operating system.

See [the examples](docs/examples/README.md) for a basic workflow and a module
that exports typed external tools. The complete language and execution contract
is documented in [the design specification](docs/design.md).

## Safety

Saved plans and `--debug` logs may contain credentials, prompts, transcripts,
reasoning, and tool data. They are stored unencrypted under the paths selected
by the user or under `<cwd>/.r42/runs`; do not publish them or commit them to
source control. The live progress UI also displays assistant, reasoning, and
tool activity supplied by the model SDK, so terminal recordings and shared
screens should be treated as sensitive.

## Development

Run the repository checks with:

```powershell
go vet ./...
go test ./... -count=1
golangci-lint run
```

## License

r42 is licensed under the [MIT License](LICENSE).
