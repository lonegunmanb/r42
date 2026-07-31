package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Azure/golden"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	modulespec "github.com/lonegunmanb/r42/internal/module/spec"
	"github.com/lonegunmanb/r42/internal/plan"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

type countingApplyFactory struct {
	calls int
}

func (f *countingApplyFactory) New(
	context.Context,
	plan.NodeSpec,
	*r42concurrency.Scope,
) (golden.ApplyBlock, error) {
	f.calls++
	return &applyFixtureBlock{BaseBlock: golden.NewBaseBlock(nil, nil)}, nil
}

type applyFixtureBlock struct {
	*golden.BaseBlock
}

func (*applyFixtureBlock) Type() string            { return "" }
func (*applyFixtureBlock) BlockType() string       { return "fixture" }
func (*applyFixtureBlock) AddressLength() int      { return 2 }
func (*applyFixtureBlock) CanExecutePrePlan() bool { return false }
func (*applyFixtureBlock) Apply() error            { return nil }

var (
	registerLifecycleFixture sync.Once
	lifecyclePlanCalls       atomic.Int64
	lifecycleApplyCalls      atomic.Int64
)

type lifecycleFixtureBlock struct {
	*golden.BaseBlock
}

func (*lifecycleFixtureBlock) Type() string            { return "" }
func (*lifecycleFixtureBlock) BlockType() string       { return "lifecycle_fixture" }
func (*lifecycleFixtureBlock) AddressLength() int      { return 2 }
func (*lifecycleFixtureBlock) CanExecutePrePlan() bool { return false }
func (*lifecycleFixtureBlock) ExecuteDuringPlan() error {
	lifecyclePlanCalls.Add(1)
	return nil
}

func (*lifecycleFixtureBlock) Apply() error {
	lifecycleApplyCalls.Add(1)
	return nil
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigPlansSourceDirectory(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42"), []byte(`
research "source" {
  model         = "test-model"
  system_prompt = "Collect evidence."

  artifact "report" {
    type = "file"
    path = "${cwd()}/report.md"
  }
}

output "summary" {
  value = one(research.source.artifact).path
}
`), 0o600))

	config, err := NewResearchConfig(directory, ResearchConfigOptions{Context: t.Context()})
	require.NoError(t, err)
	planned, err := RunResearchPlan(config)
	require.NoError(t, err)
	require.NotNil(t, planned.SavedPlan())
	assert.Same(t, planned, config.Plan())

	nodes := planned.SavedPlan().Nodes()
	require.Len(t, nodes, 1)
	assert.Equal(t, "research.source", nodes[0].Address)
	assert.Contains(t, planned.SavedPlan().Outputs()["summary"].Value.AsString(), "report.md")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchPlanAppliesThroughSourceConfig(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42"), []byte(`
output "summary" {
  value = "planned"
}
`), 0o600))

	var applied *plan.Plan
	warning := errors.New("cleanup warning")
	config, err := NewResearchConfig(directory, ResearchConfigOptions{
		Context: t.Context(),
		Apply: func(saved *plan.Plan) (map[string]cty.Value, []error, error) {
			applied = saved
			return map[string]cty.Value{"summary": cty.StringVal("applied")}, []error{warning}, nil
		},
	})
	require.NoError(t, err)
	planned, err := RunResearchPlan(config)
	require.NoError(t, err)

	require.NoError(t, planned.Apply())
	assert.Same(t, planned.SavedPlan(), applied)
	assert.Equal(t, cty.StringVal("applied"), config.Outputs()["summary"])
	assert.Equal(t, []error{warning}, config.Warnings())
}

func TestResearchConfigRestoresSavedPlan(t *testing.T) {
	t.Parallel()

	saved, err := plan.NewWithContextAndLocals(
		t.TempDir(),
		nil,
		map[string]plan.OutputSpec{"summary": {Value: cty.StringVal("planned")}},
		nil,
		nil,
	)
	require.NoError(t, err)
	var applied *plan.Plan
	config, err := NewResearchConfigFromPlan(saved, ResearchConfigOptions{
		Context:     t.Context(),
		Parallelism: 3,
		Apply: func(received *plan.Plan) (map[string]cty.Value, []error, error) {
			applied = received
			return map[string]cty.Value{"summary": cty.StringVal("applied")}, nil, nil
		},
	})
	require.NoError(t, err)

	planned := config.Plan()
	require.NotNil(t, planned)
	assert.Equal(t, 3, config.Parallelism())
	require.NoError(t, planned.Apply())
	assert.Same(t, saved, applied)
	assert.Equal(t, cty.StringVal("applied"), config.Outputs()["summary"])
}

func TestResearchConfigBuildsGoldenGraphFromNativeR42Blocks(t *testing.T) {
	t.Parallel()

	scope, err := r42concurrency.NewScope(2)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	execution := newPlanExecution(ctx, cancel, nil, scope, []plan.NodeSpec{
		{Address: "research.source", Kind: "research", Config: cty.EmptyObjectVal},
		{
			Address: "module.synthesis", Kind: "module", Config: cty.EmptyObjectVal,
			Dependencies: []string{"research.source"},
		},
	})

	config, err := newApplyResearchConfig(".", execution, 2)
	require.NoError(t, err)
	blocks := golden.Blocks[golden.Block](config)
	require.Len(t, blocks, 2)
	byAddress := make(map[string]golden.Block, len(blocks))
	for _, block := range blocks {
		byAddress[block.Address()] = block
	}

	assert.IsType(t, new(researchspec.ResearchBlock), byAddress["research.source"])
	assert.IsType(t, new(modulespec.ModuleBlock), byAddress["module.synthesis"])
	assert.Implements(t, (*golden.ApplyBlock)(nil), byAddress["research.source"])
	assert.Implements(t, (*golden.ApplyBlock)(nil), byAddress["module.synthesis"])
	parents, err := config.GetAncestors("module.synthesis")
	require.NoError(t, err)
	assert.Contains(t, parents, "research.source")
}

func TestResearchConfigRunPlanSeparatesGoldenPlanFromApply(t *testing.T) {
	t.Parallel()

	scope, err := r42concurrency.NewScope(1)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	factory := new(countingApplyFactory)
	execution := newPlanExecution(ctx, cancel, factory, scope, []plan.NodeSpec{{
		Address: "research.source", Kind: "research", Config: cty.EmptyObjectVal,
	}})
	config, err := newApplyResearchConfig(".", execution, 1)
	require.NoError(t, err)

	require.NoError(t, config.RunPlan())
	assert.Zero(t, factory.calls)

	planned := &ResearchPlan{config: config}
	require.NoError(t, planned.Apply())
	assert.Equal(t, 1, factory.calls)
}

//nolint:paralleltest // Golden's block registry and fixture counters are process-global.
func TestResearchPlanApplyCallsApplyBlockWithoutRunningPlanAgain(t *testing.T) {
	registerLifecycleFixture.Do(func() { golden.RegisterBlock(new(lifecycleFixtureBlock)) })
	lifecyclePlanCalls.Store(0)
	lifecycleApplyCalls.Store(0)

	writeFile := hclwrite.NewEmptyFile()
	writeFile.Body().AppendBlock(hclwrite.NewBlock("lifecycle_fixture", []string{"only"}))
	syntaxFile, diagnostics := hclsyntax.ParseConfig(writeFile.Bytes(), "fixture.r42", hcl.InitialPos)
	require.False(t, diagnostics.HasErrors())
	syntaxBody, ok := syntaxFile.Body.(*hclsyntax.Body)
	require.True(t, ok)

	config := &ResearchConfig{
		BaseConfig:  golden.NewBasicConfigFromArgs(golden.NewBaseConfigArgs{Basedir: ".", Ctx: t.Context()}),
		parallelism: 1,
	}
	require.NoError(t, golden.InitConfig(config, golden.AsHclBlocks(syntaxBody.Blocks, writeFile.Body().Blocks())))
	planned, err := RunResearchPlan(config)
	require.NoError(t, err)

	require.NoError(t, planned.Apply())
	assert.Equal(t, int64(1), lifecyclePlanCalls.Load())
	assert.Equal(t, int64(1), lifecycleApplyCalls.Load())
}

func TestRunResearchPlanRequiresConfig(t *testing.T) {
	t.Parallel()

	_, err := RunResearchPlan(nil)
	require.EqualError(t, err, "research config is required")
}

func TestNativePlanHCLBlocksRejectInvalidNodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		nodes []plan.NodeSpec
		want  string
	}{
		{
			name: "address kind mismatch",
			nodes: []plan.NodeSpec{{
				Address: "research.source", Kind: "module", Config: cty.EmptyObjectVal,
			}},
			want: `saved plan node "research.source" does not match kind "module"`,
		},
		{
			name: "unsupported kind",
			nodes: []plan.NodeSpec{{
				Address: "fixture.source", Kind: "fixture", Config: cty.EmptyObjectVal,
			}},
			want: `saved plan node "fixture.source" has unsupported kind "fixture"`,
		},
		{
			name: "malformed dependency",
			nodes: []plan.NodeSpec{{
				Address: "research.source", Kind: "research", Config: cty.EmptyObjectVal,
				Dependencies: []string{"missing"},
			}},
			want: "saved plan node research.source depends on unknown node missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := nativePlanHCLBlocks(tt.nodes)
			require.EqualError(t, err, tt.want)
		})
	}
}
