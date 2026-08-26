# SecJury DCF with parallel persona review

This example reproduces the earlier Go SecJury research shape in r42:

```text
one DCF builder
      |
      v
frozen dcf-model.v2 + sources
      |
      +--> Buffett juror --------+
      +--> Munger juror ---------+
      +--> Graham juror ---------+
      +--> ... 20 dynamic tasks -+--> synthesis --> report.md
```

The DCF builder is one persistent Collection agent. It acquires inputs and
builds one complete JSON object with exactly `model` and `sources` in the same
session, writes the frozen model and source artifacts through
`submit_dcf_model`, and cannot delegate modeling to dynamic tasks. Its
`dcf-model` and `yahoo-finance` skills (including the `yf` script) are copied
byte-for-byte, modulo line endings, from
`D:/project/ai-institute/internal/skills`. The normalized `dcf-model` SHA-256 is
`ddee513c690e6f3f762ab143ce5d8d839824ea985914e17468a3d9ad43d6eb74`.

The copied generation behavior includes:

- `dcf-model.v2` with the original company, assumptions, historical,
  projection, valuation, sensitivity, and source fields;
- 3-5 historical periods and 5-10 projection periods;
- mid-year discounting unless the evidence supports another convention;
- perpetuity-growth terminal value, equity bridge, implied return, and an odd
  square WACC/terminal-growth sensitivity grid;
- the original monotonic `dcf-progress.v1` checkpoint contract;
- no spreadsheet output and no prose-wrapped final JSON.

The DCF stage uses `collection_only`: the same session acquires evidence with
the copied Yahoo Finance accelerator, calculates, and submits the frozen model.
All derived numeric values use the configured isolated Starlark calculator;
raw source retrieval and extraction do not count as calculations. This preserves
the original v2 process while allowing the model to collect a missing raw input
when a calculation exposes one. Running the Yahoo skill requires Python 3.10+
and `uv`, matching the original implementation.

Public-source acquisition has two modes. The default `use_pplx=false` uses
r42's built-in `web_search` and `web_fetch`. With `use_pplx=true`, those two
built-in tools are disabled and the DCF Collection session instead receives,
in priority order:

- `pplx_finance_search` for quotes, statements, valuation data, earnings,
  estimates, peers, and ownership;
- `pplx_pro_search` for filings, investor-relations sources, general discovery,
  and finance-search fallback;
- `pplx_fetch` to retain each selected URL as a registered evidence artifact.

This mirrors the earlier Go SecJury tool set while using the hardened fetch
artifact handling from the `chokepoint` example. PPLX mode requires
`PPLX_API_KEY`. `pplx_tool_call_quota` optionally limits successful
`pplx_fetch` calls in the DCF Collection session; its default `null` leaves the
limit to the Collection stop condition.

After `build_dcf` completes, `review_dcf` materializes the original default
roster: 17 named investor/operator personas plus evidence, assumption, and
valuation reviewers. Each is a `research_only` task and `serial = false` allows
all jurors to run concurrently under r42's global `--parallelism` budget. Every
juror receives the same frozen model, cannot acquire external facts, cannot edit
or recalculate the DCF, and must cite exact JSON paths in its findings.

The final `research_only` synthesizer receives the frozen model and every
structured opinion. Neither builder, jurors, nor synthesis configures Collection
QC or Final QC: their terminal typed tools enforce the mechanical contracts and
the downstream stages cannot repair missing acquisition.

`submit_dcf_report` uses the original deterministic Markdown rendering shape:
headline and decision, key findings, limitations, full DCF tables, sensitivity
matrix, source table, and every juror opinion. It writes both `report.json` and
`report.md`.

## Run it

Install r42 and authenticate the local GitHub Copilot CLI, then run from the
repository root:

```powershell
$env:OPENROUTER_API_KEY = "..."
r42 init ./docs/examples/secjury
r42 plan `
  -var 'target="Microsoft MSFT NASDAQ"' `
  -var 'valuation_date="2026-08-26"' `
  -var 'model="openai/gpt-5.6"' `
  -var 'model_provider={ api_key_ref = "OPENROUTER_API_KEY" }' `
  --out ./secjury.r42plan
r42 apply ./secjury.r42plan --parallelism 20 --timeout 6h
```

`model` selects the LLM name, while `model_provider` selects its transport,
endpoint, credentials, headers, and retry behavior. Its default protocol is
OpenAI-compatible OpenRouter; replace the object fields to use another
provider. `api_key_ref` names an environment variable, so credentials never
need to appear in a variable file or plan.

To use Perplexity tools instead of the built-in web tools:

```powershell
$env:PPLX_API_KEY = "..."
r42 plan `
  -var 'target="Microsoft MSFT NASDAQ"' `
  -var 'valuation_date="2026-08-26"' `
  -var 'model_provider={ api_key_ref = "OPENROUTER_API_KEY" }' `
  -var 'use_pplx=true' `
  --out ./secjury-pplx.r42plan
```

The initialized configuration can also be applied directly with the same
variables. Reduce `--parallelism` to cap concurrent juror sessions without
changing the roster or their output order.

Apply exposes three outputs:

- `dcf_model_path`: the immutable model all jurors reviewed;
- `jury_opinion_paths`: structured opinions in roster order;
- `report_path`: the final `report.md`.

The persona confidence field is preserved from the original contract. It is a
juror's qualitative self-assessment, not a calibrated probability or an
independent market estimate.
