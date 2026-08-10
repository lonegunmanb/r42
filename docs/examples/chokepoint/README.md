# Evidence-led chokepoint research

This example adapts the supply-chain bottleneck workflow from
`ai-institute/cmd/chokepoint` to r42. It builds a coverage-complete product and
supply-chain model, identifies structural continuity risks, and then tests
which public companies actually control or critically supply those nodes.

The workflow is deliberately a buyer continuity-risk analysis, not an
investment screener. A supplier relationship may be commercially attractive
without being a customer-side chokepoint, and a chokepoint does not by itself
make the supplier an attractive investment. The final report therefore contains
no composite score, stock ranking, valuation conclusion, or recommendation.

## Research flow

```text
official-source baseline
        |
        v
brainstorm + independent scope-challenger QC
        |
        v
five parallel evidence-ledger tracks
  product structure and BOM
  manufacturing, packaging, and testing
  specialized equipment and tooling
  materials, chemicals, and consumables
  qualification and integration lock-in
        |
        v
explicit cross-ledger conflict reconciliation
        |
        v
coverage-complete graph + structural chokepoint selection
        |
        v
dynamic candidate discovery (one task per chokepoint)
        |
        v
dynamic candidate assessment (one task per surviving company)
        |
        v
final evidence reconciliation
        |
        v
report + claim manifest + semantic QC
```

The five graph tracks use `research "static" "graph_track"` with `for_each`
because their identities are known during Plan. Candidate discovery and
assessment use `research "dynamic"`: their task counts are unknown until the
upstream typed tools return accepted results during Apply.

A dynamic block remains one Golden DAG node. At Apply time r42 resolves its
complete `tasks` value and runs the materialized tasks under the same global
parallelism budget as static research. An empty task list succeeds immediately.
If one materialized task fails after its retry and QC budget is exhausted, the
dynamic block fails as a whole.

## Scope before selection

The workflow does not claim that public evidence can prove an objectively
exhaustive supply chain. It enforces completeness against a reviewed scope
contract. Before the five tracks begin, `primary_source_baseline` captures the
latest applicable filings, official product documents, regulator records, and
official statements as of the fixed `as_of_date`. This prevents a later source
from silently changing the time boundary and makes first-party evidence the
starting point rather than an afterthought.

The brainstorm then writes `brainstorm.md` and submits `scope.json`. The scope
declares:

- the focal product and materially different variants;
- expected components and lifecycle stages;
- upstream and downstream stopping boundaries;
- one coverage item for every expected component and stage;
- exactly one of the five tracks responsible for each coverage item;
- unresolved scope questions rather than invented facts.

An independent QC session challenges this proposed inventory against the
official baseline. It is instructed to find omitted purchase categories,
services, module branches, equipment, materials, and qualification steps, not
merely confirm the brainstorm author's own checklist.

For a topic such as one vendor's DDR5 memory, upstream decomposition stops at
production equipment and input materials: a lithography tool is not decomposed
into optical subassemblies, and a silicon wafer is not traced back to raw mineral
extraction. Downstream coverage continues through package and module assembly,
PCB, PMIC and SPD components, DIMM form factors, and system qualification.

Every scope item must ultimately be `covered`, `unknown`, `not_applicable`, or
`out_of_scope`. An unknown item records what was researched, why the evidence is
insufficient, and how the gap affects the conclusion. The graph models products,
components, processes, equipment, materials, services, and qualification steps;
company names never become graph nodes.

## Evidence policy

Every research stage stores retained sources and atomic claims in an evidence
ledger. Source registration records the retrieval URL, canonical publication
URL, publisher, publication date, access date, broad source class, reporting
basis, provenance, named entities, snapshot path, and snapshot hash. An
unfamiliar classification is accepted and conservatively normalized to
`unknown`; the researcher does not have to retry merely to guess a marginal
category. Claims cite registered source IDs and preserve exact quotations and
locators. Quantitative claims must also retain unit, period, and derivation.

Retrieval identity and publication identity are deliberately separate:

- `url` is the page that was actually fetched and snapshotted. It may be a
  mirror, syndication page, or alternate delivery URL.
- `canonical_url` is the original publication URL and may equal `url`.
- a source record represents one retrieval URL plus one snapshot hash, so the
  ledger can retain exactly what was read;
- `origin_id` is derived from the canonical URL;
- `independence_group` is finalized from the transitive closure of equal
  canonical origins and equal normalized content fingerprints. For example,
  A/X, B/X, and B/Y are one connected origin group even though A and B have
  different canonical URLs and X and Y have different content. Registration
  order cannot turn mirrors or syndicated copies into independent
  corroboration.

The researcher does not submit a confidence score. The host derives two
separate axes. `evidence_status` describes how strongly retained sources support
the assertion:

| `evidence_status` | Meaning |
| --- | --- |
| `confirmed` | A direct authoritative-primary edge is marked `authority_for_claim`, one direct qualified-media origin is document-backed, named-source, directly observed, or supported by a published methodology, or two independent original qualified-media origins directly report the claim from anonymous sources |
| `reported` | Direct published support exists, but none of the confirmation rules above is satisfied |
| `inferred` | The claim is an explicit analytical inference or has only usable indirect support |
| `unknown` | No qualifying support remains; lead-only material does not raise the status |

`dispute_status` independently describes retained contradiction:

| `dispute_status` | Meaning |
| --- | --- |
| `clean` | No contradicting evidence is retained |
| `challenged` | A weaker or indirect contradiction is retained |
| `disputed` | A direct contradiction comes from an authoritative source for that assertion, strong named or document-backed qualified media, or two independent original qualified-media origins reporting anonymously |

An official document is not automatically authoritative for every statement:
`authority_for_claim` is set only when that source has direct authority over the
exact assertion. Conversely, qualified media can confirm a claim under the
rules above. Anonymous reporting needs two independent qualified-media origins;
two mirrors of one report still count as one. Any claim with an explicit
inference remains `inferred` regardless of source rank. Self-media, forums, and
aggregators are `lead_only`: they may guide discovery but cannot directly
support a substantive final claim.

Supplier relationship maturity is controlled separately from both evidence
axes:

```text
research -> validation -> order_received -> batch_delivery
         -> mass_production -> primary_supplier
```

`unknown` is always available. A customer relationship does not imply that
every product is in mass production, and an order does not imply batch delivery.

The ledger finalizer checks the fixed date cutoff, source references, snapshot
paths, controlled values, assigned scope coverage, and quote fidelity. Quote
matching accepts exact text plus differences in line endings and whitespace
introduced by Markdown conversion; it does not rewrite words or accept a
semantic paraphrase as an exact quotation.

Before graph construction and again before report writing, a reconciliation
stage merges the accepted ledgers and identifies claims with the same subject,
predicate, type, and qualifiers but different values. Every detected conflict
must receive exactly one explicit decision: `prefer` keeps a non-empty strict
subset, `preserve_both` keeps every claim, and `unresolved` makes every claim in
that conflict unavailable as a conclusion. The final artifact records each
claim as `available`, `excluded`, or `unresolved`. Those states survive later
reconciliation stages; an unavailable claim cannot silently become available
again. No conflicting value is silently deleted.

Duplicate source IDs are deduplicated only when their decision-relevant
publication identity agrees. Block-local retrieval details such as
`snapshot_path`, access time, named entities, or a provisional independence
group may differ; canonical URL, origin, content and snapshot hashes, title,
publisher, publication date, source class, reporting basis, and provenance may
not conflict.

## Small typed-tool transactions

The tools are intentionally transactional. A model repairs only the rejected
record or batch rather than reconstructing one large nested result and losing a
previously correct field.

| Operation | Submission size |
| --- | --- |
| Register a retained source | Exactly one source |
| Stage evidence claims | 1-5 atomic claims |
| Stage current-source checks | 1-5 key claims |
| Stage structured evidence gaps | 1-5 gaps |
| Resolve evidence conflicts | Exactly one conflict |
| Stage report claims | 1-5 material report claims |
| Submit candidate assessment | Exactly one company |

Reusing a source-derived ID, claim ID, coverage-item ID, or graph
`section + batch_id` replaces only that record or batch. Validators collect all
discoverable issues in the submitted transaction before rejecting it, so the
model can repair that transaction once instead of discovering one missing field
per retry.

The supply-chain graph is likewise staged by section: metadata, nodes, edges,
coverage resolutions, and chokepoints. Cross-section validation happens only in
`finalize_supply_chain`, whose input is just the final artifact path. A rejected
edge batch does not require retransmitting nodes, coverage, or chokepoints.
The metadata also binds the finalized reconciliation artifact. The finalizer
rejects graph objects or formal chokepoints that cite claims excluded by a
`prefer` decision, left unavailable by an unresolved conflict, or inherited as
unavailable from an earlier reconciliation.

Finalizers are deliberately small. They receive paths plus the minimum context
needed to validate already staged records; they never ask the model to resubmit
the whole ledger, graph, reconciliation, or report manifest.

## Chokepoints and companies

The graph chair must first retain the complete declared chain, including
ordinary nodes and explicit unknowns. It may select a chokepoint only when
`confirmed` and undisputed claims support a structural mechanism such as
delivery impact, lack of qualified substitutes, switching or requalification
time, supplier concentration, convergence, capacity, or yield constraints.

Each selected node records separate controlled dimensions:

- `delivery_impact`: `limited`, `material`, or `production_stop`;
- `substitutability`: `qualified_alternatives`, `lengthy_requalification`,
  `no_known_substitute`, or `unknown`;
- `supplier_concentration`: `diversified`, `concentrated`, `single_source`, or
  `unknown`;
- independently supported min/max switching and recovery times in days.

There is no aggregate score or rank. A long on-site supply contract, for
example, may improve continuity rather than prove a supply risk; the report must
distinguish a supplier moat from a buyer outage mechanism.

Candidate discovery runs one task per accepted chokepoint and retains at most
`max_candidates_per_chokepoint` public companies with evidence for the exact
node relationship. Candidate assessment then runs one task per surviving
company and records controlled maturity, alternatives, switching constraints,
falsification conditions, and what could weaken the view.

For the small set of claims that determines a final company's legal identity,
listing status, exact-node relationship, control mechanism, or maturity, the
researcher also stages a current-source check. The set includes any contract,
market-share, capacity, or date claim used in the conclusion.
`verified_primary` names newly checked registered primary sources already
attached to that claim as direct, authoritative supporting evidence;
`checked_no_primary` records the official channels checked when no current
primary disclosure exists; `not_verified` records the remaining gap.
Ambiguous outcome text is normalized to `not_verified` rather than causing a
schema-repair loop.

The assessment is `verified` only when every key claim remains `confirmed` and
has a completed current-source check (`verified_primary` or
`checked_no_primary`). A missing, incomplete, disputed, or insufficient check
does not fail the task. The typed tool keeps the company in the artifact with
`verification_status = "pending"`, `effective_relationship = "unverified"`, and
`effective_relationship_maturity = "unknown"`, together with every verification
gap. Pending companies remain visible as follow-up leads but cannot be promoted
into a formal relationship conclusion.

The final reconciliation merges the compact key-claim reviews from candidate
assessment with the ledgers' freshness checks. If a reviewed key claim is later
excluded or left unresolved, its effective evidence status is mechanically
downgraded to `unknown`. Otherwise an incomplete current-source check remains a
`reported`/`pending` constraint in the final evidence artifact and report
manifest; synthesis cannot recover the raw `confirmed` status.

## Report contract and QC

Synthesis is offline. It can use only accepted artifacts and the final
reconciled evidence; it cannot search for a missing fact. The report must first
explain the complete declared product structure and process flow, then map what
is known about the named target, and only then discuss structural chokepoints
and company relationships.

Every material factual or analytical conclusion has a stable report claim ID.
The model stages each exact atomic clause, declares it as `fact` or `inference`,
and records its upstream evidence claim IDs. The matching footnote marker must
be immediately adjacent to that clause, allowing only whitespace. A readable
sentence may contain more than one atomic claim, for example:

```markdown
Supplier A received an order[^REPORT-01], but the tested product remained in validation.[^REPORT-02]
```

The report-manifest finalizer accepts exactly one finalized
`evidence-resolution.json`. It mechanically verifies paths, finalization state,
claim availability, IDs, marker uniqueness and adjacency, text-equivalent
statements, upstream claim existence, and source URLs. It applies effective
freshness downgrades, derives each report claim's evidence and dispute axes,
then follows both supporting and contradicting evidence edges. Source records
are deduplicated by canonical URL and relation, so mirrors do not produce
repeated links while a URL retained in different relations remains explicit.

The model does not hand-write the footnote definitions. The finalizer
idempotently writes an authoritative `Claim sources` section into `report.md`.
Each `[^REPORT-ID]` definition includes `evidence_status`, `dispute_status`, the
confirmation basis, freshness state, and every deduplicated canonical
supporting and contradicting URL. The generated manifest also retains the
contributing source record IDs, retrieval URL, canonical URL, origin ID,
relation, title, publisher, and publication date. A reader can therefore follow
every substantive clause directly from the report to its source URLs.

The final QC session treats all of those mechanical checks as authoritative.
For semantic review it calls the read-only `read_report_claim_evidence` Go tool
with at most ten report claim IDs per request. The tool projects only the chosen
atomic statements and their upstream subject/predicate/value/qualifiers,
inference, evidence edges, exact quotes, source class, reporting basis,
provenance, canonical origin, independence group, reconciliation availability,
and the full validated freshness check: date, official channels, latest primary
source IDs, outcome, and gap. It never returns unrelated claims and never
modifies an artifact. QC judges whether a clause is truly atomic, whether its evidence
entails it, whether source classification and independence are honest, whether
freshness gaps are represented, and whether uncertainty is calibrated. It does
not reimplement ID, path, URL, marker, or exact-text validation with grep,
PowerShell, Bash, shell, or temporary scripts.

The graph-selection QC follows the same boundary. `finalize_supply_chain`
authoritatively checks IDs, controlled values, coverage, connectivity, evidence
levels, and reconciliation decisions. QC receives a read-only
`read_chokepoint_evidence` tool that projects only the claims, sources, and
conflicts cited by selected chokepoints; it decides whether that evidence
actually supports the proposed structural mechanism and impact. PowerShell,
Bash, and generic shell tools are disabled for this QC, so it cannot replace the
typed finalizer with ad hoc scripts or modify the candidate under review.

This split keeps mechanical checks deterministic and keeps the QC model from
spending a large context window repeatedly grepping snapshots. When QC reports
semantic problems, the researcher revises the report and all QC criteria run
again, up to `max_qc_rounds`.

## Source tools and quotas

The local `pplx_tools` module exports two inline Go typed tools:

- `pplx_pro_search` discovers current sources with the Perplexity Search API.
- `pplx_fetch` fetches one selected URL into a caller-provided absolute
  `snapshot_dir`, derives `snapshot-<url-hash>.md`, and returns the final path.

The evidence-ledger, freshness, reconciliation, report-manifest, and semantic-QC
projection tools are also native inline `go_tool` blocks. They validate and
persist UTF-8 JSON directly; the example has no Python runtime dependency for
typed tools.

`use_pplx` defaults to `false`. Researchers then use the built-in `web_search`
and `web_fetch`. When `use_pplx = true`, r42 provides the two Perplexity tools,
disables the built-in web tools for those sessions, and applies
`pplx_tool_call_quota` to successful fetch calls independently per session.
Failed calls do not permanently consume quota.

Each prompt derives its snapshot directory from `block_wd()`. Dynamic tasks add
their stable list index so concurrent tasks do not write into the same folder.
All prompts prohibit PowerShell, shell commands, curl, wget, or scripts as a way
to bypass source-tool policy or quotas.

## Run the example

Install r42 and ensure GitHub Copilot CLI is installed:

```powershell
go install github.com/lonegunmanb/r42/cmd/r42@latest
```

The checked-in variable file uses BYOK. Set the provider secret it references:

```powershell
$env:DEEPSEEK_KEY = Read-Host -MaskInput "DeepSeek API key"
```

When `use_pplx = true`, also set:

```powershell
$env:PPLX_API_KEY = Read-Host -MaskInput "Perplexity API key"
```

Initialize the configuration snapshot, inspect the plan, and apply it:

A complete chokepoint study can run for several hours. Set `--timeout` to at
least `6h`; use a longer timeout when the topic or research scope is broad.

```powershell
r42 init ./docs/examples/chokepoint
r42 plan `
  -var-file ./docs/examples/chokepoint/research.r42vars `
  --out ./chokepoint.r42plan
r42 apply --timeout 6h --parallelism 5 ./chokepoint.r42plan
```

Or plan and apply the initialized snapshot in one command:

```powershell
r42 apply `
  --timeout 6h `
  --parallelism 5 `
  -var-file ./docs/examples/chokepoint/research.r42vars
```

`research.r42vars` is analogous to `terraform.tfvars`. It controls `topic`,
the fixed `as_of_date`, market, candidate limit, QC budget, Perplexity quota,
model roles, provider endpoint, and secret environment-variable reference.
`high_impact_model` can reserve a stronger model for scope, reconciliation,
selection, and synthesis; `qc_model` can use an independent model for QC.

## Output contract

The root outputs expose:

- `scope_path`: the reviewed boundary and coverage contract;
- `supply_chain_path`: the evidence-backed graph and structural chokepoints;
- `evidence_resolution_path`: the final merged claims, freshness reviews,
  conflict decisions, and per-claim availability;
- `report_path`: the final Markdown continuity-risk report;
- `report_manifest_path`: atomic report claims, two-axis and freshness statuses,
  filtered upstream semantic context, and deduplicated canonical source records.

Per-stage evidence ledgers, source registries, candidate artifacts, assessments,
snapshots, and draft transaction directories remain under the run directory
printed by r42. Static blocks have dedicated workspaces. Dynamic tasks share
their parent block workspace and use deterministic index subdirectories.
