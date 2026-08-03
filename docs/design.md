# r42 Research DAG DSL Design

Status: current implementation contract

This document records the implemented r42 execution contract. It is the
normative source for the current implementation. When this document and an
example disagree, the explicit rules in this document win.

## 1. Purpose

r42 is a high-level HCL DSL and execution engine for research DAGs. Azure/golden
provides configuration evaluation, implicit dependency discovery, Plan hooks,
and the Apply block protocol. A leaf `research` block owns exactly one GitHub
Copilot SDK research session and, when configured, exactly one QC session.

The Go implementation must use the official upstream
`github.com/github/copilot-sdk/go` module directly. It must not introduce a
third-party fork or adapter.

### Goals

- Describe static research DAGs with typed inputs, outputs, tools, and artifacts.
- Produce an immutable Plan before Apply starts.
- Keep research and QC sessions persistent for the lifetime of one block.
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
saved nested Plans and never reparses module source. `r42 apply <directory>` is
a convenience form that performs a complete in-memory Plan before Apply.

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
- `go_tool`: a typed tool implemented by inline Go source.
- `external_tool`: a typed tool implemented by a child process.
- `research`: a research session and an optional nested QC session.
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

  tool_ids = [
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

Every `research` block is exactly one logical research session. A nested `qc`
block adds exactly one logical QC session. Each session is created once and
reused through all of its turns.

Research session fields include:

- `model_provider`: optional provider reference; omission uses the SDK's
  default provider behavior.
- `model`: required.
- `profile`: optional Copilot runtime model identity, defaulting to `model`.
  With BYOK, r42 sends `profile` as `ProviderConfig.ModelID` for capability and
  built-in-tool selection, while `model` remains `ProviderConfig.WireModel` and
  is sent to the inference provider.
- `reasoning_effort`: an arbitrary non-empty string passed through unchanged.
- `system_prompt`: required.
- `prompt`: optional.
- `tool_ids`: typed tool IDs, normally selected through a tool block's read-only `id` attribute.
- `typed_tool_call_quota`: optional per-session call limits keyed by configured
  typed tool ID.
- `terminate_tool_id`: optional typed tool ID.
- `allowed_tools` and `disallowed_tools`: SDK tool-name strings.
- `skill_directories`, `skills`, and `disabled_skills`.
- `permission`, defaulting to `approve_all`.
- `max_protocol_attempts`, defaulting to 10.
- `timeout`, with no default.
- retry overrides.

r42 prepends its fixed protocol system prompt and appends the author's
`system_prompt`. The optional `prompt` is the starting user message. When it is
absent, r42 sends a fixed start message. r42 does not validate whether a model
supports a given reasoning effort; unsupported parameters are surfaced by the
provider, with HTTP 400 failing immediately.

### Tool policy

There is no default allowlist. When present, `allowed_tools` first narrows the
available set. `disallowed_tools` is then applied and always wins. `ask_user` is
disabled by default because DAG execution is unattended.

`approve_all` automatically approves every otherwise valid tool call.
Terminate and QC verdict tools are protocol tools: r42 always registers them and
configuration cannot exclude them.

Each typed tool receives a deterministic, SDK-safe ID from its canonical block
address. Research and QC snapshots store only those IDs; the Plan stores the
complete ID-to-definition registry used during Apply. String-only SDK filter
fields can use the `id` attribute directly or the compatibility function:

```hcl
go_tool.finish.id
tool_name(go_tool.finish) # same generated tool_go_tool_finish_<uuid> value
```

Tools declared in a module remain private unless the module exposes the tool ID
as a direct string output. A parent can pass that output to `tool_ids`; Plan then
imports only the corresponding exported definition into the parent registry.

Research and QC configure typed-tool quotas independently. A successful call
consumes one unit only after its arguments pass schema validation and the tool
returns an accepted response. Execution errors and `accepted = false` responses
roll back the reservation. A zero quota disables that tool for the session.

`one(collection)` follows Terraform's zero-or-one convention: an empty list,
set, or tuple returns null, one element returns that element, and more than one
element is an error.

The generated ID is also the SDK registration name. It has the form
`tool_<go_tool|external_tool>_<name>_<uuid>`, is at most 64 characters, and is
derived from the canonical address so module instances cannot collide.

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

## 8. Research Completion Protocol

Without `terminate_tool_id`, a normal assistant completion ends research and the
block exposes no direct `result` value. Artifacts remain available.

With `terminate_tool_id`, the block completes only after an accepted call. The
terminal tool's output type must be string-compatible. Its optional output
becomes the block's optional string `result`; complex results belong in files in
the block workspace.

When a turn ends without the required terminal call, r42 sends a user message
that explicitly requires the model to call it. An `accepted = false` response is
returned as issues so the same session can repair its arguments and call again.
Every rejected terminal call and every completed turn without a terminal call
consumes one protocol attempt. `max_protocol_attempts` defaults to 10.

Required artifact failures are also repairable issues returned to the research
session. A new QC revision round resets the protocol-attempt budget.

## 9. QC Protocol

The optional QC session receives only:

- The original task, including the research `system_prompt` and optional
  `prompt`.
- `criteria`, declared as `cty.Map(cty.String)` and containing at least one item.
- The candidate optional result string.
- Declared artifact metadata and paths.

It does not receive the research transcript. It reuses one QC session across all
rounds. Criteria keys are stable identifiers suggested as issue codes, but r42
does not require `Issue.code` to equal a criterion key.

QC inherits only the effective provider, model, reasoning effort, retry policy,
and permission policy. It does not inherit research tools, skills, allowlist, or
denylist. QC configuration may explicitly customize its own policy, subject to
the mandatory rules below.

r42 injects a mandatory typed verdict tool. QC must call it with either pass or
one or more issues. The executor trusts pass. Issues are returned to the research
session for revision. `max_qc_rounds` defaults to 10 and can be overridden.

By default QC disables shell execution, file writes, sub-agents, and `ask_user`;
web search/fetch and read-only artifact inspection remain available. Authors may
relax the denylist, but can never enable `ask_user` or disable the verdict tool.

The state machine is:

```text
R1 -> validate protocol/artifacts -> Q1
       ^                              |
       |--------- issues -------------|

R2 -> validate protocol/artifacts -> Q2 -> ... -> pass
```

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
`r42 init [DIRECTORY]` recursively discovers module blocks in `*.r42.hcl`
files. A local source (`./`, `../`, or an absolute filesystem path) is copied;
other source strings are passed to go-getter v2.2.3. Installed modules live
under `<cwd>/.r42/modules` according to their canonical module address:

```text
module.a                      -> <cwd>/.r42/modules/a
module.a.module.b.module.c    -> <cwd>/.r42/modules/a/b/c
```

Existing installations are reused. `r42 init --upgrade [DIRECTORY]` refreshes
them through a staging directory so a failed copy or download leaves the
previous installation intact. Source cycles fail Init.

Plan requires every declared module to be initialized and parses only its
canonical installed directory; it does not read the original source directory.
Apply creates a new nested r42 executor over the saved Plan and never reparses
module source.

Every block can read `path.module`. It is an absolute `/`-normalized path to the
directory containing that block: the root configuration directory for root
blocks, or the corresponding `.r42/modules/a/b/c` directory for nested blocks.

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
permit before Apply begins and holds it until research and QC sessions have been
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
`30m` and `2h`. CLI overall `--timeout` defaults to `1h`; research and module
timeouts have no default. The effective deadline is the earliest of the overall
deadline, all ancestor module deadlines, and the research block deadline.

## 13. Cancellation and Session Cleanup

Fail-fast cancellation follows this order:

1. Cancel the root context and stop scheduling new research blocks.
2. Propagate cancellation through nested module executors.
3. Terminate active tool child-process trees.
4. Close research and QC sessions.
5. Release parallelism permits and flush debug files.

The original error remains primary; cleanup errors are additional diagnostics.
After a session has been created, lifecycle retry repeats the current operation
on that same session. r42 never silently replaces it. A lost or unrecoverable
session fails the block.

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

By default r42 does not persist complete research/QC transcripts, prompts, tool
arguments, or tool results. With CLI `--debug`, files in the run directory
contain:

- Complete r42 and user system prompts.
- Every user and assistant message.
- Complete research and QC transcripts.
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
r42 init [<directory>] [--upgrade]
r42 plan [-d|--directory <directory>] [--out <file.r42plan>]
r42 apply <file.r42plan>
r42 apply <directory>
```

`init` defaults its directory to `.`. It reuses installed modules unless
`--upgrade` is present.

`plan` defaults `--directory` to `.`. It always prints the Plan to stdout and
only writes a saved Plan file when `--out` is present.

`apply` prints the immutable Plan JSON to stdout before execution starts. After
a successful Apply, it prints the output values as a second JSON document.
Progress is always written to stderr. `--ui=auto` selects the Bubble Tea TUI
when stdin and stderr are interactive terminals of at least 50x12 and falls
back to the line-oriented REPL renderer for redirected output, CI, `TERM=dumb`,
or smaller terminals. `--ui=tui` requires those capabilities; `--ui=repl`
forces stable text events.

Both renderers consume the same in-memory event stream used by the debug
recorder. The stream exists even without `--debug`; that flag controls sensitive
event persistence, not live state production. The projector builds its initial
state from the fully expanded saved Plan, including module child Plans, then
tracks block lifecycle, research/QC phase, assistant reasoning/message deltas,
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

The CLI also exposes:

- `--parallelism`, default 10.
- Overall `--timeout`, default `1h`.
- `--debug`, disabled by default.
- `--ui`, default `auto`; accepted values are `auto`, `tui`, and `repl`.
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

Plan deliberately does not validate:

- External program existence or executability.
- Whether a model supports a reasoning-effort value.
- Provider-specific tool-name restrictions beyond r42's generated 64-character IDs.
- External content hashes or future file contents.

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
