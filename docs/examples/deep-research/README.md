# Planner-driven deep research

This example answers one topic through a planner-created research matrix. The
caller normally supplies only `topic`; a static planner research session
decomposes it into three task groups and submits the plan through a typed Go
tool. An optional `research_plan = list(string)` can bypass that planner when
the caller already has the subquestions: every supplied subquestion is sent to
the parallel dynamic group, while the two serial groups remain empty.

```text
static planner
      |
      +--> parallel_deep_dive              (all tasks may run concurrently)
      +--> independent_serial_deep_dive    (tasks run one at a time)
      |        both groups start after the planner
      |
      +--> final_serial_deep_dive           (waits for both groups)
                    |
                    v
          conflict detection and resolution
                    |
                    v
              final Markdown synthesis
```

The three dynamic blocks are still single nodes in the planned Golden DAG.
When Apply reaches one of them, r42 evaluates its `tasks` list, materializes
one research session per element, and gives those sessions the same global
parallelism budget as every other research block. `serial = true` only changes
the scheduling inside that dynamic block. An empty group succeeds immediately.
If one materialized task fails after retries and QC repair, its whole dynamic
block fails.

## Run it

Install r42 once:

```powershell
go install github.com/lonegunmanb/r42/cmd/r42@latest
```

From the repository root, initialize the example, then create and apply a
saved plan:

```powershell
r42 init ./docs/examples/deep-research
r42 plan `
  -var-file ./docs/examples/deep-research/research.r42vars `
  --out ./deep-research.r42plan
r42 apply ./deep-research.r42plan
```

The initialized configuration can also be applied directly after planning:

```powershell
r42 apply `
  -var-file ./docs/examples/deep-research/research.r42vars `
  --parallelism 10
```

The checked-in `research.r42vars` selects DeepSeek, so provide the referenced
key before Apply:

```powershell
$env:DEEPSEEK_KEY = Read-Host -MaskInput "DeepSeek API key"
```

Edit `research.r42vars` to change the topic, model, or reasoning effort. To
provide a fixed plan, add `research_plan = ["...", "..."]`; a non-empty value
skips the planner and creates one parallel task per list element. A null or
empty value keeps the planner-driven three-group flow. When the planner is
used, it decides how many tasks belong to each group. It must use globally
unique, filesystem-safe task IDs and provide a subquestion plus concrete
instructions for every task. If a later task needs an earlier artifact, the
planner must say so in that task's instructions. The researcher is explicitly
told to locate and inspect the exact upstream file with available search/file
tools such as `grep` before relying on it.

## Evidence pipeline

This example requires Python 3 for the local `audit_synthesis` external tool;
the research and planning tools remain inline Go tools compiled by r42.

`save_snapshot` is the typed boundary for source capture: it accepts complete
Markdown content and writes only absolute `.md` paths under the current block's
`snapshots/` directory. `submit_research_plan` rejects an empty plan, duplicate task IDs, blank
subquestions, and vague instructions. Its accepted JSON becomes
`research.static.plan["default"].result`, which the three dynamic blocks decode
with HCL `for` expressions when no caller-supplied plan is present.

Every deep-dive task must call `submit_knowledge`. The typed tool validates
unique knowledge and quote IDs, bidirectional claim-to-quote references, valid
source URLs, existing Markdown snapshot paths, and confidence
values before writing `knowledge.json` under that task's workspace. Before
submitting knowledge, the researcher must call `save_snapshot` for every
source it uses, storing the complete fetched material under
`<block_wd>/snapshots/`; each quote records its exact `snapshot_path`. Its QC
session then reads each claim, quote, snapshot, and artifact independently;
failed QC returns issues to the same research session for repair up to
`max_qc_rounds`.

Each materialized deep-dive task has its own `web_fetch = 20` entry in
`tool_call_quota`. The limit is twenty successful built-in fetch calls for that
task's research session; failed fetches do not consume it.

The conflict resolver reads all knowledge artifacts from all three dynamic
groups, records resolved and unresolved contradictions in `resolution.json`,
and has its own QC loop. The final synthesizer reads every knowledge artifact
and the resolution, then writes `report.md` with source and quote references.
Its QC calls `audit_synthesis` once per round for bounded mechanical checks:
invented or unused quote IDs, source-table URL mappings, snapshot existence,
and quote text presence. Text matching first tries the original bytes, then
line-ending equivalence, paragraph-preserving whitespace equivalence, and
Unicode NFC equivalence. These modes never fold case, punctuation, numbers,
hyphens, or paragraph boundaries. The complete audit is written beside the
report as `synthesis-audit.json`; only compact statistics and issues enter the
model context. The QC model remains responsible for semantic support,
unsupported extrapolation, plan coverage, and faithful conflict handling.

The `knowledge_paths`, `conflict_resolution_path`, and `report_path` outputs
expose the resulting artifact locations. Each dynamic task also retains its
own `result` and `artifacts` values in the parent block's `tasks` list, so
downstream HCL can iterate over structured results without inventing extra DAG
addresses.

All sessions use `model_provider.primary`, whose complete configuration is
provided by the `model_provider` object variable. The example uses inline Go
typed tools and Copilot's built-in web tools; Apply still requires an installed
Copilot CLI, while BYOK means the selected provider does not require a GitHub
Copilot subscription.
