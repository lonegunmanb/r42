package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/cli"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

type fakeRuntime struct {
	planned          *plan.Plan
	plannedDirectory string
	initialized      string
	stateDirectory   string
	modulesDirectory string
	upgradeModules   bool
	variables        []golden.CliFlagAssignedVariables
	configOptions    executor.ResearchConfigOptions
	applyDeadline    bool
	applyDeadlineAt  time.Time
	outputs          map[string]cty.Value
	warnings         []error
	planErr          error
	applyErr         error
	initErr          error
	saveOutputsErr   error
	readOutputsErr   error
	applyHook        func()
	openErr          error
	savedOutputs     map[string]cty.Value
	savedRun         string
	saveOutputsCalls int
	storedOutputs    []byte
}

func (f *fakeRuntime) InitProject(
	_ context.Context,
	directory string,
	stateDirectory string,
	upgrade bool,
) error {
	f.initialized = directory
	f.stateDirectory = stateDirectory
	f.upgradeModules = upgrade
	return f.initErr
}

func (f *fakeRuntime) OpenProject(stateDirectory string) (string, string, error) {
	f.stateDirectory = stateDirectory
	if f.openErr != nil {
		return "", "", f.openErr
	}
	config := filepath.Join(stateDirectory, "config")
	modules := filepath.Join(stateDirectory, "modules")
	f.modulesDirectory = modules
	return config, modules, nil
}

func (f *fakeRuntime) SaveProjectOutputs(
	stateDirectory string,
	runDirectory string,
	outputs map[string]cty.Value,
) error {
	f.stateDirectory = stateDirectory
	f.savedRun = runDirectory
	f.savedOutputs = outputs
	f.saveOutputsCalls++
	return f.saveOutputsErr
}

func (f *fakeRuntime) ReadProjectOutputs(stateDirectory string) ([]byte, error) {
	f.stateDirectory = stateDirectory
	return f.storedOutputs, f.readOutputsErr
}

func (f *fakeRuntime) Config(
	directory string,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	f.plannedDirectory = directory
	f.variables = options.Variables
	if f.planErr != nil {
		return nil, f.planErr
	}
	return f.config(f.planned, options)
}

//nolint:paralleltest // t.Chdir verifies the CLI process working-directory contract.
func TestCommandInitUsesWorkingDirectoryStateDirectoryAndRefreshesModules(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Chdir(workingDirectory)
	target := t.TempDir()
	runtime := new(fakeRuntime)

	stdout, stderr, err := execute(t, runtime, "init", target)

	require.NoError(t, err)
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	assert.Equal(t, target, runtime.initialized)
	assert.Equal(t, filepath.Join(workingDirectory, ".r42"), runtime.stateDirectory)
	assert.True(t, runtime.upgradeModules)
}

//nolint:paralleltest // t.Chdir verifies the CLI process working-directory contract.
func TestCommandInitDefaultsToCurrentDirectoryAndRefreshesModules(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Chdir(workingDirectory)
	runtime := new(fakeRuntime)

	_, _, err := execute(t, runtime, "init")

	require.NoError(t, err)
	assert.Equal(t, ".", runtime.initialized)
	assert.Equal(t, filepath.Join(workingDirectory, ".r42"), runtime.stateDirectory)
	assert.True(t, runtime.upgradeModules)
}

func TestCommandInitHelpDoesNotExposeUpgradeSwitch(t *testing.T) {
	t.Parallel()

	stdout, _, err := execute(t, nil, "init", "--help")

	require.NoError(t, err)
	assert.Contains(t, stdout, "r42 init [SOURCE]")
	assert.Contains(t, stdout, "Initialize the active configuration and modules")
	assert.NotContains(t, stdout, "--upgrade")
}

//nolint:paralleltest // t.Chdir verifies the output state path contract.
func TestCommandOutputPrintsStoredOutputsAsPrettyJSON(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Chdir(workingDirectory)
	runtime := &fakeRuntime{storedOutputs: []byte(`{"nested":{"count":2},"answer":"42"}`)}

	stdout, stderr, err := execute(t, runtime, "output")

	require.NoError(t, err)
	assert.JSONEq(t, `{"answer":"42","nested":{"count":2}}`, stdout)
	assert.Contains(t, stdout, "\n  \"answer\": \"42\"")
	assert.Contains(t, stdout, "\n  \"nested\": {")
	assert.Empty(t, stderr)
	assert.Equal(t, filepath.Join(workingDirectory, ".r42"), runtime.stateDirectory)
}

func TestCommandOutputRejectsArguments(t *testing.T) {
	t.Parallel()

	_, _, err := execute(t, new(fakeRuntime), "output", "answer")

	require.Error(t, err)
	assert.Equal(t, cli.ExitUsage, cli.ExitCode(err))
}

func TestCommandOutputReportsMissingSavedOutputs(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{readOutputsErr: errors.New("no saved outputs for the current configuration; run r42 apply")}
	stdout, _, err := execute(t, runtime, "output")

	require.Error(t, err)
	assert.Empty(t, stdout)
	assert.ErrorContains(t, err, "run r42 apply")
}

//nolint:paralleltest // t.Chdir verifies the schema state path contract.
func TestCommandSchemaPrintsStableRootVariablesWithoutPlanning(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Chdir(workingDirectory)
	runtime := new(fakeRuntime)
	configDirectory := filepath.Join(workingDirectory, ".r42", "config")
	require.NoError(t, os.MkdirAll(configDirectory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(configDirectory, "variables.r42.hcl"), []byte(`
variable "topic" {
  type = string
}

variable "language" {
  type    = string
  default = "zh-CN"
}
`), 0o600))

	first, stderr, err := execute(t, runtime, "schema", "--json")
	require.NoError(t, err)
	second, _, err := execute(t, runtime, "schema", "--json")
	require.NoError(t, err)

	assert.Empty(t, stderr)
	assert.Equal(t, first, second)
	assert.JSONEq(t, `{
  "schema_version": 1,
  "variables": [
    {
      "name":"language","description":null,"type":"string","required":false,
      "nullable":true,"sensitive":false,"has_default":true,"default":"zh-CN",
      "default_redacted":false
    },
    {
      "name":"topic","description":null,"type":"string","required":true,
      "nullable":true,"sensitive":false,"has_default":false,"default":null,
      "default_redacted":false
    }
  ]
}`, first)
	assert.Empty(t, runtime.plannedDirectory)
	assert.Zero(t, runtime.saveOutputsCalls)
}

func TestCommandSchemaRequiresJSONFlagAndRejectsArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments []string
	}{
		{name: "missing json flag", arguments: []string{"schema"}},
		{name: "positional argument", arguments: []string{"schema", "template", "--json"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := execute(t, new(fakeRuntime), test.arguments...)

			require.Error(t, err)
			assert.Equal(t, cli.ExitUsage, cli.ExitCode(err))
		})
	}
}

//nolint:paralleltest // t.Chdir isolates the schema configuration fixture.
func TestCommandSchemaReportsOutputWriteFailure(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Chdir(workingDirectory)
	configDirectory := filepath.Join(workingDirectory, ".r42", "config")
	require.NoError(t, os.MkdirAll(configDirectory, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(configDirectory, "variables.r42.hcl"),
		[]byte(`variable "input" { type = string }`),
		0o600,
	))
	command := cli.NewCommand(new(fakeRuntime))
	command.SetOut(failingWriter{})
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"schema", "--json"})

	err := command.ExecuteContext(t.Context())

	require.Error(t, err)
	assert.ErrorContains(t, err, "write root variable schema")
}

//nolint:paralleltest // t.Chdir verifies initialized root-versus-module behavior.
func TestCommandSchemaExcludesChildModuleVariables(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Chdir(workingDirectory)
	templateDirectory := t.TempDir()
	childDirectory := filepath.Join(templateDirectory, "child")
	require.NoError(t, os.Mkdir(childDirectory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(templateDirectory, "main.r42.hcl"), []byte(`
variable "root_input" { type = string }
module "child" { source = "./child" }
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(childDirectory, "main.r42.hcl"), []byte(`
variable "child_input" { type = string }
`), 0o600))
	runtime := cli.NewRuntime()
	_, _, err := execute(t, runtime, "init", templateDirectory)
	require.NoError(t, err)

	stdout, stderr, err := execute(t, runtime, "schema", "--json")

	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Contains(t, stdout, `"name": "root_input"`)
	assert.NotContains(t, stdout, "child_input")
	assert.NoDirExists(t, filepath.Join(workingDirectory, ".r42", "runs"))
}

//nolint:paralleltest // t.Chdir verifies module installation relative to the CLI working directory.
func TestCommandInitMakesModulesAvailableToDirectoryApply(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Chdir(workingDirectory)
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(child, "main.r42.hcl"), []byte(`
output "answer" { value = "42" }
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.r42.hcl"), []byte(`
module "child" { source = "./child" }
output "answer" { value = module.child.answer }
output "root_module" { value = path.module }
`), 0o600))
	runtime := cli.NewRuntime()

	_, _, err := execute(t, runtime, "apply")
	require.Error(t, err)
	require.ErrorContains(t, err, "run r42 init")

	_, _, err = execute(t, runtime, "init", root)
	require.NoError(t, err)
	stdout, _, err := execute(t, runtime, "apply")

	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(workingDirectory, ".r42", "modules", "child", "main.r42.hcl"))
	assert.FileExists(t, filepath.Join(workingDirectory, ".r42", "config", "main.r42.hcl"))
	assert.Contains(t, stdout, `"42"`)
	assert.Contains(t, stdout, filepath.ToSlash(filepath.Join(workingDirectory, ".r42", "config")))
	assert.NoDirExists(t, filepath.Join(root, ".r42"))
	assert.FileExists(t, filepath.Join(workingDirectory, ".r42", "state.json"))
	stored, outputStderr, err := execute(t, runtime, "output")
	require.NoError(t, err)
	assert.Empty(t, outputStderr)
	assert.Contains(t, stored, `"answer": "42"`)
	assert.Contains(t, stored, `"root_module":`)
}

func (f *fakeRuntime) ConfigFromPlan(
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	return f.config(planned, options)
}

func (f *fakeRuntime) config(
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	f.configOptions = options
	options.Apply = func(*plan.Plan) (map[string]cty.Value, []error, error) {
		if f.applyHook != nil {
			f.applyHook()
		}
		f.applyDeadlineAt, f.applyDeadline = options.Context.Deadline()
		return f.outputs, f.warnings, f.applyErr
	}
	return executor.NewResearchConfigFromPlan(planned, options)
}

func TestCommandPlanSavesOptionalOutputAndSeparatesPermissionWarning(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	planned, err := plan.NewWithContextAndLocals(directory, nil, map[string]plan.OutputSpec{
		"answer": {Value: cty.StringVal("42")},
	}, nil, nil)
	require.NoError(t, err)
	runtime := &fakeRuntime{planned: planned}
	outPath := filepath.Join(t.TempDir(), "research.r42plan")

	stdout, stderr, executeErr := execute(t, runtime,
		"plan",
		"--out", outPath,
		"--var", "topic=markets",
		"--var-file", "inputs.r42vars",
	)

	require.NoError(t, executeErr)
	assert.Equal(t, filepath.Join(runtime.stateDirectory, "config"), runtime.plannedDirectory)
	assert.Len(t, runtime.variables, 2)
	assert.Contains(t, stdout, `"answer"`)
	assert.NotContains(t, stdout, "unencrypted")
	assert.Contains(t, stderr, "unencrypted")
	loaded, err := plan.Load(outPath)
	require.NoError(t, err)
	assert.Equal(t, planned.Outputs(), loaded.Outputs())
}

func TestCommandPlanUsesInitializedConfigurationWithoutSaving(t *testing.T) {
	t.Parallel()

	planned := mustPlan(t)
	runtime := &fakeRuntime{planned: planned}

	stdout, stderr, err := execute(t, runtime, "plan")

	require.NoError(t, err)
	assert.Equal(t, filepath.Join(runtime.stateDirectory, "config"), runtime.plannedDirectory)
	assert.Contains(t, stdout, `"nodes"`)
	assert.Empty(t, stderr)
}

func TestCommandPlanPrintsBeforeReportingSaveFailure(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{planned: mustPlan(t)}
	stdout, stderr, err := execute(t, runtime, "plan", "--out", t.TempDir())

	require.Error(t, err)
	require.ErrorContains(t, err, "save plan")
	assert.Contains(t, stdout, `"nodes"`)
	assert.Empty(t, stderr)
}

func TestCommandPlanTreatsWhitespaceOutputAsOmitted(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{planned: mustPlan(t)}
	stdout, stderr, err := execute(t, runtime, "plan", "--out", " \t ")

	require.NoError(t, err)
	assert.Contains(t, stdout, `"nodes"`)
	assert.Empty(t, stderr)
}

func TestCommandPlanHelpDoesNotOfferConfigurationDirectory(t *testing.T) {
	t.Parallel()

	stdout, _, err := execute(t, nil, "plan", "--help")

	require.NoError(t, err)
	assert.Contains(t, stdout, "r42 plan [flags]")
	assert.NotContains(t, stdout, "--directory")
	assert.NotContains(t, stdout, "plan DIRECTORY")
}

//nolint:paralleltest // t.Chdir verifies the CLI process working-directory contract.
func TestCommandPlanDebugRecordsDetailedGoldenPlanningLifecycle(t *testing.T) {
	workingDirectory := t.TempDir()
	directory := t.TempDir()
	t.Chdir(workingDirectory)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
output "answer" { value = "42" }
`), 0o600))

	planPath := filepath.Join(t.TempDir(), "saved.r42plan")
	runtime := cli.NewRuntime()
	_, _, err := execute(t, runtime, "init", directory)
	require.NoError(t, err)
	_, stderr, err := execute(t, runtime, "plan", "--out", planPath, "--debug")

	require.NoError(t, err)
	assert.Contains(t, stderr, "sensitive")
	events := readDebugEvents(t, workingDirectory)
	for _, action := range []string{
		"plan",
		"config.directory.scan",
		"config.file.collect",
		"hcl.syntax.parse",
		"hcl.write.parse",
		"hcl.block.extract",
		"golden.config.init",
		"golden.run_plan",
		"block.decode",
		"block.plan",
		"plan.snapshot",
		"plan.display",
		"plan.save",
	} {
		assert.Contains(t, events, `"action":"`+action+`"`)
	}
	encodedPath, err := json.Marshal(filepath.Join(workingDirectory, ".r42", "config", "main.r42.hcl"))
	require.NoError(t, err)
	assert.Contains(t, events, string(encodedPath))
	assert.NoDirExists(t, filepath.Join(directory, ".r42"))
}

//nolint:paralleltest // t.Chdir verifies the CLI process working-directory contract.
func TestCommandPlanStoresDebugRunInWorkingDirectory(t *testing.T) {
	workingDirectory := t.TempDir()
	configurationDirectory := t.TempDir()
	t.Chdir(workingDirectory)
	require.NoError(t, os.WriteFile(filepath.Join(configurationDirectory, "main.r42.hcl"), []byte(`
output "answer" { value = "42" }
`), 0o600))

	runtime := cli.NewRuntime()
	_, _, err := execute(t, runtime, "init", configurationDirectory)
	require.NoError(t, err)
	_, _, err = execute(t, runtime, "plan", "--debug")

	require.NoError(t, err)
	assert.Contains(t, readDebugEvents(t, workingDirectory), `"action":"plan"`)
	assert.NoDirExists(t, filepath.Join(configurationDirectory, ".r42"))
}

//nolint:paralleltest // t.Chdir isolates debug output from the repository.
func TestCommandPlanFailsFastBeforeInitialization(t *testing.T) {
	t.Chdir(t.TempDir())

	_, _, err := execute(t, cli.NewRuntime(), "plan", "--debug")

	require.Error(t, err)
	assert.ErrorContains(t, err, "run r42 init")
}

//nolint:paralleltest // t.Chdir isolates debug output from the repository.
func TestCommandPlanDebugRecordsParseFailure(t *testing.T) {
	workingDirectory := t.TempDir()
	directory := t.TempDir()
	t.Chdir(workingDirectory)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`output "answer" { value = "42" }`), 0o600))
	runtime := cli.NewRuntime()
	_, _, err := execute(t, runtime, "init", directory)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(workingDirectory, ".r42", "config", "broken.r42.hcl"),
		[]byte(`research "static" "broken" {`), 0o600,
	))

	_, _, err = execute(t, runtime, "plan", "--debug")

	require.Error(t, err)
	events := readDebugEvents(t, workingDirectory)
	assert.Contains(t, events, `"action":"hcl.syntax.parse","status":"failed"`)
	assert.Contains(t, events, `"action":"plan","status":"failed"`)
	assert.Contains(t, events, `"error":`)
}

//nolint:paralleltest // t.Chdir verifies the CLI process working-directory contract.
func TestCommandApplyDebugRecordsDetailedPlanAndApplyLifecycle(t *testing.T) {
	workingDirectory := t.TempDir()
	directory := t.TempDir()
	t.Chdir(workingDirectory)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "main.r42.hcl"), []byte(`
research "static" "source" {
  model         = "test-model"
  system_prompt = "Collect evidence."
}
output "answer" { value = "42" }
	`), 0o600))
	runtime := cli.NewRuntimeWithOptions(cli.RuntimeOptions{Sessions: &fakeSessionOpener{}})
	_, _, err := execute(t, runtime, "init", directory)
	require.NoError(t, err)

	_, _, err = execute(t, runtime, "apply", "--debug", "--parallelism", "1")

	require.NoError(t, err)
	events := readDebugEvents(t, workingDirectory)
	for _, action := range []string{
		"plan",
		"config.file.collect",
		"hcl.block.extract",
		"golden.config.init",
		"golden.run_plan",
		"plan.snapshot",
		"plan.display",
		"apply",
		"apply.golden.config.init",
		"apply.golden.plan",
		"apply.golden.apply",
		"block.decode",
		"block.plan",
		"block.factory",
		"block.apply",
		"block.cleanup",
		"apply.outputs.resolve",
		"session.open",
		"session.send",
		"session.close",
	} {
		assert.Contains(t, events, `"action":"`+action+`"`)
	}
	assert.Contains(t, events, `"block_address":"research.static.source"`)
	assert.NoDirExists(t, filepath.Join(directory, ".r42"))
}

//nolint:paralleltest // t.Chdir verifies the CLI process working-directory contract.
func TestCommandApplyStoresRunInWorkingDirectory(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		debug     bool
	}{
		{name: "default", arguments: nil},
		{name: "debug", arguments: []string{"--debug"}, debug: true},
	}
	//nolint:paralleltest // Each subtest changes the process working directory.
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workingDirectory := t.TempDir()
			configurationDirectory := t.TempDir()
			t.Chdir(workingDirectory)
			require.NoError(t, os.WriteFile(filepath.Join(configurationDirectory, "main.r42.hcl"), []byte(`
output "answer" { value = "42" }
`), 0o600))

			runtime := cli.NewRuntime()
			_, _, err := execute(t, runtime, "init", configurationDirectory)
			require.NoError(t, err)
			arguments := append([]string{"apply"}, test.arguments...)
			_, _, err = execute(t, runtime, arguments...)

			require.NoError(t, err)
			runs, err := os.ReadDir(filepath.Join(workingDirectory, ".r42", "runs"))
			require.NoError(t, err)
			require.Len(t, runs, 1)
			if test.debug {
				assert.Contains(t, readDebugEvents(t, workingDirectory), `"action":"apply"`)
			}
			assert.NoDirExists(t, filepath.Join(configurationDirectory, ".r42"))
		})
	}
}

func readDebugEvents(t *testing.T, directory string) string {
	t.Helper()
	runs, err := os.ReadDir(filepath.Join(directory, ".r42", "runs"))
	require.NoError(t, err)
	require.Len(t, runs, 1)
	content, err := os.ReadFile(filepath.Join(directory, ".r42", "runs", runs[0].Name(), "events.jsonl"))
	require.NoError(t, err)
	return string(content)
}

//nolint:paralleltest // t.Chdir isolates CLI run artifacts from the source tree.
func TestCommandApplySupportsSavedPlanAndInitializedConfiguration(t *testing.T) {
	t.Chdir(t.TempDir())

	directory := t.TempDir()
	planned, err := plan.NewWithContextAndLocals(directory, nil, nil, nil, nil)
	require.NoError(t, err)
	planPath := filepath.Join(t.TempDir(), "saved.r42plan")
	_, err = plan.Save(planPath, planned)
	require.NoError(t, err)

	tests := []struct {
		name          string
		arguments     []string
		wantPlanCalls bool
	}{
		{name: "saved plan", arguments: []string{planPath}},
		{name: "initialized configuration", wantPlanCalls: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &fakeRuntime{
				planned:  planned,
				outputs:  map[string]cty.Value{"answer": cty.StringVal("42")},
				warnings: []error{errors.New("close session failed")},
			}

			arguments := append([]string{"apply"}, test.arguments...)
			arguments = append(arguments,
				"--parallelism", "3",
				"--timeout", "2s",
				"--session-stall-timeout", "45s",
				"--debug",
			)
			stdout, stderr, executeErr := execute(t, runtime, arguments...)

			require.NoError(t, executeErr)
			assert.Equal(t, test.wantPlanCalls, runtime.plannedDirectory != "")
			assert.Equal(t, 3, runtime.configOptions.Parallelism)
			assert.Equal(t, 45*time.Second, runtime.configOptions.SessionStallTimeout)
			assert.True(t, runtime.configOptions.Debug)
			assert.True(t, runtime.applyDeadline)
			assert.Contains(t, stdout, `"nodes"`)
			assert.Contains(t, stdout, `"answer": "42"`)
			assert.Less(t, strings.Index(stdout, `"nodes"`), strings.Index(stdout, `"answer": "42"`))
			assert.NotContains(t, stdout, "close session failed")
			assert.Contains(t, stderr, "close session failed")
			assert.Contains(t, stderr, "sensitive")
		})
	}
}

func TestCommandApplyPrintsPlanBeforeExecution(t *testing.T) {
	t.Parallel()
	planned := mustPlan(t)
	wantPlan, err := plan.Display(planned)
	require.NoError(t, err)
	wantOutputs, err := plan.DisplayValues(map[string]cty.Value{"answer": cty.StringVal("42")})
	require.NoError(t, err)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runtime := &fakeRuntime{planned: planned, outputs: map[string]cty.Value{"answer": cty.StringVal("42")}}
	runtime.applyHook = func() {
		assert.Equal(t, wantPlan, stdout.String())
	}
	command := cli.NewCommand(runtime)
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"apply"})

	err = command.ExecuteContext(t.Context())

	require.NoError(t, err)
	assert.Equal(t, wantPlan+wantOutputs, stdout.String())
	assert.Contains(t, stderr.String(), "Research tasks: 0")
}

//nolint:paralleltest // t.Chdir verifies the Apply state path contract.
func TestCommandApplyPersistsOutputsBeforePrettyPrinting(t *testing.T) {
	workingDirectory := t.TempDir()
	t.Chdir(workingDirectory)
	runDirectory := filepath.Join(workingDirectory, ".r42", "runs", "run-test")
	planned, err := plan.NewForRun(t.TempDir(), runDirectory, nil, nil, nil, nil)
	require.NoError(t, err)
	runtime := &fakeRuntime{
		planned: planned,
		outputs: map[string]cty.Value{
			"answer": cty.StringVal("42"),
		},
	}

	stdout, _, err := execute(t, runtime, "apply")

	require.NoError(t, err)
	wantPlan, err := plan.Display(planned)
	require.NoError(t, err)
	assert.Equal(t, wantPlan+"{\n  \"answer\": \"42\"\n}\n", stdout)
	assert.Equal(t, filepath.Join(workingDirectory, ".r42"), runtime.stateDirectory)
	assert.Equal(t, runDirectory, runtime.savedRun)
	assert.Equal(t, cty.StringVal("42"), runtime.savedOutputs["answer"])
	assert.Equal(t, 1, runtime.saveOutputsCalls)
}

func TestCommandApplyDoesNotPersistOutputsAfterFailure(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{planned: mustPlan(t), applyErr: assert.AnError}

	_, _, err := execute(t, runtime, "apply")

	require.ErrorIs(t, err, assert.AnError)
	assert.Zero(t, runtime.saveOutputsCalls)
}

func TestCommandApplyReportsOutputPersistenceFailureBeforeDisplayingOutputs(t *testing.T) {
	t.Parallel()

	planned := mustPlan(t)
	wantPlan, err := plan.Display(planned)
	require.NoError(t, err)
	runtime := &fakeRuntime{
		planned:        planned,
		outputs:        map[string]cty.Value{"answer": cty.StringVal("42")},
		saveOutputsErr: errors.New("state is read-only"),
	}

	stdout, _, err := execute(t, runtime, "apply")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "save apply outputs")
	assert.Equal(t, wantPlan, stdout)
}

func TestCommandApplyREPLShowsDAGAndLiveProgressOnStderr(t *testing.T) {
	t.Parallel()

	runDirectory := filepath.Join(t.TempDir(), "run-42")
	planned, err := plan.NewForRun(t.TempDir(), runDirectory, []plan.NodeSpec{
		{Address: "research.static.collect", Kind: "research"},
		{
			Address: "research.static.summary", Kind: "research",
			Dependencies: []string{"research.static.collect"},
		},
	}, nil, nil, nil)
	require.NoError(t, err)
	runtime := &fakeRuntime{planned: planned}
	runtime.applyHook = func() {
		ctx := runtime.configOptions.Context
		require.NoError(t, debuglog.Lifecycle(ctx, "block.apply", debuglog.StatusStarted, debuglog.Event{
			BlockAddress: "research.static.collect", BlockType: "research",
		}))
		require.NoError(t, debuglog.Record(ctx, debuglog.Event{
			Kind: debuglog.EventMessage, Action: "assistant.reasoning_delta",
			BlockAddress: "research.static.collect", Session: debuglog.SessionResearch,
			Content: "checking evidence",
		}))
		require.NoError(t, debuglog.Lifecycle(ctx, "block.apply", debuglog.StatusCompleted, debuglog.Event{
			BlockAddress: "research.static.collect", BlockType: "research",
		}))
	}

	stdout, stderr, err := execute(t, runtime, "apply", "--ui=repl")

	require.NoError(t, err)
	assert.Contains(t, stdout, `"nodes"`)
	assert.NotContains(t, stdout, "START research")
	assert.Contains(t, stderr, "Run: "+runDirectory)
	assert.Contains(t, stderr, "Research tasks: 2")
	assert.Contains(t, stderr, "research.static.collect -> research.static.summary")
	assert.Contains(t, stderr, "START research.static.collect")
	assert.Contains(t, stderr, "THINKING checking evidence")
	assert.Contains(t, stderr, "DONE research.static.collect")
}

func TestCommandApplyHelpDescribesAutomaticUISelection(t *testing.T) {
	t.Parallel()

	stdout, _, err := execute(t, nil, "apply", "--help")

	require.NoError(t, err)
	assert.Contains(t, stdout, "r42 apply [PLAN]")
	assert.Contains(t, stdout, "--ui string")
	assert.Contains(t, stdout, "auto, tui, repl, or jsonl")
	assert.Contains(t, stdout, `(default "auto")`)
}

func TestCommandApplyPrintsPlanWhenExecutionFails(t *testing.T) {
	t.Parallel()
	planned := mustPlan(t)
	wantPlan, err := plan.Display(planned)
	require.NoError(t, err)
	runtime := &fakeRuntime{planned: planned, applyErr: assert.AnError}

	stdout, _, err := execute(t, runtime, "apply")

	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, wantPlan, stdout)
}

func TestCommandApplyFailsFastWhenPlanCannotBeWritten(t *testing.T) {
	t.Parallel()
	applyCalled := false
	runtime := &fakeRuntime{planned: mustPlan(t), applyHook: func() { applyCalled = true }}
	command := cli.NewCommand(runtime)
	command.SetOut(failingWriter{})
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"apply"})

	err := command.ExecuteContext(t.Context())

	require.ErrorContains(t, err, "write plan")
	assert.False(t, applyCalled)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, assert.AnError
}

func TestCommandDiagnosticsAndExitCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		runtime  *fakeRuntime
		wantCode int
		wantErr  string
	}{
		{
			name:     "usage",
			args:     []string{"apply", "one.r42plan", "two.r42plan"},
			runtime:  &fakeRuntime{},
			wantCode: cli.ExitUsage,
			wantErr:  "at most 1",
		},
		{
			name:     "plan block diagnostic",
			args:     []string{"plan"},
			runtime:  &fakeRuntime{planErr: errors.New("research.market: model is required")},
			wantCode: cli.ExitFailure,
			wantErr:  "research.market",
		},
		{
			name:     "plan directory flag is rejected",
			args:     []string{"plan", "--directory", t.TempDir()},
			runtime:  &fakeRuntime{planned: mustPlan(t)},
			wantCode: cli.ExitUsage,
			wantErr:  "unknown flag",
		},
		{
			name:     "apply failure",
			args:     []string{"apply"},
			runtime:  &fakeRuntime{planned: mustPlan(t), applyErr: errors.New("apply block research.market: failed")},
			wantCode: cli.ExitFailure,
			wantErr:  "research.market",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := execute(t, test.runtime, test.args...)
			require.Error(t, err)
			assert.Equal(t, test.wantCode, cli.ExitCode(err))
			assert.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestCommandApplyFailsFastWhenTargetCannotBeInspected(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "missing.r42plan")

	_, _, err := execute(t, &fakeRuntime{}, "apply", target)

	require.Error(t, err)
	require.ErrorContains(t, err, "inspect apply target")
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestCommandApplyRejectsConfigurationDirectoryArgument(t *testing.T) {
	t.Parallel()

	_, _, err := execute(t, &fakeRuntime{}, "apply", t.TempDir())

	require.Error(t, err)
	assert.Equal(t, cli.ExitUsage, cli.ExitCode(err))
	assert.ErrorContains(t, err, "saved plan")
}

func execute(t *testing.T, runtime cli.Runtime, args ...string) (string, string, error) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := cli.NewCommand(runtime)
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(args)
	err := command.ExecuteContext(t.Context())
	return stdout.String(), stderr.String(), err
}

func mustPlan(t *testing.T) *plan.Plan {
	t.Helper()
	planned, err := plan.NewWithContextAndLocals(t.TempDir(), nil, nil, nil, nil)
	require.NoError(t, err)
	return planned
}

func TestApplyTimeoutValidation(t *testing.T) {
	t.Parallel()

	_, _, err := execute(t, &fakeRuntime{planned: mustPlan(t)}, "apply", "--timeout", "0s")
	require.Error(t, err)
	assert.Equal(t, cli.ExitUsage, cli.ExitCode(err))
	assert.ErrorContains(t, err, "timeout must be positive")
}

func TestApplyDefaultParallelism(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{planned: mustPlan(t)}
	_, _, err := execute(t, runtime, "apply")
	require.NoError(t, err)
	assert.Equal(t, 10, runtime.configOptions.Parallelism)
}

func TestApplyDefaultTimeout(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{planned: mustPlan(t)}
	_, _, err := execute(t, runtime, "apply")
	require.NoError(t, err)
	assert.False(t, runtime.applyDeadline)
}

func TestApplyDefaultSessionStallTimeout(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{planned: mustPlan(t)}
	_, _, err := execute(t, runtime, "apply")
	require.NoError(t, err)
	assert.Equal(t, 15*time.Minute, runtime.configOptions.SessionStallTimeout)
}

func TestApplySessionStallTimeoutValidation(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"0s", "-1s"} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()

			_, _, err := execute(
				t,
				&fakeRuntime{planned: mustPlan(t)},
				"apply",
				"--session-stall-timeout",
				value,
			)
			require.Error(t, err)
			assert.Equal(t, cli.ExitUsage, cli.ExitCode(err))
			require.ErrorContains(t, err, "session stall timeout must be positive")
		})
	}
}

func TestApplyRedactsSensitiveOutputFromStdout(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{
		planned: mustPlan(t),
		outputs: map[string]cty.Value{
			"secret": corespec.MarkSensitive(cty.StringVal("do-not-print")),
		},
	}
	stdout, _, err := execute(t, runtime, "apply")
	require.NoError(t, err)
	assert.Contains(t, stdout, "<sensitive>")
	assert.NotContains(t, stdout, "do-not-print")
}

func TestApplyCancelledContextUsesFailureExit(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{planned: mustPlan(t), applyErr: context.Canceled}
	_, _, err := execute(t, runtime, "apply")
	require.Error(t, err)
	assert.Equal(t, cli.ExitFailure, cli.ExitCode(err))
}

func TestTimeoutContextActuallyExpires(t *testing.T) {
	t.Parallel()

	deadlineObserved := make(chan struct{})
	runtime := runtimeFunc{
		plan: mustPlan(t),
		apply: func(ctx context.Context) error {
			<-ctx.Done()
			close(deadlineObserved)
			return ctx.Err()
		},
	}
	_, _, err := execute(t, runtime, "apply", "--timeout", "1ms")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-deadlineObserved:
	case <-time.After(time.Second):
		t.Fatal("apply did not observe timeout")
	}
}

func TestApplyTimeoutIncludesInitializedConfigurationPlanning(t *testing.T) {
	t.Parallel()

	runtime := &blockingPlanRuntime{}
	_, _, err := execute(t, runtime, "apply", "--timeout", "1ms")

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, runtime.applyCalled)
}

type blockingPlanRuntime struct{ applyCalled bool }

func (*blockingPlanRuntime) OpenProject(stateDirectory string) (string, string, error) {
	return filepath.Join(stateDirectory, "config"), filepath.Join(stateDirectory, "modules"), nil
}

func (*blockingPlanRuntime) SaveProjectOutputs(string, string, map[string]cty.Value) error {
	return nil
}

func (*blockingPlanRuntime) ReadProjectOutputs(string) ([]byte, error) {
	return []byte(`{}`), nil
}

func (*blockingPlanRuntime) Config(
	_ string,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	ctx := options.Context
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("plan context has no deadline")
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (r *blockingPlanRuntime) ConfigFromPlan(
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	options.Apply = func(*plan.Plan) (map[string]cty.Value, []error, error) {
		r.applyCalled = true
		return nil, nil, nil
	}
	return executor.NewResearchConfigFromPlan(planned, options)
}

type runtimeFunc struct {
	plan  *plan.Plan
	apply func(context.Context) error
}

func (runtimeFunc) OpenProject(stateDirectory string) (string, string, error) {
	return filepath.Join(stateDirectory, "config"), filepath.Join(stateDirectory, "modules"), nil
}

func (runtimeFunc) SaveProjectOutputs(string, string, map[string]cty.Value) error {
	return nil
}

func (runtimeFunc) ReadProjectOutputs(string) ([]byte, error) {
	return []byte(`{}`), nil
}

func (r runtimeFunc) Config(
	_ string,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	return r.config(r.plan, options)
}

func (r runtimeFunc) ConfigFromPlan(
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	return r.config(planned, options)
}

func (r runtimeFunc) config(
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	options.Apply = func(*plan.Plan) (map[string]cty.Value, []error, error) {
		return nil, nil, r.apply(options.Context)
	}
	return executor.NewResearchConfigFromPlan(planned, options)
}
