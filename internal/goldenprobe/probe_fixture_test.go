package goldenprobe

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Azure/golden"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func init() {
	golden.RegisterBlock(new(probeBlock))
	golden.RegisterBlock(new(staticModuleBlock))
	golden.RegisterBlock(new(customModuleBlock))
}

type probeConfig struct {
	*golden.BaseConfig
}

type probeBlock struct {
	*golden.BaseBlock
	Value   string `hcl:"value"`
	Result  string `attribute:"result"`
	Applied bool
}

func (*probeBlock) Type() string { return "value" }

func (*probeBlock) BlockType() string { return "probe" }

func (*probeBlock) AddressLength() int { return 3 }

func (*probeBlock) CanExecutePrePlan() bool { return false }

func (b *probeBlock) ExecuteDuringPlan() error {
	b.Result = strings.ToUpper(b.Value)

	return nil
}

func (b *probeBlock) Apply() error {
	b.Applied = true

	return nil
}

type staticModuleBlock struct {
	*golden.BaseBlock
	Source string `hcl:"source"`
}

func (*staticModuleBlock) Type() string { return "" }

func (*staticModuleBlock) BlockType() string { return "static_module" }

func (*staticModuleBlock) AddressLength() int { return 2 }

func (*staticModuleBlock) CanExecutePrePlan() bool { return false }

func (*staticModuleBlock) ExecuteDuringPlan() error { return nil }

type customModuleBlock struct {
	*golden.BaseBlock
	Source string
	Inputs map[string]cty.Value
}

func (*customModuleBlock) Type() string { return "" }

func (*customModuleBlock) BlockType() string { return "custom_module" }

func (*customModuleBlock) AddressLength() int { return 2 }

func (*customModuleBlock) CanExecutePrePlan() bool { return false }

func (*customModuleBlock) ExecuteDuringPlan() error { return nil }

func (b *customModuleBlock) Decode(block *golden.HclBlock, context *hcl.EvalContext) error {
	b.Inputs = make(map[string]cty.Value)
	for name, attribute := range block.Attributes() {
		if name == "depends_on" || name == "for_each" {
			continue
		}
		value, diagnostics := attribute.Expr.Value(context)
		if diagnostics.HasErrors() {
			return diagnostics
		}
		if name == "source" {
			b.Source = value.AsString()
			continue
		}
		b.Inputs[name] = value
	}

	return nil
}

type dependencyProbeResult struct {
	values            map[string]string
	implicitAncestors map[string]any
	explicitAncestors map[string]any
}

func runDependencyProbe() (dependencyProbeResult, error) {
	implicitConfig, err := newProbeConfig(`
probe "value" "first" {
  value = "hello"
}

probe "value" "second" {
  value = probe.value.first.result
}
`, "", nil)
	if err != nil {
		return dependencyProbeResult{}, err
	}
	if err = implicitConfig.RunPlan(); err != nil {
		return dependencyProbeResult{}, err
	}

	values := make(map[string]string)
	for _, block := range golden.Blocks[*probeBlock](implicitConfig) {
		values[block.Address()] = block.Result
	}
	implicitAncestors, err := implicitConfig.GetAncestors("probe.value.second")
	if err != nil {
		return dependencyProbeResult{}, err
	}

	explicitConfig, err := newProbeConfig(`
probe "value" "first" {
  value = "hello"
}

probe "value" "dependent" {
  value      = "world"
  depends_on = [probe.value.first]
}
`, "", nil)
	if err != nil {
		return dependencyProbeResult{}, err
	}
	explicitAncestors, err := explicitConfig.GetAncestors("probe.value.dependent")
	if err != nil {
		return dependencyProbeResult{}, err
	}

	return dependencyProbeResult{
		values:            values,
		implicitAncestors: implicitAncestors,
		explicitAncestors: explicitAncestors,
	}, nil
}

type planApplyProbeResult struct {
	planInterfaceMethods []string
	appliedAfterPlan     bool
	appliedAfterTraverse bool
}

func runPlanApplyProbe() (planApplyProbeResult, error) {
	config, err := newProbeConfig(`
probe "value" "only" {
  value = "hello"
}
`, "", nil)
	if err != nil {
		return planApplyProbeResult{}, err
	}
	if err = config.RunPlan(); err != nil {
		return planApplyProbeResult{}, err
	}

	block := golden.Blocks[*probeBlock](config)[0]
	result := planApplyProbeResult{
		planInterfaceMethods: interfaceMethods(reflect.TypeFor[golden.Plan]()),
		appliedAfterPlan:     block.Applied,
	}

	err = golden.Traverse[golden.ApplyBlock](config.BaseConfig, func(block golden.ApplyBlock) error {
		return block.Apply()
	})
	result.appliedAfterTraverse = block.Applied

	return result, err
}

type variableSources struct {
	environment     string
	defaultFile     string
	defaultJSON     string
	autoFile        string
	autoFiles       []namedVariableFile
	additionalFiles []namedVariableFile
	cli             string
	cliSources      []cliVariableSource
}

type namedVariableFile struct {
	name  string
	value string
}

type cliVariableSource struct {
	fileName string
	value    string
}

func readVariable(t *testing.T, sources variableSources) (string, error) {
	t.Helper()

	directory := t.TempDir()
	t.Setenv("R42_VAR_subject", sources.environment)
	writeVariableFile(t, directory, "r42.r42vars", sources.defaultFile)
	writeVariableFile(t, directory, "r42.r42vars.json", sources.defaultJSON)
	writeVariableFile(t, directory, "z.auto.r42vars", sources.autoFile)
	for _, file := range sources.autoFiles {
		writeVariableFile(t, directory, file.name, file.value)
	}
	for _, file := range sources.additionalFiles {
		writeVariableFile(t, directory, file.name, file.value)
	}

	var assigned []golden.CliFlagAssignedVariables
	for _, source := range sources.cliSources {
		if source.fileName != "" {
			assigned = append(assigned, golden.NewCliFlagAssignedVariableFile(filepath.Join(directory, source.fileName)))
			continue
		}
		assigned = append(assigned, golden.NewCliFlagAssignedVariable("subject", source.value))
	}
	if sources.cli != "" {
		assigned = append(assigned, golden.NewCliFlagAssignedVariable("subject", sources.cli))
	}
	config, err := newProbeConfig(`
variable "subject" {
  type    = string
  default = "block"
}
`, directory, assigned)
	if err != nil {
		return "", err
	}

	return golden.Blocks[*golden.VariableBlock](config)[0].Value().AsString(), nil
}

func writeVariableFile(t *testing.T, directory, name, value string) {
	t.Helper()

	if value == "" {
		return
	}
	content := "subject = \"" + value + "\"\n"
	if filepath.Ext(name) == ".json" {
		content = "{\"subject\":\"" + value + "\"}\n"
	}
	err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600)
	require.NoError(t, err)
}

type dynamicInputProbeResult struct {
	staticDecodeError error
	customSource      string
	customInputs      map[string]cty.Value
	explicitAncestors map[string]any
	expandedSources   []string
}

func runDynamicInputProbe() (dynamicInputProbeResult, error) {
	staticConfig, err := newProbeConfig(`
static_module "child" {
  source = "./child"
  topic  = "energy"
}
`, "", nil)
	if err != nil {
		return dynamicInputProbeResult{}, err
	}
	staticDecodeError := staticConfig.RunPlan()

	customConfig, err := newProbeConfig(`
custom_module "upstream" {
  source = "./upstream"
}

custom_module "child" {
  source     = "./child"
  topic      = "energy"
  year       = 2030
  depends_on = [custom_module.upstream]
}
`, "", nil)
	if err != nil {
		return dynamicInputProbeResult{}, err
	}
	if err = customConfig.RunPlan(); err != nil {
		return dynamicInputProbeResult{}, err
	}
	customBlocks := golden.Blocks[*customModuleBlock](customConfig)
	var child *customModuleBlock
	for _, block := range customBlocks {
		if block.Name() == "child" {
			child = block
			break
		}
	}
	if child == nil {
		return dynamicInputProbeResult{}, hcl.Diagnostics{&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "custom child module was not planned",
		}}
	}
	explicitAncestors, err := customConfig.GetAncestors("custom_module.child")
	if err != nil {
		return dynamicInputProbeResult{}, err
	}

	expandedConfig, err := newProbeConfig(`
custom_module "expanded" {
  for_each = toset(["a", "b"])
  source   = "./${each.value}"
}
`, "", nil)
	if err != nil {
		return dynamicInputProbeResult{}, err
	}
	if err = expandedConfig.RunPlan(); err != nil {
		return dynamicInputProbeResult{}, err
	}
	expandedSources := make([]string, 0, 2)
	for _, block := range golden.Blocks[*customModuleBlock](expandedConfig) {
		expandedSources = append(expandedSources, block.Source)
	}

	return dynamicInputProbeResult{
		staticDecodeError: staticDecodeError,
		customSource:      child.Source,
		customInputs:      child.Inputs,
		explicitAncestors: explicitAncestors,
		expandedSources:   expandedSources,
	}, nil
}

type opaquePayload struct{}

type mappedShape struct {
	Payload opaquePayload `hcl:"payload"`
}

func mappedProbeType() cty.Type {
	golden.AddCustomTypeMapping[opaquePayload](cty.String)

	return golden.StructToCtyType(reflect.TypeFor[mappedShape]())
}

func newProbeConfig(source, baseDirectory string, assigned []golden.CliFlagAssignedVariables) (*probeConfig, error) {
	blocks, err := parseBlocks(source)
	if err != nil {
		return nil, err
	}
	config := &probeConfig{
		BaseConfig: golden.NewBasicConfig(baseDirectory, "r42", "r42", nil, assigned, nil),
	}
	if err = golden.InitConfig(config, blocks); err != nil {
		return nil, err
	}

	return config, nil
}

func parseBlocks(source string) ([]*golden.HclBlock, error) {
	syntaxFile, diagnostics := hclsyntax.ParseConfig([]byte(source), "probe.r42", hcl.InitialPos)
	if diagnostics.HasErrors() {
		return nil, diagnostics
	}
	writeFile, diagnostics := hclwrite.ParseConfig([]byte(source), "probe.r42", hcl.InitialPos)
	if diagnostics.HasErrors() {
		return nil, diagnostics
	}

	syntaxBody, ok := syntaxFile.Body.(*hclsyntax.Body)
	if !ok {
		return nil, hcl.Diagnostics{&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "unexpected HCL body type",
		}}
	}

	return golden.AsHclBlocks(syntaxBody.Blocks, writeFile.Body().Blocks()), nil
}

func interfaceMethods(interfaceType reflect.Type) []string {
	methods := make([]string, 0, interfaceType.NumMethod())
	for index := range interfaceType.NumMethod() {
		methods = append(methods, interfaceType.Method(index).Name)
	}

	return methods
}
