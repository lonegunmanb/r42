# Company-first supply-chain research

This example studies a product supply chain and produces a falsifiable list of
public companies worth researching next. It is deliberately not an investment
screener: A/B/C are research priorities, not stock ratings, and the report does
not rank expected returns or recommend securities.

The workflow optimizes for three questions:

1. Does the source directly support the fact?
2. Do the facts support the proposed continuity-risk node?
3. Do the node and company evidence support spending more research time on that
   company?

## Workflow

    define scope
        |
        v
    atomic claim cards across five parallel tracks
        |
        v
    reference supply-chain map
        |                         |
        v                         v
    node assessments      company research priorities
        |                         |
        +------------+------------+
                     |
                     v
    company-first report with direct source URLs

primary_source_baseline first captures current official filings, regulator
records, official product documents, and official statements. brainstorm defines
the product boundary and assigns every relevant component and stage to one of
five parallel tracks:

- product structure and BOM;
- manufacturing, packaging, and testing;
- specialized equipment and tooling;
- materials, chemicals, and consumables;
- qualification and integration lock-in.

The supply-chain map keeps ordinary nodes and explicit gaps. It does not score
or pre-declare chokepoints. It identifies two independent target lists:
continuity-risk `assessment_targets`, and supplier-addressable
`company_mapping_targets` for components, materials, processes, equipment, and
services. Companies remain outside the supply-chain graph.

Each node assessment answers:

1. Does the risk concern current production, expansion and upgrade, or one
   product branch?
2. Does the named target actually depend on the node?
3. Which alternatives are already qualified and have usable capacity?
4. Does switching or recovery time exceed known inventory or other buffers?
5. What future evidence would disprove the conclusion?

risk_scope and conclusion are independent:

- scope is global or branch;
- conclusion is confirmed, candidate, or not_proven.

not_proven means public evidence is insufficient. It does not mean that no risk
exists.

The company stage combines discovery and verification in one research task per
company-mapping target, independently of node risk assessment. It records an
existing supplier, qualified alternative, exact-node capability match,
related-product company, or unverified lead. An empty company list is valid.

## Atomic claim cards

The report uses one evidence layer. There are no secondary RPT claims, report
manifest, reconciliation sessions, or composite chokepoint scores.

    {
      "id": "C-012",
      "statement": "The company outsources all chip packaging.",
      "status": "confirmed",
      "scope": "DDR5 die packaging",
      "as_of": "2025-12-31",
      "source_url": "https://example.com/filing",
      "exact_quote": "The company outsources all chip packaging.",
      "locator": "page 227, production model",
      "derived_from": []
    }

The three statuses have narrow meanings:

| Status | Meaning |
| --- | --- |
| confirmed | An authoritative primary source directly states the fact |
| reported | A retained published source directly reports the fact |
| inferred | The conclusion is derived from existing claim IDs |

An unknown is not a claim. It belongs in an assessment's unknowns or
unresolved_questions.

Supplier material can confirm its own product's existence or specification. It
cannot by itself confirm customer adoption, qualification, market share, or
independent performance.

The typed tools keep source governance in source-registry.json: canonical and
retrieval URLs, publisher, publication and access dates, source class, original
origin, artifact path, and content hashes. Report authors do not repeatedly fill
or reconcile those fields.

Mechanical validation happens before QC:

- paths must stay inside the current run's blocks directory;
- sources and claim IDs must exist;
- URLs must be complete and usable;
- dates must not exceed the fixed evidence cutoff;
- inferred claims must reference existing, acyclic premises;
- quotations must occur in the saved artifact;
- Unicode spaces, line breaks, and tabs may differ, but words, punctuation, and
  order may not.

The tool returns all detected problems in one response. QC only judges semantic
questions: whether a quotation entails the claim, whether an inference
overreaches, whether different products or periods were merged, and whether the
conclusion is appropriately cautious.

## Company priorities

The report begins with companies rather than ending with a long supply-chain
inventory.

| Priority | Meaning |
| --- | --- |
| A | An exact-node relationship or capability is confirmed and a high-value unresolved question has an executable next check |
| B | The exact-node link is plausible, but the relationship, qualification, or benefit mechanism is incomplete |
| C | Only industry relevance or a related product is established |
| do_not_research | The node or company link is too weak for the next research round |

Every public-company record includes a non-empty ticker and market so the same
security can be merged safely across mapping targets. `research_priority` is separate from `role`. Relationship claim IDs prove a
customer, supplier, or qualification relationship; capability claim IDs prove
the ability to supply the exact node without implying a named customer. Every
listed company includes the exact node, strongest evidence, largest remaining
unknown, and next verification action. It also records four separate
economic-exposure dimensions:

| Dimension | Allowed status |
| --- | --- |
| customer validation | `unknown`, `evaluation`, `qualified`, `ordered`, `delivering`, `production_use` |
| revenue materiality | `unknown`, `exposure_unquantified`, `quantified_immaterial`, `quantified_material` |
| bottleneck capture | `unknown`, `none`, `plausible`, `demonstrated` |
| commercialization timing | `unknown`, `current`, `within_12_months`, `beyond_12_months` |

Each dimension has its own `evidence_directness` and `claim_ids`.
`evidence_directness` is `none`, `confirmed`, `reported`, or `inferred`, matching
the referenced atomic claim status. An unknown dimension uses `none` and an
empty claim list. Timing is measured from the evidence cutoff and refers to
expected supplier revenue timing, not only the target product's launch date.

Optional `exposure_signals` preserve the scope of evidence that does not fit a
single categorical dimension. A signal records `scope`, `subject`, `metric`,
`value`, `as_of`, `evidence_directness`, and `claim_ids`; scope is `company`,
`segment`, `modality`, `target_branch`, or `named_program`. A segment or modality
metric is never silently promoted to named-program economics.

A company with only a related product cannot receive A. A requires confirmed
relationship evidence for an existing supplier or qualified alternative, or
confirmed exact-node capability evidence for a capability match. It does not
require a named target customer. These gates are enforced by the typed tool.

## Report contract

The model places a marker such as [[claim:C-012]] immediately after each
substantive atomic clause. The final typed tool replaces it with a direct
original-source link and appends the referenced claim card, quotation, locator,
and URL. There is no second report-claim layer. Invalid IDs, missing URLs, and
truncated URLs containing three dots reject finalization without rewriting the
report.

The report order is:

1. companies worth researching next;
2. confirmed and candidate global or branch-specific nodes;
3. current-production, expansion/upgrade, and product-branch views;
4. node assessments and falsification conditions;
5. not-proven nodes and unresolved questions;
6. the decision-relevant supply-chain map and scope limitations.

## Source tools

By default, Collection uses the built-in web_search and web_fetch tools. Set
use_pplx to true to use the pplx_pro_search and pplx_fetch Go tools from the
local module. In that mode the built-in web tools are disabled for Collection.
pplx_tool_call_quota limits successful pplx_fetch calls per Collection session;
set it to null (the default) for no call limit and let Collection QC decide
when the acquired evidence is sufficient.

Every block uses the default `collection_batch_size = 10` and the default
`max_collection_rounds = 10`. Search and fetch tools appear only in
`collection_tool_ids`; closed Research receives registered artifacts and
validated upstream typed-tool JSON.

All prompts prohibit PowerShell, shell commands, curl, wget, and scripts as a
way to bypass source-tool policy or quotas. Every retained source is saved as a
Markdown artifact inside the block workspace.

## Run

Install r42 and ensure GitHub Copilot CLI is installed:

    go install github.com/lonegunmanb/r42/cmd/r42@latest

Create a local `research.r42vars` in this directory before planning. It is
analogous to `terraform.tfvars`, is ignored by Git, and must never be
committed. It must provide the required topic, evidence cutoff, and Research and
QC provider configurations; the remaining variables have defaults:

    topic      = "The technology or industrial system to study"
    as_of_date = "2026-08-25"
    model_provider = {
      api_key_ref = "OPENROUTER_API_KEY"
    }
    qc_model_provider = {
      api_key_ref = "OPENROUTER_QC_API_KEY"
    }

Set the referenced environment variables separately. For example:

    $env:OPENROUTER_API_KEY = Read-Host -MaskInput "OpenRouter API key"
    $env:OPENROUTER_QC_API_KEY = Read-Host -MaskInput "OpenRouter QC API key"

When use_pplx is true, also set PPLX_API_KEY.

Initialize, plan, and apply:

    r42 init ./docs/examples/chokepoint
    r42 plan -var-file ./docs/examples/chokepoint/research.r42vars --out ./chokepoint.r42plan
    r42 apply --timeout 6h --parallelism 5 ./chokepoint.r42plan

A complete study can take several hours. Use a timeout of at least six hours;
increase it for a broad product boundary.

The root outputs are report_path, scope_path, supply_chain_path,
node_assessment_paths, and company_priority_paths. Artifacts, claim cards,
source registries, node assessments, and company priority artifacts remain
under the run directory printed by r42.
