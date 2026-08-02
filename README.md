# r42

r42 is an HCL-based planner and execution engine for reproducible AI research
workflows. A configuration describes a directed acyclic graph (DAG) of research
sessions, quality-control sessions, typed tools, artifacts, modules, variables,
and outputs. r42 plans the complete graph before it starts any model session,
then applies the immutable plan through the official GitHub Copilot SDK.

## Why r42

Long-running research is easier to inspect and repeat when orchestration is
separate from model execution. The Plan phase resolves references, expands the
DAG, validates typed tool schemas, and captures module outputs before paid or
stateful work begins. Apply then executes only the saved graph, runs independent
research blocks concurrently, and fails fast when a block cannot complete.

This design provides:

- reviewable JSON plans before execution;
- explicit and implicit dependencies between research tasks;
- one persistent research session, plus an optional QC session, per block;
- typed inline Go and external-process tools with stable IDs;
- Terraform-style modules installed with `r42 init`;
- isolated per-block workspaces for reports, snapshots, and other artifacts;
- opt-in JSONL debug logs containing prompts, reasoning, messages, and tool events.

## Build

r42 requires Go 1.25 or newer and an authenticated GitHub Copilot CLI when the
default provider is used.

```powershell
git clone https://github.com/lonegunmanb/r42.git
cd r42
go build -o r42.exe ./cmd/r42
```

## Example

r42 loads every `*.r42.hcl` file in the target directory. The following
configuration creates one research session:

```hcl
research "summary" {
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
r42 plan --directory . --out research.r42plan
r42 apply research.r42plan
```

`r42 apply .` is the convenience form that plans the directory, prints the plan
as JSON, and immediately applies it. The overall Apply timeout defaults to one
hour and can be changed with `--timeout`.

Modules are installed below `<cwd>/.r42/modules`. Each applied research block
gets a workspace below `<cwd>/.r42/runs/<run-id>/blocks`; `block_wd()` returns
that block-specific absolute path, while `path.module` returns the absolute
directory containing the block's initialized configuration. Both functions use
`/` path separators on every operating system.

See [the examples](docs/examples/README.md) for a basic workflow and a module
that exports typed external tools. The complete language and execution contract
is documented in [the design specification](docs/design.md).

## Safety

Saved plans and `--debug` logs may contain credentials, prompts, transcripts,
reasoning, and tool data. They are stored unencrypted under the paths selected
by the user or under `<cwd>/.r42/runs`; do not publish them or commit them to
source control.

## Development

Run the repository checks with:

```powershell
go vet ./...
go test ./... -count=1
golangci-lint run
```

## License

r42 is licensed under the [MIT License](LICENSE).
