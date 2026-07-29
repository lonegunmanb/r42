# Golden Capability Spike

This spike targets Golden
`v0.0.0-20260603014844-1d1c394b55ea`. Executable evidence lives in
`internal/goldenprobe` and deliberately remains test-only.

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
`PlanBlock.ExecuteDuringPlan`, and mutates the live registered block instances.
The executable probe verifies that Golden's exported `Plan` interface contains
only `String() string` and `Apply() error` and that `RunPlan` does not call a
block's `Apply` method.

A separate exported-surface and pinned-source audit found no serializable Plan
model or nested executor API. The audit covered `plan.go`, `base_config.go`,
`config.go`, and the package's exported symbols at the pinned pseudo-version.
This is a source-audit conclusion, not a negative behavior inferred from guessed
method names.

Golden also has no top-level Apply executor. The executable path is caller-owned:
traverse the planned DAG with `golden.Traverse[golden.ApplyBlock]` and invoke
each block's `Apply` method. The probe verifies that `RunPlan` does not apply a
block and an explicit traversal does.

Consequently r42 must own:

- immutable Plan data and serialization;
- reconstruction of the Apply graph from saved Plan data;
- Apply scheduling, cancellation, and cleanup;
- creation of a nested executor over an already saved child Plan.

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

Golden has no exported production source-directory loader. r42 still needs to
load and parse all `.r42` files, pair `hclsyntax` and `hclwrite` blocks, and pass
the result through `golden.AsHclBlocks` and `golden.InitConfig`.

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
