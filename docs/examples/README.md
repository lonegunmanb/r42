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
