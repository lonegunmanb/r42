# r42 examples

The `basic` example is a single research session without a terminal tool. It
uses the official GitHub Copilot SDK default provider, so Apply requires a local
GitHub Copilot CLI installation with an authenticated account.

From the repository root:

```powershell
go run ./cmd/r42 plan ./docs/examples/basic --out ./basic.r42plan
go run ./cmd/r42 apply ./basic.r42plan
```

The same directory can be planned and applied in one command:

```powershell
go run ./cmd/r42 apply ./docs/examples/basic
```

This block has no terminal tool, so a successful Apply produces no output
value. The session completes when the model finishes its response.

## Multi-step research

The `multi-step` example fans out into three independent research directions,
then runs a synthesis session after all three subreports exist. Its explicit
`depends_on` list forms the fan-in edge, while the blocks exchange reports
through a shared directory inside the retained run.

```powershell
go run ./cmd/r42 plan ./docs/examples/multi-step --out ./multi-step.r42plan
go run ./cmd/r42 apply ./multi-step.r42plan --parallelism 3
```

Apply prints the absolute path of the final report when the DAG completes.
