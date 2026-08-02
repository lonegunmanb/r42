# Golden Capability Spike

`go.mod` declares Azure/golden
`v0.0.0-20260603014844-1d1c394b55ea` and replaces it with the cumulative r42
fork `github.com/lonegunmanb/golden`
`v0.0.0-20260802095203-0924ab02ace4`. Executable capability evidence lives in
`internal/goldenprobe`; r42 integration behavior is covered in
`internal/executor`, `internal/research/spec`, and `internal/module/spec` tests.

## Block And Type Registration

Custom blocks are registered process-wide with `golden.RegisterBlock`. A block
embeds `*golden.BaseBlock`, supplies its block type, subtype, address length, and
lifecycle methods, and declares its fixed HCL schema with struct tags. Golden
constructs registered blocks with reflection, injects `BaseBlock`, applies
defaults, and uses registered address lengths to recognize references.

`golden.StructToCtyType` derives object types from tagged Go structs.
`golden.AddCustomTypeMapping[T]` adds process-wide mappings for Go types that do
not have a built-in cty representation. The probe maps an otherwise opaque Go
type to `cty.String` and verifies the derived containing object type.

Both registries are global mutable state. r42 should register its fixed block
and type set once during process initialization, before concurrent planning.

## Dependencies

Golden walks every HCL expression during `InitConfig`. Traversals beginning with
a registered reference keyword create DAG edges automatically. The probe proves
that `probe.value.first.result` makes `probe.value.first` an ancestor of
`probe.value.second`, and that Plan evaluates the upstream result before decoding
the downstream block.

`depends_on` is a meta attribute containing a list of block address traversals.
Golden validates each address and the same expression walker creates explicit
edges. No r42-specific dependency parser is needed after r42 has handed Golden
the expression and registered the referenced block type.

## Plan And Apply

`Config.RunPlan` decodes and validates blocks, invokes
`PlanBlock.ExecuteDuringPlan`, and populates the live registered block
instances. It does not call `ApplyBlock.Apply`. r42 research and module blocks
use `ExecuteDuringPlan` only to validate and snapshot their planned
configuration; no Copilot session, typed tool process, artifact mutation, or
other Apply work starts in this phase.

A separate exported-surface and pinned-source audit found no serializable Plan
model or nested executor API. The audit covered `plan.go`, `base_config.go`,
`config.go`, and the package's exported symbols at the pinned pseudo-version.
This is a source-audit conclusion, not a negative behavior inferred from guessed
method names.

For a source directory, r42 retains the `ResearchConfig` used by `RunPlan` and
serializes its own immutable Plan snapshot. Applying a saved Plan reconstructs
its nodes as native r42 `research` and `module` blocks, runs Golden Plan once to
decode and validate that reconstructed graph, and then calls
`ResearchPlan.Apply`. Apply collects the reconstructed `ApplyBlock` instances
from Golden, then r42's saved-plan scheduler invokes each ready block's
`Apply()` method. The block delegates its canonical address to the r42 execution
factory, which performs the actual research or nested-module work.

Consequently r42 must own:

- immutable Plan data and serialization;
- reconstruction of the Apply graph from saved Plan data;
- the saved-node factory, cancellation, and cleanup;
- creation of a nested executor over an already saved child Plan.

The fork's `BaseConfig` accepts a parallelism setting and runs independent ready
vertices through `runDagOnParallel` during Plan. Its generic
`Traverse[ApplyBlock]` remains serial. r42 therefore schedules saved-plan Apply
from its immutable dependency lists, calls each native block's `Apply()` method,
and uses global plus ancestor-module permit scopes to count only active research
blocks. Module orchestration nodes do not consume research permits.

The audited Golden version has no API for constructing a nested executor from a
child Plan. Module planning and execution can still reuse Golden configurations
internally, but r42 must provide the Plan and executor boundary.

## Variable Loading

Golden variable blocks use their HCL `default` only when no external value is
available. External values are merged in this order, with later sources winning:

1. `<DSL>_VAR_<name>` environment variables;
2. `<dsl-full-name>.<dsl-abbreviation>vars` and its JSON form;
3. lexically sorted `*.auto.<dsl-abbreviation>vars` and JSON files;
4. CLI assignments and explicitly named variable files, in declaration order.

The probe verifies every boundary above, including HCL versus JSON default
files, lexical ordering across HCL and JSON auto files, and declaration ordering
when direct CLI values and `golden.NewCliFlagAssignedVariableFile` are mixed.
Root CLI plumbing should pass Golden's `CliFlagAssignedVariables` directly.

The fork also supports Terraform-style `optional(T, default)` modifiers inside
object type constraints. Type comparison uses cty's semantic equality rather
than Go `==`, because object types contain maps and are not Go-comparable.

Golden has no exported production source-directory loader. r42 still needs to
load and parse all `*.r42.hcl` files, pair `hclsyntax` and `hclwrite` blocks, and
pass the result through `golden.AsHclBlocks` and `golden.InitConfig`.

## Dynamic Module Inputs

A regular `gohcl` decode uses the registered Go struct as a closed schema.
Terraform-style module arguments have author-defined names, so an input such as
`topic = "energy"` is rejected as an unsupported argument when only fixed fields
like `source` are tagged.

The module block therefore must implement `golden.CustomDecode`. Its decoder
reads fixed attributes, skips Golden-handled `depends_on` and `for_each` meta
attributes, and evaluates every remaining attribute into a typed
`map[string]cty.Value`. The probe demonstrates the closed-schema failure,
string and number inputs, source decoding, an explicit dependency, and expanded
`for_each` instances.

## Native Go Fields

Golden decodes HCL primitive attributes directly into Go `string`, `bool`,
`int`, and `[]string` fields. An optional primitive can use a pointer such as
`*int`: an omitted attribute remains `nil`, while an explicit zero remains a
non-nil pointer to zero. r42 blocks should therefore use native Go fields for
primitive attributes instead of routing them through `cty.Value`.

Golden reapplies `default` tags after decoding, and the defaults library treats
Go zero values as unset. A default tag can therefore overwrite an explicitly
configured `false`, `0`, or empty string. Fields where an explicit zero differs
from omission must use a pointer and apply their default during r42 validation.
`cty.Value` remains appropriate for block traversals, dynamically typed DSL
values, and values that must retain cty marks.

For a two-segment root block reference such as `go_tool.finish`, the block must
implement `golden.SingleValueBlock`. Its `Value()` may return a `cty.Object`;
Golden then decodes the traversal directly into a consumer's `cty.Value` field
and records the implicit dependency. A small object containing the block address,
kind, and public block attributes preserves typed reference semantics. Returning
an address string instead would erase the referenced block's object shape.

## Fork Compatibility Fixes

r42 relies on cumulative fixes in the pinned fork rather than compensating in
its own DSL layer. In particular, the fork:

- copies the underlying `hclsyntax.Block` while expanding `for_each`, so
  parallel instances do not mutate one shared nested-block body;
- compares cty types with semantic equality, avoiding a panic for object-typed
  variables;
- populates expanded `SingleValues` only after the replacement vertices are in
  the DAG, preserving keyed `for_each` namespaces for downstream traversals;
- accepts the second default argument in `optional(T, default)` object
  attributes; and
- traverses independent ready vertices in parallel when Config parallelism is
  set.

Two boundaries remain owned by r42. Golden still exposes aggregate
initialization/decode errors rather than per-block decode middleware, so r42 can
record the failed config initialization but cannot always identify one block
lifecycle for malformed HCL. Golden also cannot decode an unknown Plan-time cty
value into a native Go primitive; r42 keeps apply-time values in cty or saved
Plan structures until they become known.
