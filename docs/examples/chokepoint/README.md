# Dynamic chokepoint research

This example adapts the supply-chain bottleneck workflow from
`ai-institute/cmd/chokepoint` to r42. It starts with an exploratory brainstorm,
checks the proposed decomposition through five independent evidence tracks,
selects structural chokepoints, discovers companies for the resulting nodes,
and independently assesses every discovered company.

The workflow is intentionally not an investment screener. It first establishes
where control exists in the physical and qualification graph. Only then does it
ask which public companies control or critically supply those exact nodes.

## Research flow

```text
brainstorm hypotheses
        |
        v
five parallel graph tracks
  product structure and BOM
  manufacturing, packaging, and testing
  specialized equipment and tooling
  materials, chemicals, and consumables
  qualification and integration lock-in
        |
        v
graph reconciliation and chokepoint selection
        |
        | produces N evidence-backed chokepoint nodes
        v
dynamic candidate discovery: N independent tasks
        |
        | produces M company hypotheses across those nodes
        v
dynamic candidate assessment: M independent tasks
        |
        v
final report and final QC
```

The five graph tracks use `research "static" "graph_track"` with `for_each`
because their identities are known during Plan. Candidate discovery and
candidate assessment use `research "dynamic"` because neither the number of
selected chokepoints nor the number of supported companies is known then.

The dynamic blocks remain single nodes in the Golden DAG. When a dynamic block
begins Apply, r42 resolves its complete `tasks` value and runs its members as
independent research sessions under the same global parallelism budget used by
static research. The TUI can display those materialized tasks without changing
the planned DAG.

## Why two dynamic stages

`research.dynamic.discover_candidates.tasks` is derived from the accepted
terminate-tool result of `research.static.select_chokepoints`:

```hcl
tasks = [
  for index, chokepoint in
  jsondecode(research.static.select_chokepoints.result).chokepoints : {
    # one complete research configuration per actual chokepoint
  }
]
```

Every discovery task returns zero or more candidates. The second dynamic block
uses a nested `for` expression and `flatten` to turn those results into one task
per company:

```hcl
tasks = flatten([
  for discovery_index, discovery in
  research.dynamic.discover_candidates.tasks : [
    for candidate_index, candidate in
    jsondecode(discovery.result).candidates : {
      # one complete research configuration per actual candidate
    }
  ]
])
```

These expressions are allowed to be unknown during Plan. They become wholly
known only after their upstream terminate tools have succeeded during Apply.
An empty chokepoint or candidate list succeeds immediately and contributes zero
internal tasks. If one materialized task fails after retry and QC repair are
exhausted, its dynamic research block fails as a whole.

Each dynamic task retains its own `result` and `artifacts` in the parent
block's `tasks` list. That lets the next stage iterate over structured results
without inventing fake DAG addresses.

## Evidence and typed completion

Four inline Go typed tools define the stage boundaries:

| Tool | Accepted artifact | Important enforced relationships |
| --- | --- | --- |
| `submit_track_evidence` | `track-evidence.json` | Finding IDs, quote IDs, exact quotes, fetched snapshots, and bidirectional finding-to-quote references |
| `submit_chokepoint_chain` | `chokepoints.json` | Five reviewed tracks, unique graph nodes, valid edge endpoints, node-bound ranked chokepoints, and evidence finding IDs |
| `submit_candidates` | `candidates.json` | Exact assigned node, public-company identity, direct control or critical-supply relationship, maximum candidate count, and fetched evidence |
| `submit_candidate_scorecard` | `scorecard.json` | Exact candidate/node binding, eight required 0-5 factors, control mechanism, peer alternatives, falsification conditions, and fetched evidence |

The first three tools return their accepted JSON as the research `result`, so a
downstream HCL expression can use `jsondecode(...)`. They also write the same
payload to the declared artifact. QC reads both the JSON artifact and every
referenced Markdown snapshot. A research session cannot finish by returning a
JSON code block or prose; its terminate-tool call must pass host validation.

The final report is Markdown rather than a terminate-tool result. Its artifact
declaration is paired with a prompt that explicitly requires the file, and its
QC reads the complete chain, every discovery result, and every scorecard.

## Source tools and optional Perplexity quotas

The local `pplx_tools` module follows `docs/examples/multi-step` and exports two
inline Go typed-tool IDs:

- `pplx_pro_search` searches current sources with the Perplexity Search API.
- `pplx_fetch` fetches one selected URL. The researcher supplies an absolute
  `snapshot_dir`; the tool derives `snapshot-<url-hash>.md` and returns its
  absolute `snapshot_path`.

The `use_pplx` variable defaults to `false`. In that mode, researchers use the
built-in `web_search` and `web_fetch` tools, receive neither Perplexity typed
tool, and have no Perplexity quota. Set `use_pplx = true` to give every static
research block and every materialized dynamic task both Go tools. r42
then disables the built-in `web_search` and `web_fetch` tools for those research
sessions and independently configures:

```hcl
tool_call_quota = {
  (module.pplx_tools.pplx_fetch_tool_id) = 10
}
```

Therefore the quota is ten successful fetches per research session, not ten for
the whole run. Candidate tasks do not consume another candidate's quota.
Malformed or failed calls do not permanently consume quota. The total possible
Perplexity cost grows with the number of selected nodes and candidates, so use
`max_candidates_per_chokepoint` and the CLI `--parallelism` deliberately.

Unlike the multi-step smoke test, this workflow may fetch several sources in
one block. Each prompt gives the researcher a `snapshot_dir` derived from that
block's `block_wd()`. The tool hashes the final fetched URL to produce a stable
filename, so repeated fetches of the same URL overwrite the same snapshot.
Dynamic tasks share their parent block's `block_wd()`, so their prompts include
the task index in `snapshot_dir` to keep concurrent task files apart.

When enabled, the Go tools require `PPLX_API_KEY`. r42 compiles their inline Go
source and derives the input and output schemas from the `Input` and `Output`
types.

## Run the example

Install r42 and ensure GitHub Copilot CLI is installed:

```powershell
go install github.com/lonegunmanb/r42/cmd/r42@latest
```

The checked-in variable file selects DeepSeek through BYOK and currently sets
`use_pplx = true`. The model provider key is always required:

```powershell
$env:DEEPSEEK_KEY = Read-Host -MaskInput "DeepSeek API key"
```

With `use_pplx = true`, also provide the Perplexity API key:

```powershell
$env:PPLX_API_KEY = Read-Host -MaskInput "Perplexity API key"
```

Set `use_pplx = false` to use the built-in `web_search` and `web_fetch` tools
instead; in that mode `PPLX_API_KEY` is not needed.

Install the local module before planning:

```powershell
r42 init ./docs/examples/chokepoint
```

Inspect and save the immutable plan:

```powershell
r42 plan `
  -var-file ./docs/examples/chokepoint/research.r42vars `
  --out ./chokepoint.r42plan
```

Then apply it:

```powershell
r42 apply --timeout 2h --parallelism 5 ./chokepoint.r42plan
```

The convenience form plans and applies the initialized snapshot in one command:

```powershell
r42 apply `
  --timeout 2h `
  --parallelism 5 `
  -var-file ./docs/examples/chokepoint/research.r42vars
```

`research.r42vars` is analogous to `terraform.tfvars`. Change `topic`,
`market`, `max_candidates_per_chokepoint`, model, provider endpoint, or secret
environment-variable reference without editing the research graph. Toggle
`use_pplx` to switch between built-in web tools and the Perplexity typed tools.

## Output contract

The root outputs expose:

- the exploratory `brainstorm.md`;
- the reconciled `chokepoints.json` graph;
- one `candidates.json` path per selected chokepoint;
- one `scorecard.json` path per discovered company;
- the final `report.md`.

All run artifacts live under the run directory printed by r42. Static blocks
receive their own block workspace. Dynamic tasks share the dynamic parent's
workspace and use deterministic index directories for their declared JSON
artifacts.

This example requires r42's `research "dynamic"` block. It also serves as an
end-to-end acceptance configuration for unknown-at-Plan tasks, nested dynamic
fan-out, shared parallelism, task-level quotas, task-level QC, and task-level
TUI observability.
