package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/lonegunmanb/golden"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	modulespec "github.com/lonegunmanb/r42/internal/module/spec"
	"github.com/lonegunmanb/r42/internal/plan"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	runpkg "github.com/lonegunmanb/r42/internal/run"
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
	lifecycleApplyHook       func() error
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
	if lifecycleApplyHook != nil {
		return lifecycleApplyHook()
	}
	return nil
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigPlansSourceDirectory(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "source" {
  model         = "test-model"
  system_prompt = "Collect evidence."

  artifact "report" {
    type = "file"
    path = "${cwd()}/report.md"
	description = "Plan report fixture"
  }
}

output "summary" {
  value = one(research.static.source.artifact).path
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
	assert.Equal(t, "research.static.source", nodes[0].Address)
	assert.Contains(t, planned.SavedPlan().Outputs()["summary"].Value.AsString(), "report.md")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigPreservesHeredocLocalExpression(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
locals {
  system_prompt = <<-PROMPT
    Research carefully.
    Stop when the evidence is sufficient.
  PROMPT

  tool_call_quota = {
    web_fetch = 20
  }
}
`), 0o600))

	config, err := NewResearchConfig(directory, ResearchConfigOptions{Context: t.Context()})
	require.NoError(t, err)
	planned, err := RunResearchPlan(config)
	require.NoError(t, err)
	source, ok := planned.SavedPlan().LocalExpressions()["system_prompt"]
	require.True(t, ok)

	_, diagnostics := hclsyntax.ParseExpression([]byte(source), "saved-plan-local", hcl.InitialPos)
	require.False(t, diagnostics.HasErrors(), "%s; source=%q", diagnostics.Error(), source)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigDelegatesVariableTypeDefaultsToGolden(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
variable "provider" {
  type = object({
    type     = optional(string, "openai")
    endpoint = optional(string, "https://example.test/v1")
    retry = optional(object({
      attempts = optional(number, 3)
    }), {})
  })
  default = {}
}

research "static" "source" {
  model         = var.provider.type
  system_prompt = "${var.provider.endpoint}|${var.provider.retry.attempts}"
}
`), 0o600))

	config, err := NewResearchConfig(directory, ResearchConfigOptions{Context: t.Context()})
	require.NoError(t, err)
	planned, err := RunResearchPlan(config)
	require.NoError(t, err)

	nodes := planned.SavedPlan().Nodes()
	require.Len(t, nodes, 1)
	decoded, err := modulespec.DecodeResearchPlan(nodes[0].Config)
	require.NoError(t, err)
	assert.Equal(t, "openai", decoded.Config.Model)
	assert.Equal(t, "https://example.test/v1|3", decoded.Config.SystemPrompt)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigExposesSourceDirectoryAsPathModule(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
output "module_path" {
  value = path.module
}
`), 0o600))

	config, err := NewResearchConfig(directory, ResearchConfigOptions{Context: t.Context()})
	require.NoError(t, err)
	planned, err := RunResearchPlan(config)
	require.NoError(t, err)

	assert.Equal(t, filepath.ToSlash(directory), planned.Outputs["module_path"].Value.AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestApplyResearchConfigExposesSavedDirectoryAsPathModule(t *testing.T) {
	directory := t.TempDir()
	execution := &planExecution{ctx: t.Context()}

	config, err := newApplyResearchConfig(directory, execution, 1)
	require.NoError(t, err)
	pathValue, ok := config.EvalContext().Variables["path"]
	require.True(t, ok)

	assert.Equal(t, filepath.ToSlash(directory), pathValue.GetAttr("module").AsString())
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigPlansBlockWorkingDirectoriesWithoutCreatingRun(t *testing.T) {
	directory := t.TempDir()
	runRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "first" {
  model         = "test-model"
  system_prompt = block_wd()
}
research "static" "second" {
  model         = "test-model"
  system_prompt = "${block_wd()}/notes"
}
`), 0o600))

	config, err := NewResearchConfig(directory, ResearchConfigOptions{
		Context: t.Context(), RunDirectory: runRoot,
	})
	require.NoError(t, err)
	planned, err := RunResearchPlan(config)
	require.NoError(t, err)
	saved := planned.SavedPlan()
	require.NotNil(t, saved)
	reserved, err := runpkg.Open(saved.RunDirectory())
	require.NoError(t, err)
	assert.NoDirExists(t, filepath.Join(runRoot, ".r42"))

	configs := make(map[string]researchspec.Config)
	for _, node := range saved.Nodes() {
		decoded, decodeErr := modulespec.DecodeResearchPlan(node.Config)
		require.NoError(t, decodeErr)
		configs[node.Address] = decoded.Config
	}
	first, err := reserved.WorkspacePath("research.static.first")
	require.NoError(t, err)
	second, err := reserved.WorkspacePath("research.static.second")
	require.NoError(t, err)
	assert.Equal(t, first, configs["research.static.first"].SystemPrompt)
	assert.Equal(t, second+"/notes", configs["research.static.second"].SystemPrompt)
	assert.NotEqual(t, first, second)
	assert.True(t, filepath.IsAbs(filepath.FromSlash(first)))
	assert.NotContains(t, first, `\`)
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchPlanAppliesThroughSourceConfig(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
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
		{Address: "research.static.source", Kind: "research", Config: cty.EmptyObjectVal},
		{
			Address: "module.synthesis", Kind: "module", Config: cty.EmptyObjectVal,
			Dependencies: []string{"research.static.source"},
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

	assert.IsType(t, new(researchspec.ResearchBlock), byAddress["research.static.source"])
	assert.IsType(t, new(modulespec.ModuleBlock), byAddress["module.synthesis"])
	assert.Implements(t, (*golden.ApplyBlock)(nil), byAddress["research.static.source"])
	assert.Implements(t, (*golden.ApplyBlock)(nil), byAddress["module.synthesis"])
	parents, err := config.GetAncestors("module.synthesis")
	require.NoError(t, err)
	assert.Contains(t, parents, "research.static.source")
}

//nolint:paralleltest // Golden's block registry is process-global.
func TestResearchConfigRebuildsStaticForEachInstanceDependencies(t *testing.T) {
	execution := &planExecution{ctx: t.Context(), nodes: []plan.NodeSpec{
		{
			Address: "research.static.deep_dive[001]",
			Kind:    "research",
			Config:  cty.EmptyObjectVal,
		},
		{
			Address: "research.static.deep_dive[002]",
			Kind:    "research",
			Config:  cty.EmptyObjectVal,
		},
		{
			Address: "research.static.deep_dive[003]",
			Kind:    "research",
			Config:  cty.EmptyObjectVal,
		},
		{
			Address: "research.static.summary",
			Kind:    "research",
			Config:  cty.EmptyObjectVal,
			Dependencies: []string{
				"research.static.deep_dive[001]",
				"research.static.deep_dive[002]",
				"research.static.deep_dive[003]",
			},
		},
	}}

	config, err := newApplyResearchConfig(".", execution, 2)
	require.NoError(t, err)
	require.NoError(t, config.RunPlan())
	blocks := golden.Blocks[*researchspec.ResearchBlock](config)
	addresses := make([]string, 0, len(blocks))
	for _, block := range blocks {
		addresses = append(addresses, block.Address())
	}
	assert.ElementsMatch(t, []string{
		"research.static.deep_dive[001]",
		"research.static.deep_dive[002]",
		"research.static.deep_dive[003]",
		"research.static.summary",
	}, addresses)
	parents, err := config.GetAncestors("research.static.summary")
	require.NoError(t, err)
	assert.Contains(t, parents, "research.static.deep_dive[001]")
	assert.Contains(t, parents, "research.static.deep_dive[002]")
	assert.Contains(t, parents, "research.static.deep_dive[003]")
}

func TestResearchConfigRunPlanSeparatesGoldenPlanFromApply(t *testing.T) {
	t.Parallel()

	scope, err := r42concurrency.NewScope(1)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	factory := new(countingApplyFactory)
	execution := newPlanExecution(ctx, cancel, factory, scope, []plan.NodeSpec{{
		Address: "research.static.source", Kind: "research", Config: cty.EmptyObjectVal,
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

//nolint:paralleltest // Golden's block registry and lifecycle hook are process-global.
func TestResearchPlanApplyDelegatesParallelSchedulingToGolden(t *testing.T) {
	registerLifecycleFixture.Do(func() { golden.RegisterBlock(new(lifecycleFixtureBlock)) })
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	lifecycleApplyHook = func() error {
		started <- struct{}{}
		<-release
		return nil
	}
	t.Cleanup(func() { lifecycleApplyHook = nil })

	writeFile := hclwrite.NewEmptyFile()
	writeFile.Body().AppendBlock(hclwrite.NewBlock("lifecycle_fixture", []string{"first"}))
	writeFile.Body().AppendBlock(hclwrite.NewBlock("lifecycle_fixture", []string{"second"}))
	syntaxFile, diagnostics := hclsyntax.ParseConfig(writeFile.Bytes(), "fixture.r42", hcl.InitialPos)
	require.False(t, diagnostics.HasErrors())
	syntaxBody, ok := syntaxFile.Body.(*hclsyntax.Body)
	require.True(t, ok)

	config := &ResearchConfig{
		BaseConfig:  golden.NewBasicConfigFromArgs(golden.NewBaseConfigArgs{Basedir: ".", Ctx: t.Context()}),
		parallelism: 2,
	}
	require.NoError(t, golden.InitConfig(config, golden.AsHclBlocks(syntaxBody.Blocks, writeFile.Body().Blocks())))
	planned, err := RunResearchPlan(config)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- planned.Apply() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		require.FailNow(t, "first ApplyBlock did not start")
	}

	secondStarted := false
	select {
	case <-started:
		secondStarted = true
	case <-time.After(time.Second):
	}
	close(release)
	require.NoError(t, <-done)
	assert.True(t, secondStarted, "second ApplyBlock did not start before the first was released")
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
				Address: "research.static.source", Kind: "module", Config: cty.EmptyObjectVal,
			}},
			want: `saved plan node "research.static.source" does not match kind "module"`,
		},
		{
			name: "research subtype is missing",
			nodes: []plan.NodeSpec{{
				Address: "research.source", Kind: "research", Config: cty.EmptyObjectVal,
			}},
			want: `saved plan node "research.source" does not match kind "research"`,
		},
		{
			name: "for each key is empty",
			nodes: []plan.NodeSpec{{
				Address: "research.static.source[]", Kind: "research", Config: cty.EmptyObjectVal,
			}},
			want: `saved plan node "research.static.source[]" does not match kind "research"`,
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
				Address: "research.static.source", Kind: "research", Config: cty.EmptyObjectVal,
				Dependencies: []string{"missing"},
			}},
			want: "saved plan node research.static.source depends on unknown node missing",
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
