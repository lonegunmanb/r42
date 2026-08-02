# r42 examples

The `basic` example is a single research session without a terminal tool. It
uses the official GitHub Copilot SDK default provider, so Apply requires a local
GitHub Copilot CLI installation with an authenticated account.

From the repository root:

```powershell
go run ./cmd/r42 plan --directory ./docs/examples/basic --out ./basic.r42plan
go run ./cmd/r42 apply ./basic.r42plan
```

The same directory can be planned and applied in one command:

```powershell
go run ./cmd/r42 apply ./docs/examples/basic
```

This block has no terminal tool, so a successful Apply produces no output
value. The session completes when the model finishes its response.

## Multi-step research

The `multi-step` example demonstrates module-owned external tools. The
`pplx_tools` child module declares a Python search process and fetch process,
then exports their generated tool IDs as outputs. The root research block uses
those IDs to search python.org and save a Markdown snapshot in its block
workspace.

The example provider reads `DEEPSEEK_KEY`. The Python tools read
`PPLX_API_KEY` and require Python 3; they otherwise use only the Python standard
library. Initialize the module before planning:

```powershell
go run ./cmd/r42 init ./docs/examples/multi-step
go run ./cmd/r42 plan --directory ./docs/examples/multi-step --out ./multi-step.r42plan
go run ./cmd/r42 apply ./multi-step.r42plan
```

The initialized module is copied to `.r42/modules/pplx_tools` below the
directory where the CLI was started. `path.module` lets the module invoke its
copied Python file. During Apply, `block_wd()` resolves to the research block's
absolute workspace and the fetch tool writes `snapshot.md` there. Apply prints
that artifact path when the DAG completes.
