# Deep-research matrix

This example turns a user-supplied research plan into a reproducible r42 DAG:

```text
N independent subquestion researchers
        | each submits typed knowledge + exact quotes
        | each passes an independent QC loop
        v
cross-subquestion conflict detection and resolution
        | passes its own QC loop
        v
final Markdown synthesis and final QC
```

It follows the high-level matrix flow in `ai-institute`: execute planned
research subtasks, build evidence-backed knowledge, resolve conflicts, then
synthesize. The difference is that there is no model-driven planning stage.
The caller supplies the plan as `research_plan = list(string)` in a variable
file, so Plan knows the complete DAG before any session starts.

From the repository root, inspect and save the plan without starting Copilot:

```powershell
go run ./cmd/r42 plan `
  --directory ./docs/examples/deep-research `
  -var-file ./docs/examples/deep-research/research.r42vars `
  --out ./deep-research.r42plan
```

Then apply the immutable plan:

```powershell
go run ./cmd/r42 apply ./deep-research.r42plan
```

The directory can also be planned and applied directly:

```powershell
go run ./cmd/r42 apply `
  -var-file ./docs/examples/deep-research/research.r42vars `
  ./docs/examples/deep-research
```

Before Apply, provide the API key referenced by the selected provider. The
checked-in `research.r42vars` overrides the variable default with DeepSeek and
therefore expects:

```powershell
$env:DEEPSEEK_KEY = Read-Host -MaskInput "DeepSeek API key"
```

Edit `research.r42vars` to replace the topic, add or remove subquestions, or
select another provider model identifier. The optional `system_prompt` variable
overrides the default analyst instructions for every deep-dive instance. Each
list element becomes one `research.static.deep_dive[...]` instance. Those
instances are independent and can run in parallel; the conflict resolver waits
for every `knowledge.json`, and the final synthesizer waits for the validated
`resolution.json`.

All sessions use `model_provider.primary`. Its complete configuration is
exposed as the `model_provider` object variable. `variables.r42.hcl` defaults to
OpenRouter and `OPENROUTER_API_KEY`, while the supplied `research.r42vars`
selects DeepSeek and `DEEPSEEK_KEY`. Override the object in a variable file to
change the endpoint, protocol, transport, headers, authentication mode, or
retry policy. Only one of `api_key`, `api_key_ref`, `bearer_token`, and
`bearer_token_ref` may be non-null.

The `submit_knowledge` typed tool rejects malformed evidence graphs before a
subquestion can finish. It requires unique knowledge and quote IDs, at least one
quote per claim, no unused quotes, valid source URLs, exact quote text, and a
high/medium/low confidence. It writes the accepted payload to that block's
`knowledge.json`. The nested QC session then opens every source and checks every
knowledge item and quote individually. A failed QC verdict returns issues to
the same research session for repair, up to `max_qc_rounds`.

No module initialization or `external_tool` executable is required. The typed
tools use inline Go and the research sessions use Copilot's built-in web tools.
Apply requires an installed Copilot CLI, but the default BYOK provider does not
require a GitHub account or Copilot subscription. Override the provider and
model variables together when using a different API.
