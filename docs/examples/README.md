# r42 examples

The `basic` example is a reasoning-only research workflow without a terminal
tool. Collection freezes a minimal information-needs plan and submits an empty
checkpoint because no external evidence is needed; closed Research answers from
the task statement. It uses the official
GitHub Copilot SDK default provider, so Apply requires a local GitHub Copilot
CLI installation with an authenticated account.

From the repository root:

```powershell
go run ./cmd/r42 init ./docs/examples/basic
go run ./cmd/r42 plan --out ./basic.r42plan
go run ./cmd/r42 apply ./basic.r42plan
```

The initialized artifact can be planned and applied in one command:

```powershell
go run ./cmd/r42 apply
```

This block has no terminal tool, so a successful Apply produces no output
value. After mandatory Collection and Collection QC, closed Research completes
when the model finishes its response.

## Multi-step research

The `multi-step` example demonstrates module-owned external acquisition tools. The
`pplx_tools` child module declares a Python search process and fetch process,
then exports their generated tool IDs as outputs. The root research block exposes
those IDs only through `collection_tool_ids`; Collection searches python.org and
saves a Markdown artifact before closed Research produces the result.

The example provider reads `DEEPSEEK_KEY`. The Python tools read
`PPLX_API_KEY` and require Python 3; they otherwise use only the Python standard
library. Initialize the module before planning:

```powershell
go run ./cmd/r42 init ./docs/examples/multi-step
go run ./cmd/r42 plan --out ./multi-step.r42plan
go run ./cmd/r42 apply ./multi-step.r42plan
```

The root configuration is copied to `.r42/config` and the initialized module to
`.r42/modules/pplx_tools` below the directory where the CLI was started.
`path.module` lets the module invoke its copied Python file. During Apply,
`block_wd()` resolves to the research block's absolute workspace and the fetch
tool writes `artifact.md` there. Apply prints that artifact path when the DAG
completes.

## Deep-research matrix

The `deep-research` example accepts an overall topic and an arbitrary
`list(string)` research plan through `-var-file`. It expands the plan into N
independent subquestion research workflows. A required inline Go typed tool
makes each workflow submit a structured knowledge graph of claims and exact
quotes, and each materialized workflow has Collection QC plus optional Final QC
to check evidence sufficiency and the submitted claims.

After all subquestions pass QC, a conflict-resolution block compares the
knowledge artifacts and records resolved or preserved contradictions. A final
block synthesizes a Markdown report and runs one last QC pass. See
[`deep-research/README.md`](deep-research/README.md) for the variable file and
commands.

## SecJury DCF

The `secjury` example runs one `collection_only` LLM session to acquire inputs,
calculate a complete frozen `dcf-model.v2` payload through the isolated
Starlark calculator, and submit it. It then expands the original 20-member
persona roster into concurrent `research_only` review tasks before a final
`research_only` synthesis writes `report.md`. The builder switches between
built-in web tools and PPLX finance/pro/fetch tools with `use_pplx`; no QC
sessions are created. See [`secjury/README.md`](secjury/README.md) for the
exact parity boundary and run commands.

## Morning financial briefing

The `morning` example turns overnight markets, macro/policy releases, and
industry news into a Chinese-language breakfast briefing for ordinary readers.
Three Collection tracks feed a mechanically validated frozen packet; macro,
sentiment, and strategy reviewers then inspect the same packet before a typed
tool renders the final Markdown with mandatory diversification, leverage, and
cost warnings. See [`morning/README.md`](morning/README.md).

## S3-compatible upload

[`s3-folder`](s3-folder/README.md) shows a configuration-only `s3_provider`
and a DAG-managed `s3_folder` that uploads a research workspace to AWS S3 or
Alibaba Cloud OSS. It documents environment credential references, run-root
source confinement, version-aware rollback, and exclusion of sensitive debug
events.
