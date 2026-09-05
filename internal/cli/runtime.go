package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/github/copilot-sdk/go"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/hclfuncs"
	artifactpkg "github.com/lonegunmanb/r42/internal/artifact"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	"github.com/lonegunmanb/r42/internal/config"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/evidence"
	"github.com/lonegunmanb/r42/internal/executor"
	modulespec "github.com/lonegunmanb/r42/internal/module/spec"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/project"
	"github.com/lonegunmanb/r42/internal/provider"
	"github.com/lonegunmanb/r42/internal/qc"
	researchruntime "github.com/lonegunmanb/r42/internal/research/runtime"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/lonegunmanb/r42/internal/run"
	internals3 "github.com/lonegunmanb/r42/internal/s3"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	externaltool "github.com/lonegunmanb/r42/internal/tool/external"
	"github.com/lonegunmanb/r42/internal/tool/gotool"
	toolspec "github.com/lonegunmanb/r42/internal/tool/spec"
	"github.com/lonegunmanb/r42/internal/tool/starlarktool"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

const (
	researchSystemProtocol         = "You are executing an unattended r42 research DAG block. Follow the configured tool and completion protocol."
	sessionRecoveryTimeout         = 10 * time.Second
	maxSessionStallRecoveries      = 1
	sessionStallContinuationPrompt = "The previous model turn stalled and was aborted. Continue the current task from the existing session and artifacts. Before retrying a tool call, check whether it already completed. Do not repeat successful work."
)

type Session interface {
	SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error)
	Close(context.Context) error
}

type SessionOpener interface {
	Open(context.Context, copilot.SessionConfig) (Session, error)
}

type RuntimeOptions struct {
	Sessions         SessionOpener
	StarlarkRunner   starlarkRunner
	S3ServiceFactory internals3.ServiceFactory
	S3EnvLookup      internals3.EnvLookup
}

type Engine struct {
	options RuntimeOptions
}

func NewRuntime() *Engine {
	return NewRuntimeWithOptions(RuntimeOptions{})
}

func NewRuntimeWithOptions(options RuntimeOptions) *Engine {
	return &Engine{options: options}
}

func (*Engine) InitProject(
	ctx context.Context,
	directory string,
	stateDirectory string,
	upgrade bool,
) error {
	return project.Init(ctx, directory, stateDirectory, project.InitOptions{Upgrade: upgrade})
}

func (*Engine) OpenProject(stateDirectory string) (string, string, error) {
	return project.Open(stateDirectory)
}

func (*Engine) SaveProjectOutputs(
	stateDirectory string,
	runDirectory string,
	outputs map[string]cty.Value,
) error {
	display, err := plan.DisplayValues(outputs)
	if err != nil {
		return fmt.Errorf("encode project outputs: %w", err)
	}
	return project.SaveOutputs(stateDirectory, runDirectory, []byte(display))
}

func (*Engine) ReadProjectOutputs(stateDirectory string) ([]byte, error) {
	return project.ReadOutputs(stateDirectory)
}

func (e *Engine) Config(
	directory string,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	ctx := options.Context
	if state := debugRunFromContext(ctx); state != nil {
		var activeRun *run.Run
		var err error
		ctx, activeRun, _, err = state.ensure(ctx, runDirectory(directory, options.RunDirectory))
		if err != nil {
			return nil, err
		}
		options.ReservedRunDirectory = activeRun.Directory()
	}
	apply := func(saved *plan.Plan) (map[string]cty.Value, []error, error) {
		return e.apply(ctx, saved, options)
	}
	started := time.Now()
	options.Context = ctx
	options.Apply = apply
	researchConfig, err := executor.NewResearchConfig(directory, options)
	if err != nil {
		logErr := recordLifecycleCompletion(ctx, "plan", started, err, debuglog.Event{Path: directory})
		return nil, errors.Join(err, logErr)
	}
	return researchConfig, nil
}

func (e *Engine) ConfigFromPlan(
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	ctx := options.Context
	options.Apply = func(saved *plan.Plan) (map[string]cty.Value, []error, error) {
		return e.apply(ctx, saved, options)
	}
	config, err := executor.NewResearchConfigFromPlan(planned, options)
	if err != nil {
		return nil, err
	}
	options.ReservedRunDirectory = config.Run().Directory()
	if state := debugRunFromContext(ctx); state != nil {
		ctx, _, _, err = state.ensureRun(ctx, config.Run())
		if err != nil {
			return nil, err
		}
	}
	return config, nil
}

func (e *Engine) apply(
	ctx context.Context,
	planned *plan.Plan,
	options executor.ResearchConfigOptions,
) (map[string]cty.Value, []error, error) {
	if planned == nil {
		return nil, nil, fmt.Errorf("saved plan is required")
	}
	state := debugRunFromContext(ctx)
	ownedDebugState := false
	var activeRun *run.Run
	var recorder *debuglog.Recorder
	var err error
	activeRun, err = runForPlan(
		planned,
		runDirectory(planned.Directory(), options.RunDirectory),
		options.ReservedRunDirectory,
	)
	if err != nil {
		return nil, nil, err
	}
	if options.Debug {
		if state == nil {
			state = &debugRun{enabled: true}
			ownedDebugState = true
		}
		ctx, activeRun, recorder, err = state.ensureRun(ctx, activeRun)
		if err != nil {
			return nil, nil, err
		}
	} else {
		if err = activeRun.Ensure(); err != nil {
			return nil, nil, err
		}
		recorder, err = debuglog.NewRecorder(activeRun.Directory(), false)
		if err != nil {
			return nil, nil, err
		}
		recorder.SetEventBus(debuglog.EventBusFromContext(ctx))
	}
	started := time.Now()
	if err = debuglog.Lifecycle(ctx, "apply", debuglog.StatusStarted, debuglog.Event{
		Path: planned.Directory(), Count: len(planned.Nodes()),
	}); err != nil {
		if ownedDebugState {
			_ = state.close()
		}
		return nil, nil, err
	}
	sessions := e.options.Sessions
	if sessions == nil {
		sessions = newOfficialSessionOpener()
	}
	factory := &runtimeFactory{
		results: make(map[string]cty.Value), run: activeRun, sessions: sessions, recorder: recorder,
		state: new(runtimeState), tools: planned.Tools(), directory: planned.Directory(),
		contextValues: planned.Context(), localExpressions: planned.LocalExpressions(),
		sessionStallTimeout: effectiveSessionStallTimeout(options.SessionStallTimeout),
		artifactRegistry:    artifactpkg.NewRegistry(),
		quoteRegistry:       evidence.NewQuoteRegistry(),
		starlarkRunner:      e.options.StarlarkRunner,
		s3ServiceFactory:    e.options.S3ServiceFactory,
		s3EnvLookup:         e.options.S3EnvLookup,
	}
	runner := executor.New(factory, nil)
	outputs, applyErr := runner.Apply(ctx, planned, options.Parallelism)
	warnings := runner.Warnings()
	warnings = append(warnings, factory.state.Warnings()...)
	if closer, ok := sessions.(interface{ Close() error }); ok {
		if closeErr := closer.Close(); closeErr != nil {
			warnings = append(warnings, fmt.Errorf("stop copilot client: %w", closeErr))
		}
	}
	if closeErr := factory.Close(); closeErr != nil {
		warnings = append(warnings, closeErr)
	}
	if logErr := recordLifecycleCompletion(ctx, "apply", started, applyErr, debuglog.Event{
		Path: planned.Directory(), Count: len(planned.Nodes()),
	}); logErr != nil {
		warnings = append(warnings, logErr)
	}
	if ownedDebugState {
		if closeErr := state.close(); closeErr != nil {
			warnings = append(warnings, fmt.Errorf("close debug log: %w", closeErr))
		}
	} else if !options.Debug {
		if closeErr := recorder.Close(); closeErr != nil {
			warnings = append(warnings, fmt.Errorf("close debug log: %w", closeErr))
		}
	}
	return outputs, warnings, applyErr
}

func runForPlan(planned *plan.Plan, fallbackRoot, reservedDirectory string) (*run.Run, error) {
	if planned.RunDirectory() != "" {
		return run.Open(planned.RunDirectory())
	}
	if reservedDirectory != "" {
		return run.Open(reservedDirectory)
	}
	return run.NewManager(fallbackRoot).Reserve()
}

func runDirectory(configurationDirectory, requested string) string {
	if strings.TrimSpace(requested) == "" {
		return configurationDirectory
	}
	return requested
}

func recordLifecycleCompletion(
	ctx context.Context,
	action string,
	started time.Time,
	operationErr error,
	event debuglog.Event,
) error {
	duration := time.Since(started).Milliseconds()
	event.DurationMS = &duration
	status := debuglog.StatusCompleted
	if operationErr != nil {
		status = debuglog.StatusFailed
		event.Error = operationErr.Error()
	}
	if err := debuglog.Lifecycle(ctx, action, status, event); err != nil {
		return fmt.Errorf("record %s lifecycle: %w", action, err)
	}
	return nil
}

type runtimeFactory struct {
	mu                  sync.Mutex
	results             map[string]cty.Value
	run                 *run.Run
	sessions            SessionOpener
	recorder            *debuglog.Recorder
	state               *runtimeState
	prefix              string
	tools               map[string]plan.ToolSpec
	directory           string
	contextValues       map[string]cty.Value
	localExpressions    map[string]string
	sessionStallTimeout time.Duration
	artifactRegistry    *artifactpkg.Registry
	quoteRegistry       *evidence.QuoteRegistry
	starlarkRunner      starlarkRunner
	s3ServiceFactory    internals3.ServiceFactory
	s3EnvLookup         internals3.EnvLookup
}

const (
	finalQCCalculatorToolID            = "r42_final_qc_calculator"
	finalQCCalculatorTimeout           = 5 * time.Second
	finalQCCalculatorMemoryLimit int64 = 128 << 20
)

func defaultFinalQCCalculatorDefinition() plan.ToolSpec {
	defaults := starlarktool.DefaultConfig()
	return plan.ToolSpec{
		ID:      finalQCCalculatorToolID,
		Address: "r42.starlark.final_qc_calculator",
		Kind:    string(config.AddressKindStarlark),
		Description: "Perform isolated, resource-bounded numerical calculations for Final QC. " +
			"code is Starlark source and data_json is one JSON value; read it as data " +
			"and assign a JSON-compatible value to top-level result. Available values " +
			"are data, math, stats, matrix, and fail; imports, files, network, and " +
			"processes are unavailable.",
		Starlark: &plan.StarlarkToolSpec{
			MaxSteps:       defaults.MaxSteps,
			TimeoutNanos:   int64(finalQCCalculatorTimeout),
			MaxSourceBytes: defaults.MaxSourceBytes,
			MaxDataBytes:   defaults.MaxDataBytes,
			MaxResultBytes: defaults.MaxResultBytes,
			MaxStdoutBytes: defaults.MaxStdoutBytes,
			MemoryLimit:    int(finalQCCalculatorMemoryLimit),
		},
	}
}

type finalQCCalculatorOptions struct {
	ctx               context.Context
	blockAddress      string
	sessionKind       debuglog.SessionKind
	configuredToolIDs []string
	tools             []sdk.Tool
	typedQuota        map[string]int
}

func (f *runtimeFactory) ensureFinalQCCalculator(
	opts finalQCCalculatorOptions,
) ([]sdk.Tool, string, error) {
	definitions, err := f.resolveToolDefinitions(opts.configuredToolIDs, nil)
	if err != nil {
		return nil, "", err
	}
	for _, definition := range definitions {
		if definition.Kind == string(config.AddressKindStarlark) {
			return opts.tools, definition.ID, nil
		}
	}
	definition := defaultFinalQCCalculatorDefinition()
	if opts.typedQuota == nil {
		opts.typedQuota = make(map[string]int)
	}
	opts.typedQuota[definition.ID] = 20
	calculator, err := f.buildStarlarkTool(
		opts.ctx, opts.blockAddress, opts.sessionKind, definition, newToolCallQuota(opts.typedQuota),
	)
	if err != nil {
		return nil, "", err
	}
	return append(opts.tools, calculator), definition.ID, nil
}

type starlarkRunner interface {
	Run(context.Context, starlarktool.WorkerRequest) (starlarktool.WorkerResponse, error)
}

type runtimeState struct {
	compilerMu sync.Mutex
	compiler   *gotool.Compiler
	warningsMu sync.Mutex
	warnings   []error
}

func (f *runtimeFactory) ensureArtifactRegistry() *artifactpkg.Registry {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.artifactRegistry == nil {
		f.artifactRegistry = artifactpkg.NewRegistry()
	}
	return f.artifactRegistry
}

func (f *runtimeFactory) ensureQuoteRegistry() *evidence.QuoteRegistry {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.quoteRegistry == nil {
		f.quoteRegistry = evidence.NewQuoteRegistry()
	}
	return f.quoteRegistry
}

func (s *runtimeState) addWarning(err error) {
	if err == nil {
		return
	}
	s.warningsMu.Lock()
	s.warnings = append(s.warnings, err)
	s.warningsMu.Unlock()
}

func (s *runtimeState) Warnings() []error {
	s.warningsMu.Lock()
	defer s.warningsMu.Unlock()
	return slices.Clone(s.warnings)
}

func (f *runtimeFactory) New(
	ctx context.Context,
	node plan.NodeSpec,
	scope *r42concurrency.Scope,
) (golden.ApplyBlock, error) {
	if node.Kind == "module" {
		return f.newModuleBlock(ctx, node, scope)
	}
	if node.Kind == "s3_folder" {
		return f.newS3FolderBlock(ctx, node)
	}
	if node.Kind != "research" {
		return nil, fmt.Errorf("unsupported apply node kind %q", node.Kind)
	}
	if strings.HasPrefix(node.Address, "research.dynamic.") {
		return f.newDynamicResearchBlock(ctx, node, scope)
	}
	planned, err := modulespec.DecodeResearchPlan(node.Config)
	if err != nil {
		return nil, err
	}
	if planned.Expression != "" {
		value, evaluateErr := f.evaluateResearchExpression(node.Address, planned.Expression, "research")
		if evaluateErr != nil {
			return nil, evaluateErr
		}
		configValue, decodeErr := researchspec.DecodeDynamicTask(value)
		if decodeErr != nil {
			return nil, decodeErr
		}
		configValue, err = researchspec.ResolveArtifactReferences(configValue)
		if err != nil {
			return nil, err
		}
		planned, err = planned.Resolve(configValue)
		if err != nil {
			return nil, err
		}
		if err = modulespec.ValidateResearchToolIDs(configValue, f.tools); err != nil {
			return nil, err
		}
	}
	workspace, err := f.run.Workspace(f.CanonicalAddress(node.Address))
	if err != nil {
		return nil, err
	}
	return f.newResearchBlock(ctx, node.Address, workspace, planned, f.publish)
}

func (f *runtimeFactory) openSession(
	ctx context.Context,
	address string,
	kind debuglog.SessionKind,
	config copilot.SessionConfig,
) (Session, error) {
	toolNames := make([]string, len(config.Tools))
	for index, tool := range config.Tools {
		toolNames[index] = tool.Name
	}
	event := debuglog.Event{
		BlockAddress: address,
		Session:      kind,
		Model:        config.Model,
		WorkingDir:   config.WorkingDirectory,
		ToolNames:    toolNames,
	}
	started := time.Now()
	if err := debuglog.Lifecycle(ctx, "session.open", debuglog.StatusStarted, event); err != nil {
		return nil, err
	}
	session, err := f.sessions.Open(ctx, config)
	logErr := debuglog.CompleteLifecycle(ctx, "session.open", started, err, event)
	if err == nil && logErr == nil {
		return session, nil
	}
	var closeErr error
	if session != nil {
		closeErr = session.Close(ctx)
	}
	return nil, errors.Join(err, logErr, closeErr)
}

func (f *runtimeFactory) newModuleBlock(
	ctx context.Context,
	node plan.NodeSpec,
	scope *r42concurrency.Scope,
) (golden.ApplyBlock, error) {
	if node.Module == nil || node.Module.Plan == nil {
		return nil, fmt.Errorf("module %s has no saved child plan", node.Address)
	}
	childScope, err := scope.Module(node.Module.Parallelism)
	if err != nil {
		return nil, err
	}
	executionAddress := f.CanonicalAddress(node.Address)
	childFactory := &runtimeFactory{
		results: make(map[string]cty.Value), run: f.run, sessions: f.sessions,
		recorder: f.recorder, state: f.state, prefix: executionAddress, tools: node.Module.Plan.Tools(),
		directory: node.Module.Plan.Directory(), contextValues: node.Module.Plan.Context(),
		localExpressions:    node.Module.Plan.LocalExpressions(),
		sessionStallTimeout: f.sessionStallTimeout,
		starlarkRunner:      f.starlarkRunner,
		s3ServiceFactory:    f.s3ServiceFactory,
		s3EnvLookup:         f.s3EnvLookup,
	}
	return &moduleApplyBlock{
		BaseBlock: new(golden.BaseBlock), ctx: ctx, address: node.Address,
		planned: node.Module.Plan, timeout: node.Module.Timeout, scope: childScope,
		factory: childFactory, publish: f.publish,
	}, nil
}

func (f *runtimeFactory) CanonicalAddress(address string) string {
	if f.prefix == "" {
		return address
	}
	return f.prefix + "." + address
}

type moduleApplyBlock struct {
	*golden.BaseBlock
	ctx      context.Context
	address  string
	planned  *plan.Plan
	timeout  time.Duration
	scope    *r42concurrency.Scope
	factory  *runtimeFactory
	publish  func(string, cty.Value)
	warnings []error
}

func (*moduleApplyBlock) Type() string            { return "" }
func (*moduleApplyBlock) BlockType() string       { return "module" }
func (*moduleApplyBlock) AddressLength() int      { return 2 }
func (*moduleApplyBlock) CanExecutePrePlan() bool { return false }
func (b *moduleApplyBlock) Address() string       { return b.address }

func (b *moduleApplyBlock) Apply() error {
	ctx := b.ctx
	if b.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.timeout)
		defer cancel()
	}
	runner := executor.New(b.factory, nil)
	outputs, err := runner.ApplyInScope(ctx, b.planned, b.scope)
	b.warnings = runner.Warnings()
	if err != nil {
		return err
	}
	b.publish(b.address, objectValue(outputs))
	return nil
}

func (b *moduleApplyBlock) Cleanup(context.Context) error {
	return errors.Join(b.warnings...)
}

func (f *runtimeFactory) buildTools(
	ctx context.Context,
	blockAddress string,
	sessionKind debuglog.SessionKind,
	workspace string,
	toolIDs []string,
	terminateToolID *string,
	terminal *researchruntime.TerminalRecorder,
	quota *toolCallQuota,
) ([]sdk.Tool, cty.Type, error) {
	definitions, err := f.resolveToolDefinitions(toolIDs, terminateToolID)
	if err != nil {
		return nil, cty.NilType, err
	}
	result := make([]sdk.Tool, 0, len(definitions))
	terminalType := cty.NilType
	for _, definition := range definitions {
		if definition.Kind == string(config.AddressKindStarlark) {
			tool, err := f.buildStarlarkTool(ctx, blockAddress, sessionKind, definition, quota)
			if err != nil {
				return nil, cty.NilType, err
			}
			result = append(result, tool)
			continue
		}
		if definition.Kind == string(config.AddressKindExternal) {
			tool, outputType, err := f.buildExternalTool(
				ctx, blockAddress, sessionKind, workspace, definition, terminateToolID, terminal, quota,
			)
			if err != nil {
				return nil, cty.NilType, err
			}
			if terminateToolID != nil && definition.ID == *terminateToolID {
				terminalType = outputType
			}
			result = append(result, tool)
			continue
		}
		if definition.Kind != string(config.AddressKindGo) {
			return nil, cty.NilType, fmt.Errorf("typed tool %s kind %s is not implemented", definition.Address, definition.Kind)
		}
		compiler, err := f.goCompiler()
		if err != nil {
			return nil, cty.NilType, err
		}
		program, err := compiler.Compile(ctx, definition.Source)
		if err != nil {
			return nil, cty.NilType, fmt.Errorf("compile %s: %w", definition.Address, err)
		}
		analysis := program.Analysis()
		input := toolspec.NewConstraint(analysis.InputType)
		parameters, err := input.JSONSchema()
		if err != nil {
			return nil, cty.NilType, fmt.Errorf("schema %s: %w", definition.Address, err)
		}
		isTerminal := terminateToolID != nil && definition.ID == *terminateToolID
		if isTerminal {
			terminalType = analysis.OutputType
		}
		result = append(result, sdk.Tool{
			Name: definition.ID, Description: quota.describe(definition.ID, definition.Description), Parameters: parameters,
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				arguments, marshalErr := json.Marshal(invocation.Arguments)
				if marshalErr != nil {
					return f.rejectedArguments(
						blockAddress, sessionKind, definition.ID, definition.Address,
						nil, marshalErr, isTerminal, terminal,
					)
				}
				if len(analysis.QuotePaths) > 0 {
					unknown, materializeErr := materializeQuoteReferences(invocation.Arguments, analysis.QuotePaths, f.ensureQuoteRegistry())
					if materializeErr != nil {
						return f.rejectedArguments(
							blockAddress, sessionKind, definition.ID, definition.Address,
							arguments, materializeErr, isTerminal, terminal,
						)
					}
					if len(unknown) > 0 {
						return rejectedArtifactReferenceResult(definition.ID, pointerValue(terminateToolID), terminal, "unknown_quote_ref", "unknown quote_ref values: "+strings.Join(unknown, ", "))
					}
					arguments, marshalErr = json.Marshal(invocation.Arguments)
					if marshalErr != nil {
						return f.rejectedArguments(
							blockAddress, sessionKind, definition.ID, definition.Address,
							nil, marshalErr, isTerminal, terminal,
						)
					}
				}
				value, decodeErr := ctyjson.Unmarshal(arguments, input.Type())
				if decodeErr != nil {
					return f.rejectedArguments(
						blockAddress, sessionKind, definition.ID, definition.Address,
						arguments, decodeErr, isTerminal, terminal,
					)
				}
				value, validateErr := input.Apply(value)
				if validateErr != nil {
					return f.rejectedArguments(
						blockAddress, sessionKind, definition.ID, definition.Address,
						arguments, validateErr, isTerminal, terminal,
					)
				}
				arguments, marshalErr = ctyjson.Marshal(value, input.Type())
				if marshalErr != nil {
					return sdk.ToolResult{}, fmt.Errorf("encode validated %s arguments: %w", definition.Address, marshalErr)
				}
				if reserveErr := quota.reserve(definition.ID); reserveErr != nil {
					return sdk.ToolResult{}, reserveErr
				}
				response, invokeErr := program.Invoke(ctx, arguments, workspace)
				if invokeErr != nil {
					quota.rollback(definition.ID)
					var stdout string
					var stderr string
					var invocationErr *gotool.InvocationError
					if errors.As(invokeErr, &invocationErr) {
						stdout = invocationErr.Stdout()
						stderr = invocationErr.Stderr()
					}
					if recordErr := f.recordToolFailure(
						blockAddress, sessionKind, definition.ID, definition.Address,
						arguments, invokeErr, stdout, stderr,
					); recordErr != nil {
						return sdk.ToolResult{}, errors.Join(invokeErr, recordErr)
					}
					return sdk.ToolResult{}, invokeErr
				}
				if !response.Accepted {
					quota.rollback(definition.ID)
				}
				outputValue := cty.NullVal(analysis.OutputType)
				if response.Output != nil {
					outputValue, decodeErr = ctyjson.Unmarshal(*response.Output, analysis.OutputType)
					if decodeErr != nil {
						return sdk.ToolResult{}, fmt.Errorf("decode %s output for postcondition: %w", definition.Address, decodeErr)
					}
				}
				if postconditionErr := evaluateToolPostconditions(value, outputValue, definition.Postconditions); postconditionErr != nil {
					if isTerminal {
						terminal.RecordError(postconditionErr)
					}
					return sdk.ToolResult{}, fmt.Errorf("%s postcondition failed: %w", definition.Address, postconditionErr)
				}
				wire := corespec.ToolResponse[json.RawMessage]{
					Accepted: response.Accepted, Output: response.Output, Issues: response.Issues,
				}
				encoded, encodeErr := json.Marshal(wire)
				if encodeErr != nil {
					return sdk.ToolResult{}, fmt.Errorf("encode %s response: %w", definition.Address, encodeErr)
				}
				if recordErr := f.recorder.Record(debuglog.Event{
					Kind: debuglog.EventTool, BlockAddress: blockAddress, Session: sessionKind,
					ToolName: definition.ID, ToolAddress: definition.Address,
					Arguments: arguments, Result: encoded, Stderr: response.Stderr,
				}); recordErr != nil {
					return sdk.ToolResult{}, recordErr
				}
				if isTerminal {
					recorded, terminalErr := toTerminalResponse(wire, analysis.OutputType)
					if terminalErr != nil {
						terminal.RecordError(terminalErr)
						return sdk.ToolResult{}, terminalErr
					}
					if recordErr := terminal.Record(recorded); recordErr != nil {
						return sdk.ToolResult{}, recordErr
					}
				}
				return sdk.ToolResult{TextResultForLLM: string(encoded), ResultType: "success"}, nil
			},
		})
	}
	return result, terminalType, nil
}

func (f *runtimeFactory) buildStarlarkTool(
	ctx context.Context,
	blockAddress string,
	sessionKind debuglog.SessionKind,
	definition plan.ToolSpec,
	quota *toolCallQuota,
) (sdk.Tool, error) {
	if definition.Starlark == nil {
		return sdk.Tool{}, fmt.Errorf("starlark tool %s has no settings", definition.Address)
	}
	input := toolspec.NewConstraint(cty.Object(map[string]cty.Type{"code": cty.String, "data_json": cty.String}))
	parameters, err := input.JSONSchema()
	if err != nil {
		return sdk.Tool{}, fmt.Errorf("schema %s: %w", definition.Address, err)
	}
	runner := f.starlarkRunner
	if runner == nil {
		runner = starlarktool.NewRunner()
	}
	settings := *definition.Starlark
	return sdk.Tool{
		Name: definition.ID, Description: quota.describe(definition.ID, definition.Description), Parameters: parameters,
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			arguments, marshalErr := json.Marshal(invocation.Arguments)
			if marshalErr != nil {
				return f.rejectedArguments(blockAddress, sessionKind, definition.ID, definition.Address, nil, marshalErr, false, nil)
			}
			value, decodeErr := ctyjson.Unmarshal(arguments, input.Type())
			if decodeErr != nil {
				return f.rejectedArguments(blockAddress, sessionKind, definition.ID, definition.Address, arguments, decodeErr, false, nil)
			}
			value, validateErr := input.Apply(value)
			if validateErr != nil {
				return f.rejectedArguments(blockAddress, sessionKind, definition.ID, definition.Address, arguments, validateErr, false, nil)
			}
			arguments, marshalErr = ctyjson.Marshal(value, input.Type())
			if marshalErr != nil {
				return sdk.ToolResult{}, fmt.Errorf("encode validated %s arguments: %w", definition.Address, marshalErr)
			}
			if reserveErr := quota.reserve(definition.ID); reserveErr != nil {
				return sdk.ToolResult{}, reserveErr
			}
			fields := value.AsValueMap()
			response, runErr := runner.Run(ctx, starlarktool.WorkerRequest{
				Code: fields["code"].AsString(), DataJSON: fields["data_json"].AsString(),
				Config: starlarktool.Config{
					MaxSteps: settings.MaxSteps, MaxSourceBytes: settings.MaxSourceBytes,
					MaxDataBytes: settings.MaxDataBytes, MaxResultBytes: settings.MaxResultBytes,
					MaxStdoutBytes: settings.MaxStdoutBytes,
				},
				TimeoutNanos: settings.TimeoutNanos, MemoryLimit: int64(settings.MemoryLimit),
			})
			if runErr != nil {
				quota.rollback(definition.ID)
				var stdout string
				var stderr string
				var invocationErr *starlarktool.InvocationError
				if errors.As(runErr, &invocationErr) {
					stdout = invocationErr.Stdout()
					stderr = invocationErr.Stderr()
				}
				if recordErr := f.recordToolFailure(blockAddress, sessionKind, definition.ID, definition.Address, arguments, runErr, stdout, stderr); recordErr != nil {
					return sdk.ToolResult{}, errors.Join(runErr, recordErr)
				}
				return sdk.ToolResult{}, runErr
			}
			if response.Error != nil {
				quota.rollback(definition.ID)
				issue := starlarkToolIssue(*response.Error)
				wire := corespec.ToolResponse[starlarktool.Result]{Issues: []corespec.Issue{issue}}
				return f.recordStarlarkToolResponse(blockAddress, sessionKind, definition, arguments, wire)
			}
			if response.Result == nil {
				quota.rollback(definition.ID)
				err := errors.New("starlark worker exited without a result or error")
				if recordErr := f.recordToolFailure(blockAddress, sessionKind, definition.ID, definition.Address, arguments, err, "", ""); recordErr != nil {
					return sdk.ToolResult{}, errors.Join(err, recordErr)
				}
				return sdk.ToolResult{}, err
			}
			wire := corespec.ToolResponse[starlarktool.Result]{Accepted: true, Output: response.Result}
			return f.recordStarlarkToolResponse(blockAddress, sessionKind, definition, arguments, wire)
		},
	}, nil
}

func (f *runtimeFactory) recordStarlarkToolResponse(
	blockAddress string,
	sessionKind debuglog.SessionKind,
	definition plan.ToolSpec,
	arguments []byte,
	wire corespec.ToolResponse[starlarktool.Result],
) (sdk.ToolResult, error) {
	encoded, err := json.Marshal(wire)
	if err != nil {
		return sdk.ToolResult{}, fmt.Errorf("encode %s response: %w", definition.Address, err)
	}
	if err = f.recorder.Record(debuglog.Event{
		Kind: debuglog.EventTool, BlockAddress: blockAddress, Session: sessionKind,
		ToolName: definition.ID, ToolAddress: definition.Address, Arguments: arguments, Result: encoded,
	}); err != nil {
		return sdk.ToolResult{}, err
	}
	return sdk.ToolResult{TextResultForLLM: string(encoded), ResultType: "success"}, nil
}

func starlarkToolIssue(workerError starlarktool.WorkerError) corespec.Issue {
	message := boundedDiagnostic(workerError.Message, 4_096)
	if stdout := boundedDiagnostic(workerError.Stdout, 2_048); stdout != "" {
		message += "\nstdout tail:\n" + stdout
	}
	repairHint := "Repair the Starlark source or input JSON, then call the calculator again."
	switch workerError.Code {
	case "starlark_step_limit", "starlark_timeout", "starlark_output_limit":
		repairHint = "Reduce the calculation size or output, then call the calculator again."
	case "starlark_data_json":
		repairHint = "Provide one valid JSON value in data_json, then call the calculator again."
	}
	return corespec.Issue{Code: workerError.Code, Message: message, RepairHint: &repairHint}
}

func boundedDiagnostic(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

func (f *runtimeFactory) resolveToolDefinitions(
	toolIDs []string,
	terminateToolID *string,
) ([]plan.ToolSpec, error) {
	ids := slices.Clone(toolIDs)
	if terminateToolID != nil && !slices.Contains(ids, *terminateToolID) {
		ids = append(ids, *terminateToolID)
	}
	definitions := make([]plan.ToolSpec, 0, len(ids))
	for _, id := range ids {
		definition, ok := f.tools[id]
		if !ok {
			return nil, fmt.Errorf("typed tool id %q was not planned", id)
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func (f *runtimeFactory) buildExternalTool(
	ctx context.Context,
	blockAddress string,
	sessionKind debuglog.SessionKind,
	workspace string,
	definition plan.ToolSpec,
	terminateToolID *string,
	terminal *researchruntime.TerminalRecorder,
	quota *toolCallQuota,
) (sdk.Tool, cty.Type, error) {
	input, err := parseConstraint(definition.InputTypeExpression)
	if err != nil {
		return sdk.Tool{}, cty.NilType, fmt.Errorf("input type %s: %w", definition.Address, err)
	}
	output, err := parseConstraint(definition.OutputTypeExpression)
	if err != nil {
		return sdk.Tool{}, cty.NilType, fmt.Errorf("output type %s: %w", definition.Address, err)
	}
	parameters, err := input.JSONSchema()
	if err != nil {
		return sdk.Tool{}, cty.NilType, err
	}
	isTerminal := terminateToolID != nil && definition.ID == *terminateToolID
	runner := externaltool.NewRunner()
	tool := sdk.Tool{
		Name: definition.ID, Description: quota.describe(definition.ID, definition.Description), Parameters: parameters,
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			arguments, marshalErr := json.Marshal(invocation.Arguments)
			if marshalErr != nil {
				return f.rejectedArguments(
					blockAddress, sessionKind, definition.ID, definition.Address,
					nil, marshalErr, isTerminal, terminal,
				)
			}
			value, decodeErr := ctyjson.Unmarshal(arguments, input.Type())
			if decodeErr != nil {
				return f.rejectedArguments(
					blockAddress, sessionKind, definition.ID, definition.Address,
					arguments, decodeErr, isTerminal, terminal,
				)
			}
			value, validateErr := input.Apply(value)
			if validateErr != nil {
				return f.rejectedArguments(
					blockAddress, sessionKind, definition.ID, definition.Address,
					arguments, validateErr, isTerminal, terminal,
				)
			}
			if reserveErr := quota.reserve(definition.ID); reserveErr != nil {
				return sdk.ToolResult{}, reserveErr
			}
			result, runErr := runner.Run(ctx, externaltool.Config{
				Program: definition.Program, Workspace: workspace, WorkingDir: definition.WorkingDir,
				Input: input, Output: output,
			}, value)
			if runErr != nil {
				quota.rollback(definition.ID)
				var stdout string
				var stderr string
				var executionErr *externaltool.ExecutionError
				if errors.As(runErr, &executionErr) {
					stdout = executionErr.Stdout()
					stderr = executionErr.Stderr()
				}
				if recordErr := f.recordToolFailure(
					blockAddress, sessionKind, definition.ID, definition.Address,
					arguments, runErr, stdout, stderr,
				); recordErr != nil {
					return sdk.ToolResult{}, errors.Join(runErr, recordErr)
				}
				return sdk.ToolResult{}, runErr
			}
			if !result.Accepted {
				quota.rollback(definition.ID)
			}
			outputValue := cty.NullVal(output.Type())
			if result.Output != nil {
				outputValue = *result.Output
			}
			if postconditionErr := evaluateToolPostconditions(value, outputValue, definition.Postconditions); postconditionErr != nil {
				if isTerminal {
					terminal.RecordError(postconditionErr)
				}
				return sdk.ToolResult{}, fmt.Errorf("%s postcondition failed: %w", definition.Address, postconditionErr)
			}
			var rawOutput *json.RawMessage
			if result.Output != nil {
				encodedOutput, encodeErr := ctyjson.Marshal(*result.Output, output.Type())
				if encodeErr != nil {
					return sdk.ToolResult{}, encodeErr
				}
				raw := json.RawMessage(encodedOutput)
				rawOutput = &raw
			}
			wire := corespec.ToolResponse[json.RawMessage]{
				Accepted: result.Accepted, Output: rawOutput, Issues: result.Issues,
			}
			encoded, encodeErr := json.Marshal(wire)
			if encodeErr != nil {
				return sdk.ToolResult{}, encodeErr
			}
			if recordErr := f.recorder.Record(debuglog.Event{
				Kind: debuglog.EventTool, BlockAddress: blockAddress, Session: sessionKind,
				ToolName: definition.ID, ToolAddress: definition.Address,
				Arguments: arguments, Result: encoded, Stderr: result.Stderr,
			}); recordErr != nil {
				return sdk.ToolResult{}, recordErr
			}
			if isTerminal {
				recorded, terminalErr := toTerminalResponse(wire, output.Type())
				if terminalErr != nil {
					terminal.RecordError(terminalErr)
					return sdk.ToolResult{}, terminalErr
				}
				if recordErr := terminal.Record(recorded); recordErr != nil {
					return sdk.ToolResult{}, recordErr
				}
			}
			return sdk.ToolResult{TextResultForLLM: string(encoded), ResultType: "success"}, nil
		},
	}
	return tool, output.Type(), nil
}

func evaluateToolPostconditions(input, output cty.Value, conditions []corespec.Condition) error {
	if len(conditions) == 0 {
		return nil
	}
	for _, condition := range conditions {
		if _, err := condition.Evaluate(
			map[string]cty.Value{"input": input, "output": output}, nil,
		); err != nil {
			return err
		}
	}
	return nil
}

type toolCallQuota struct {
	mu     sync.Mutex
	limits map[string]int
	used   map[string]int
}

func newToolCallQuota(limits map[string]int) *toolCallQuota {
	return &toolCallQuota{limits: maps.Clone(limits), used: make(map[string]int)}
}

func (q *toolCallQuota) reserve(toolName string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	limit, configured := q.limits[toolName]
	if !configured {
		return nil
	}
	if q.used[toolName] >= limit {
		return fmt.Errorf(
			"tool %q per-session call quota exhausted (limit %d successful calls); "+
				"this quota will not reset during this session; do not call this tool again; "+
				"continue with existing results or another available tool",
			toolName, limit,
		)
	}
	q.used[toolName]++
	return nil
}

func (q *toolCallQuota) describe(toolID, description string) string {
	limit, configured := q.limits[toolID]
	if !configured {
		return description
	}
	return fmt.Sprintf(
		"%s\n\nr42 per-session call quota: at most %d accepted calls. "+
			"Invalid arguments, execution errors, and accepted=false responses do not consume this quota. "+
			"Once exhausted, this quota will not reset during this session; do not call this tool again.",
		description, limit,
	)
}

func (q *toolCallQuota) rollback(toolID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, configured := q.limits[toolID]; configured && q.used[toolID] > 0 {
		q.used[toolID]--
	}
}

func splitToolCallQuota(limits map[string]int) (map[string]int, map[string]int) {
	typed := make(map[string]int)
	builtIn := make(map[string]int)
	for name, limit := range limits {
		if plan.IsToolID(name) {
			typed[name] = limit
			continue
		}
		builtIn[name] = limit
	}
	return typed, builtIn
}

func builtInToolCallQuotaHooks(quota *toolCallQuota) *sdk.SessionHooks {
	if quota == nil || len(quota.limits) == 0 {
		return nil
	}
	return &sdk.SessionHooks{
		OnPreToolUse: func(input sdk.PreToolUseHookInput, _ sdk.HookInvocation) (*sdk.PreToolUseHookOutput, error) {
			return toolCallQuotaDecision(quota, input.ToolName), nil
		},
		OnPostToolUseFailure: func(
			input sdk.PostToolUseFailureHookInput,
			_ sdk.HookInvocation,
		) (*sdk.PostToolUseFailureHookOutput, error) {
			quota.rollback(input.ToolName)
			return &sdk.PostToolUseFailureHookOutput{}, nil
		},
	}
}

func toolCallQuotaDecision(quota *toolCallQuota, toolName string) *sdk.PreToolUseHookOutput {
	reservationError := quota.reserve(toolName)
	if reservationError == nil {
		return &sdk.PreToolUseHookOutput{PermissionDecision: "allow"}
	}
	return &sdk.PreToolUseHookOutput{
		PermissionDecision:       "deny",
		PermissionDecisionReason: reservationError.Error(),
	}
}

func appendBuiltInToolCallQuotaPrompt(systemPrompt string, limits map[string]int) string {
	if len(limits) == 0 {
		return systemPrompt
	}
	names := make([]string, 0, len(limits))
	for name := range limits {
		names = append(names, name)
	}
	slices.Sort(names)

	var result strings.Builder
	result.WriteString(systemPrompt)
	result.WriteString("\n\nr42 built-in tool call quotas for this session:\n")
	for _, name := range names {
		fmt.Fprintf(&result, "- %s: at most %d successful calls.\n", name, limits[name])
	}
	result.WriteString("failed calls do not consume quota. Once a tool is exhausted, do not retry it; continue with existing results or another available tool.")
	return result.String()
}

func (f *runtimeFactory) recordToolFailure(
	blockAddress string,
	sessionKind debuglog.SessionKind,
	toolID string,
	toolAddress string,
	arguments []byte,
	cause error,
	stdout string,
	stderr string,
) error {
	failure, _ := json.Marshal(map[string]string{"error": cause.Error()})
	return f.recorder.Record(debuglog.Event{
		Kind: debuglog.EventTool, BlockAddress: blockAddress, Session: sessionKind,
		ToolName: toolID, ToolAddress: toolAddress, Arguments: arguments, Result: failure,
		Stdout: stdout, Stderr: stderr,
	})
}

func (f *runtimeFactory) rejectedArguments(
	blockAddress string,
	sessionKind debuglog.SessionKind,
	toolID string,
	toolAddress string,
	arguments []byte,
	cause error,
	isTerminal bool,
	terminal *researchruntime.TerminalRecorder,
) (sdk.ToolResult, error) {
	issues := []corespec.Issue{{Code: "invalid_arguments", Message: cause.Error()}}
	wire := corespec.ToolResponse[json.RawMessage]{Issues: issues}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return sdk.ToolResult{}, err
	}
	if err = f.recorder.Record(debuglog.Event{
		Kind: debuglog.EventTool, BlockAddress: blockAddress, Session: sessionKind,
		ToolName: toolID, ToolAddress: toolAddress, Arguments: arguments, Result: encoded,
	}); err != nil {
		return sdk.ToolResult{}, err
	}
	if isTerminal {
		if err = terminal.Record(corespec.ToolResponse[string]{Issues: issues}); err != nil {
			return sdk.ToolResult{}, err
		}
	}
	return sdk.ToolResult{TextResultForLLM: string(encoded), ResultType: "success"}, nil
}

func parseConstraint(source string) (toolspec.Constraint, error) {
	expression, diagnostics := hclsyntax.ParseExpression([]byte(source), "saved-tool-type", hcl.InitialPos)
	if diagnostics.HasErrors() {
		return toolspec.Constraint{}, diagnostics
	}
	typeValue, defaults, diagnostics := typeexpr.TypeConstraintWithDefaults(expression)
	if diagnostics.HasErrors() {
		return toolspec.Constraint{}, diagnostics
	}
	if err := corespec.ValidateType(typeValue); err != nil {
		return toolspec.Constraint{}, err
	}
	return toolspec.NewConstraintWithDefaults(typeValue, defaults), nil
}

func toTerminalResponse(
	response corespec.ToolResponse[json.RawMessage],
	outputType cty.Type,
) (corespec.ToolResponse[string], error) {
	result := corespec.ToolResponse[string]{Accepted: response.Accepted, Issues: response.Issues}
	if response.Output == nil {
		return result, nil
	}
	value, err := ctyjson.Unmarshal(*response.Output, outputType)
	if err != nil {
		return result, fmt.Errorf("decode terminal output: %w", err)
	}
	value, err = convert.Convert(value, cty.String)
	if err != nil {
		return result, fmt.Errorf("convert terminal output to string: %w", err)
	}
	output := value.AsString()
	result.Output = &output
	return result, nil
}

func (f *runtimeFactory) goCompiler() (*gotool.Compiler, error) {
	f.state.compilerMu.Lock()
	defer f.state.compilerMu.Unlock()
	if f.state.compiler != nil {
		return f.state.compiler, nil
	}
	compiler, err := gotool.NewCompiler()
	if err != nil {
		return nil, err
	}
	f.state.compiler = compiler
	return compiler, nil
}

func (f *runtimeFactory) Close() error {
	f.state.compilerMu.Lock()
	defer f.state.compilerMu.Unlock()
	if f.state.compiler == nil {
		return nil
	}
	err := f.state.compiler.Close()
	f.state.compiler = nil
	return err
}

func (f *runtimeFactory) ResolveOutputs(planned *plan.Plan) (map[string]cty.Value, error) {
	contextValues := planned.Context()
	f.mu.Lock()
	for address, value := range f.results {
		setBlockResult(contextValues, address, value)
	}
	f.mu.Unlock()
	functions := hclfuncs.Functions(planned.Directory())
	maps.Copy(functions, config.Functions())
	evaluationContext := &hcl.EvalContext{Variables: contextValues, Functions: functions}
	if err := evaluateLocals(evaluationContext, planned.LocalExpressions()); err != nil {
		return nil, err
	}
	result := make(map[string]cty.Value, len(planned.Outputs()))
	for name, output := range planned.Outputs() {
		if output.Value.IsWhollyKnown() || output.Expression == "" {
			result[name] = output.Value
			continue
		}
		expression, diagnostics := hclsyntax.ParseExpression(
			[]byte(output.Expression), "saved-plan-output", hcl.InitialPos,
		)
		if diagnostics.HasErrors() {
			return nil, fmt.Errorf("parse output %q expression: %w", name, diagnostics)
		}
		value, diagnostics := expression.Value(evaluationContext)
		if diagnostics.HasErrors() {
			return nil, fmt.Errorf("evaluate output %q: %w", name, diagnostics)
		}
		if !value.IsWhollyKnown() {
			return nil, fmt.Errorf("output %q is still unknown after apply", name)
		}
		if corespec.IsSensitive(output.Value) && !corespec.IsSensitive(value) {
			value = corespec.MarkSensitive(value)
		}
		result[name] = value
	}
	return result, nil
}

func evaluateLocals(evaluationContext *hcl.EvalContext, expressions map[string]string) error {
	if len(expressions) == 0 {
		return nil
	}
	values := make(map[string]cty.Value, len(expressions))
	if existing, ok := evaluationContext.Variables["local"]; ok && existing.Type().IsObjectType() {
		maps.Copy(values, existing.AsValueMap())
	}
	pending := maps.Clone(expressions)
	for len(pending) > 0 {
		progress := false
		for name, source := range pending {
			expression, diagnostics := hclsyntax.ParseExpression(
				[]byte(source), "saved-plan-local", hcl.InitialPos,
			)
			if diagnostics.HasErrors() {
				return fmt.Errorf("parse local %q expression: %w", name, diagnostics)
			}
			value, diagnostics := expression.Value(evaluationContext)
			if diagnostics.HasErrors() {
				return fmt.Errorf("evaluate local %q: %w", name, diagnostics)
			}
			if !value.IsWhollyKnown() {
				continue
			}
			values[name] = value
			evaluationContext.Variables["local"] = cty.ObjectVal(values)
			delete(pending, name)
			progress = true
		}
		if !progress {
			break
		}
	}
	return nil
}

func setBlockResult(contextValues map[string]cty.Value, address string, value cty.Value) {
	path, instanceKey, ok := splitBlockResultAddress(address)
	if !ok || len(path) < 2 {
		return
	}
	namespace, exists := contextValues[path[0]]
	if !exists {
		return
	}
	updated, ok := setBlockResultPath(namespace, path[1:], instanceKey, value)
	if !ok {
		return
	}
	contextValues[path[0]] = updated
}

func splitBlockResultAddress(address string) ([]string, *string, bool) {
	var instanceKey *string
	if open := strings.LastIndexByte(address, '['); open >= 0 {
		if open == 0 || !strings.HasSuffix(address, "]") || open == len(address)-2 {
			return nil, nil, false
		}
		key := address[open+1 : len(address)-1]
		instanceKey = &key
		address = address[:open]
	}
	if strings.Contains(address, "]") {
		return nil, nil, false
	}
	path := strings.Split(address, ".")
	if slices.Contains(path, "") {
		return nil, nil, false
	}
	return path, instanceKey, true
}

func setBlockResultPath(
	namespace cty.Value,
	path []string,
	instanceKey *string,
	value cty.Value,
) (cty.Value, bool) {
	if len(path) == 0 || !namespace.Type().IsObjectType() {
		return cty.NilVal, false
	}
	values := namespace.AsValueMap()
	existing, exists := values[path[0]]
	if !exists {
		return cty.NilVal, false
	}
	if len(path) > 1 {
		updated, ok := setBlockResultPath(existing, path[1:], instanceKey, value)
		if !ok {
			return cty.NilVal, false
		}
		values[path[0]] = updated
		return cty.ObjectVal(values), true
	}
	if instanceKey == nil {
		values[path[0]] = mergeBlockResult(existing, value)
		return cty.ObjectVal(values), true
	}
	if !existing.Type().IsObjectType() {
		return cty.NilVal, false
	}
	instances := existing.AsValueMap()
	instance, exists := instances[*instanceKey]
	if !exists {
		return cty.NilVal, false
	}
	instances[*instanceKey] = mergeBlockResult(instance, value)
	values[path[0]] = cty.ObjectVal(instances)
	return cty.ObjectVal(values), true
}

func mergeBlockResult(existing, result cty.Value) cty.Value {
	if !existing.Type().IsObjectType() || !result.Type().IsObjectType() {
		return result
	}
	values := existing.AsValueMap()
	maps.Copy(values, result.AsValueMap())
	return cty.ObjectVal(values)
}

func (f *runtimeFactory) publish(address string, value cty.Value) {
	f.mu.Lock()
	f.results[address] = value
	f.mu.Unlock()
}

func qcVerdictTool(address string, recorder *debuglog.Recorder, verdicts *qc.VerdictRecorder) sdk.Tool {
	return sdk.Tool{
		Name:        "r42_qc_verdict",
		Description: "Submit pass with no issues, or revise_research when a direct QC repair still needs another confirmation attempt. Final QC repairs the candidate itself and never reopens Collection or starts Research.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"decision": map[string]any{
					"type": "string",
					"enum": []string{"pass", "revise_research"},
				},
				"issues": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":          map[string]any{"type": "string"},
							"code":        map[string]any{"type": "string"},
							"message":     map[string]any{"type": "string"},
							"path":        map[string]any{"type": "string"},
							"repair_hint": map[string]any{"type": "string"},
						},
						"required":             []string{"id", "code", "message"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"decision"},
			"additionalProperties": false,
		},
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			arguments, marshalErr := json.Marshal(invocation.Arguments)
			if marshalErr != nil {
				return sdk.ToolResult{}, marshalErr
			}
			var verdict qc.Verdict
			if unmarshalErr := json.Unmarshal(arguments, &verdict); unmarshalErr != nil {
				return sdk.ToolResult{}, unmarshalErr
			}
			if validationErr := verdict.Validate(); validationErr != nil {
				rejected, _ := json.Marshal(corespec.ToolResponse[any]{
					Accepted: false,
					Issues:   []corespec.Issue{{Code: "invalid_verdict", Message: validationErr.Error()}},
				})
				//nolint:nilerr // Invalid verdicts are repairable tool results, not handler failures.
				return sdk.ToolResult{TextResultForLLM: string(rejected), ResultType: "success"}, nil
			}
			if recordErr := verdicts.RecordFinal(verdict); recordErr != nil {
				verdicts.RecordError(recordErr)
				allowed := verdicts.FinalIssues()
				ids := make([]string, 0, len(allowed))
				for _, issue := range allowed {
					ids = append(ids, issue.ID)
				}
				return rejectedToolResult("invalid_qc_issue_transition", fmt.Sprintf("%s; allowed issue IDs: %s", recordErr, strings.Join(ids, ", ")))
			}
			result, _ := json.Marshal(map[string]any{"accepted": true})
			if recordErr := recorder.Record(debuglog.Event{
				Kind: debuglog.EventTool, BlockAddress: address, Session: debuglog.SessionQC,
				ToolName: "r42_qc_verdict", Arguments: arguments, Result: result,
			}); recordErr != nil {
				return sdk.ToolResult{}, recordErr
			}
			return sdk.ToolResult{TextResultForLLM: string(result), ResultType: "success"}, nil
		},
	}
}

func qcUpdateIssuesTool(address string, recorder *debuglog.Recorder, verdicts *qc.VerdictRecorder) sdk.Tool {
	type issueUpdate struct {
		Action   string           `json:"action"`
		Issues   []corespec.Issue `json:"issues,omitempty"`
		IssueIDs []string         `json:"issue_ids,omitempty"`
	}
	return sdk.Tool{
		Name:        "r42_qc_update_issues",
		Description: "Record or resolve Final-QC semantic issues. Use action=open with issue details but no id; the host assigns stable FQ-* IDs. After repairing an issue, use action=resolve with its exact host ID. Use action=list to inspect active issues. This tool never changes the candidate artifact.",
		Parameters: objectSchema(map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"open", "resolve", "list"}},
			"issues": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "properties": map[string]any{
					"code": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"},
					"path": map[string]any{"type": "string"}, "repair_hint": map[string]any{"type": "string"},
				}, "required": []string{"code", "message"}, "additionalProperties": false,
			}},
			"issue_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, []string{"action"}),
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			arguments, err := decodeArguments[issueUpdate](invocation.Arguments)
			if err != nil {
				return rejectedToolResult("invalid_qc_issue_update", err.Error())
			}
			var output any
			switch arguments.Action {
			case "open":
				if len(arguments.Issues) == 0 {
					return rejectedToolResult("invalid_qc_issue_update", "open requires at least one issue")
				}
				opened, openErr := verdicts.OpenFinalIssues(arguments.Issues)
				if openErr != nil {
					return rejectedToolResult("invalid_qc_issue_update", openErr.Error())
				}
				output = map[string]any{"opened": opened, "active_issues": verdicts.FinalIssues()}
			case "resolve":
				if len(arguments.IssueIDs) == 0 {
					return rejectedToolResult("invalid_qc_issue_update", "resolve requires at least one issue ID")
				}
				if resolveErr := verdicts.ResolveFinalIssues(arguments.IssueIDs); resolveErr != nil {
					return rejectedToolResult("invalid_qc_issue_update", resolveErr.Error())
				}
				output = map[string]any{"resolved_ids": arguments.IssueIDs, "active_issues": verdicts.FinalIssues()}
			case "list":
				output = map[string]any{"active_issues": verdicts.FinalIssues()}
			default:
				return rejectedToolResult("invalid_qc_issue_update", fmt.Sprintf("unsupported action %q", arguments.Action))
			}
			result, resultErr := acceptedToolResult(output)
			if resultErr != nil {
				return sdk.ToolResult{}, resultErr
			}
			if recorder != nil {
				encoded, _ := json.Marshal(invocation.Arguments)
				if recordErr := recorder.Record(debuglog.Event{Kind: debuglog.EventTool, BlockAddress: address, Session: debuglog.SessionFinalQC, ToolName: "r42_qc_update_issues", Arguments: encoded, Result: []byte(result.TextResultForLLM)}); recordErr != nil {
					return sdk.ToolResult{}, recordErr
				}
			}
			return result, nil
		},
	}
}

func qcCompleteTool(address string, recorder *debuglog.Recorder, verdicts *qc.VerdictRecorder) sdk.Tool {
	return sdk.Tool{
		Name:        "r42_qc_complete",
		Description: "Finish Final QC after directly repairing the candidate. This tool has no issue payload: it succeeds only when every issue previously registered with r42_qc_update_issues has been explicitly resolved.",
		Parameters:  objectSchema(map[string]any{}, nil),
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			if err := verdicts.RecordFinalCompletion(); err != nil {
				issues := verdicts.FinalIssues()
				issues = append([]corespec.Issue{{Code: "unresolved_qc_issues", Message: err.Error()}}, issues...)
				return responseToolResult(corespec.ToolResponse[any]{Issues: issues})
			}
			result, err := acceptedToolResult(map[string]any{"active_issues": verdicts.FinalIssues()})
			if err != nil {
				return sdk.ToolResult{}, err
			}
			if recorder != nil {
				arguments, _ := json.Marshal(invocation.Arguments)
				if recordErr := recorder.Record(debuglog.Event{Kind: debuglog.EventTool, BlockAddress: address, Session: debuglog.SessionFinalQC, ToolName: "r42_qc_complete", Arguments: arguments, Result: []byte(result.TextResultForLLM)}); recordErr != nil {
					return sdk.ToolResult{}, recordErr
				}
			}
			return result, nil
		},
	}
}

type researchApplyBlock struct {
	*golden.BaseBlock
	ctx              context.Context
	address          string
	session          Session
	runner           *researchruntime.Runner
	qcSession        Session
	qcRunner         *qc.Runner
	qcConfig         qc.Config
	config           researchruntime.Config
	publish          func(string, cty.Value)
	cancel           context.CancelFunc
	workflowRun      func(context.Context) (researchruntime.Result, error)
	workflowSessions []Session
	afterSuccess     func()
}

func (*researchApplyBlock) Type() string            { return "static" }
func (*researchApplyBlock) BlockType() string       { return "research" }
func (*researchApplyBlock) AddressLength() int      { return 3 }
func (*researchApplyBlock) CanExecutePrePlan() bool { return false }
func (b *researchApplyBlock) Address() string       { return b.address }

func (b *researchApplyBlock) Apply() error {
	var result researchruntime.Result
	var err error
	switch {
	case b.workflowRun != nil:
		result, err = b.workflowRun(b.ctx)
	case b.qcRunner == nil:
		result, err = b.runner.Run(b.ctx, b.config)
	default:
		var reviewed qc.Result
		reviewed, err = b.qcRunner.Run(b.ctx, b.qcConfig)
		result = reviewed.Candidate
	}
	if err != nil {
		return err
	}
	if b.afterSuccess != nil {
		b.afterSuccess()
	}
	value := map[string]cty.Value{
		"artifact": researchspec.ArtifactsValueWithIDs(b.config.Artifacts, result.Artifacts, b.config.ArtifactIDs),
	}
	if b.config.TerminateToolName != "" {
		value["result"] = cty.NullVal(cty.String)
		if result.Value != nil {
			value["result"] = cty.StringVal(*result.Value)
		}
	}
	b.publish(b.address, cty.ObjectVal(value))
	return nil
}

func (b *researchApplyBlock) Cleanup(ctx context.Context) error {
	if b.cancel != nil {
		defer b.cancel()
	}
	if len(b.workflowSessions) > 0 {
		var closeErr error
		for _, session := range slices.Backward(b.workflowSessions) {
			closeErr = errors.Join(closeErr, session.Close(ctx))
		}
		return closeErr
	}
	var qcErr error
	if b.qcSession != nil {
		qcErr = b.qcSession.Close(ctx)
	}
	return errors.Join(qcErr, b.session.Close(ctx))
}

type recordingSession struct {
	Session
	recorder           *debuglog.Recorder
	address            string
	kind               debuglog.SessionKind
	stallTimeout       time.Duration
	terminationTimeout time.Duration
	typedToolActivity  *typedToolActivity
	sendMu             sync.Mutex
	eventMu            sync.Mutex
	kindMu             sync.RWMutex
	seen               map[string]struct{}
	toolName           map[string]string
	lastEvent          string
	tainted            bool
}

type recoverableSession interface {
	Abort(context.Context) error
	Resume(context.Context) error
}

type sessionSendResult struct {
	event *sdk.SessionEvent
	err   error
}

type sessionProgress struct {
	mu              sync.Mutex
	activeTools     map[string]struct{}
	activeAgents    map[string]int
	callbacks       int
	acceptCallbacks bool
	sessionIdle     bool
	lastActivity    time.Time
	activitySignal  chan struct{}
}

type typedToolActivity struct {
	mu             sync.Mutex
	active         int
	recovering     bool
	lastActivity   time.Time
	activitySignal chan struct{}
}

func newSessionProgress() *sessionProgress {
	return &sessionProgress{
		activeTools: make(map[string]struct{}), activeAgents: make(map[string]int),
		acceptCallbacks: true, lastActivity: time.Now(), activitySignal: make(chan struct{}, 1),
	}
}

func newTypedToolActivity() *typedToolActivity {
	return &typedToolActivity{activitySignal: make(chan struct{}, 1)}
}

func trackTypedToolActivity(tools []sdk.Tool, activity *typedToolActivity) {
	for index := range tools {
		tools[index].Handler = trackedToolHandler(tools[index].Handler, activity)
	}
}

func trackedToolHandler(handler sdk.ToolHandler, activity *typedToolActivity) sdk.ToolHandler {
	return func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
		if !activity.begin() {
			return sdk.ToolResult{}, fmt.Errorf(
				"typed tool invocation rejected because session recovery is in progress",
			)
		}
		defer activity.finish()
		return handler(invocation)
	}
}

func (a *typedToolActivity) begin() bool {
	a.mu.Lock()
	if a.recovering {
		a.mu.Unlock()
		return false
	}
	a.active++
	a.lastActivity = time.Now()
	a.mu.Unlock()
	a.signalActivity()
	return true
}

func (a *typedToolActivity) finish() {
	a.mu.Lock()
	a.active--
	a.lastActivity = time.Now()
	a.mu.Unlock()
	a.signalActivity()
}

func (a *typedToolActivity) state() (time.Time, bool) {
	if a == nil {
		return time.Time{}, true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastActivity, a.active == 0
}

func (a *typedToolActivity) signalActivity() {
	select {
	case a.activitySignal <- struct{}{}:
	default:
	}
}

func (a *typedToolActivity) signal() <-chan struct{} {
	if a == nil {
		return nil
	}
	return a.activitySignal
}

func (a *typedToolActivity) beginRecovery() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.recovering = true
	a.mu.Unlock()
}

func (a *typedToolActivity) endRecovery() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.recovering = false
	a.lastActivity = time.Now()
	a.mu.Unlock()
	a.signalActivity()
}

func (p *sessionProgress) beginCallback() bool {
	p.mu.Lock()
	if !p.acceptCallbacks {
		p.mu.Unlock()
		return false
	}
	p.callbacks++
	p.lastActivity = time.Now()
	p.mu.Unlock()
	p.signalActivity()
	return true
}

func (p *sessionProgress) finishCallback() {
	p.mu.Lock()
	p.callbacks--
	p.mu.Unlock()
	p.signalActivity()
}

func (p *sessionProgress) track(event sdk.SessionEvent) {
	p.mu.Lock()
	switch data := event.Data.(type) {
	case *sdk.ToolExecutionStartData:
		p.activeTools[data.ToolCallID] = struct{}{}
	case *sdk.ToolExecutionCompleteData:
		delete(p.activeTools, data.ToolCallID)
	case *sdk.SubagentStartedData:
		p.activeAgents[subagentKey(data.ToolCallID, data.AgentName)]++
	case *sdk.SubagentCompletedData:
		p.completeSubagent(data.ToolCallID, data.AgentName)
	case *sdk.SubagentFailedData:
		p.completeSubagent(data.ToolCallID, data.AgentName)
	case *sdk.SessionIdleData:
		p.sessionIdle = true
	}
	p.mu.Unlock()
	p.signalActivity()
}

func (p *sessionProgress) signalActivity() {
	select {
	case p.activitySignal <- struct{}{}:
	default:
	}
}

func (p *sessionProgress) completeSubagent(toolCallID, agentName string) {
	key := subagentKey(toolCallID, agentName)
	remaining := p.activeAgents[key] - 1
	if remaining <= 0 {
		delete(p.activeAgents, key)
		return
	}
	p.activeAgents[key] = remaining
}

func subagentKey(toolCallID, agentName string) string {
	return toolCallID + "\x00" + agentName
}

func (p *sessionProgress) activity() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastActivity
}

func (p *sessionProgress) tryCloseCallbackAdmission() (bool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	inactive := p.callbacks == 0 && len(p.activeTools) == 0 && len(p.activeAgents) == 0
	if !inactive {
		return p.sessionIdle, false
	}
	p.acceptCallbacks = false
	return p.sessionIdle, true
}

func (p *sessionProgress) beginRecovery() {
	p.mu.Lock()
	p.sessionIdle = false
	p.mu.Unlock()
}

func (p *sessionProgress) closeCallbackAdmission() {
	p.mu.Lock()
	p.acceptCallbacks = false
	p.mu.Unlock()
	p.signalActivity()
}

func (s *recordingSession) setKind(kind debuglog.SessionKind) {
	s.kindMu.Lock()
	s.kind = kind
	s.kindMu.Unlock()
}

func (s *recordingSession) currentKind() debuglog.SessionKind {
	s.kindMu.RLock()
	defer s.kindMu.RUnlock()
	return s.kind
}

type phasedResearch struct {
	research    qc.Research
	session     *recordingSession
	artifactIDs func() []string
	mu          sync.Mutex
	calls       int
}

func (r *phasedResearch) Run(ctx context.Context, config researchruntime.Config) (researchruntime.Result, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	if call == 1 {
		r.session.setKind(debuglog.SessionResearch)
	} else {
		r.session.setKind(debuglog.SessionRevision)
	}
	if r.artifactIDs != nil {
		config.InitialPrompt = researchEvidencePrompt(config.InitialPrompt, r.artifactIDs())
	}
	return r.research.Run(ctx, config)
}

type sessionEventSource interface {
	On(sdk.SessionEventHandler) func()
}

func (s *recordingSession) SendAndWait(ctx context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	kind := s.currentKind()
	if err := s.recordUserMessage(kind, options.Prompt); err != nil {
		return nil, err
	}
	ctx = debuglog.WithRecorder(ctx, s.recorder)
	lifecycleEvent := debuglog.Event{BlockAddress: s.address, Session: kind}
	started := time.Now()
	if err := debuglog.Lifecycle(ctx, "session.send", debuglog.StatusStarted, lifecycleEvent); err != nil {
		return nil, err
	}
	var eventErr error
	var eventErrMu sync.Mutex
	handleEvent := func(event sdk.SessionEvent) {
		var err error
		switch {
		case strings.HasPrefix(string(event.Type()), "session."):
			err = s.recordSessionEvent(&event)
		case event.Type() == sdk.SessionEventTypeAssistantMessage ||
			event.Type() == sdk.SessionEventTypeAssistantMessageDelta ||
			event.Type() == sdk.SessionEventTypeAssistantReasoning ||
			event.Type() == sdk.SessionEventTypeAssistantReasoningDelta ||
			event.Type() == sdk.SessionEventTypeAssistantUsage ||
			event.Type() == sdk.SessionEventTypeAssistantIdle:
			err = s.recordAssistantEvent(&event)
		case event.Type() == sdk.SessionEventTypeAssistantToolCallDelta ||
			event.Type() == sdk.SessionEventTypeToolSearchActivated ||
			event.Type() == sdk.SessionEventTypeToolUserRequested ||
			event.Type() == sdk.SessionEventTypeToolExecutionStart ||
			event.Type() == sdk.SessionEventTypeToolExecutionProgress ||
			event.Type() == sdk.SessionEventTypeToolExecutionPartialResult ||
			event.Type() == sdk.SessionEventTypeToolExecutionComplete:
			err = s.recordToolEvent(&event)
		default:
			return
		}
		if err != nil {
			eventErrMu.Lock()
			if eventErr == nil {
				eventErr = err
			}
			eventErrMu.Unlock()
		}
	}
	waitEvent := debuglog.Event{
		BlockAddress: s.address,
		Session:      kind,
		WaitFor:      string(sdk.SessionEventTypeSessionIdle),
	}
	s.resetLastEvent()
	waitStarted := time.Now()
	if err := debuglog.Lifecycle(ctx, "session.wait", debuglog.StatusStarted, waitEvent); err != nil {
		return nil, err
	}
	var recovery recoverableSession
	if recoverable, ok := s.Session.(recoverableSession); ok {
		recovery = recoverable
	}
	event, operationErr := s.sendWithStallWatchdog(ctx, options, recovery, kind, handleEvent)
	waitEvent.LastEvent = s.lastSDKEvent()
	waitLogErr := debuglog.CompleteLifecycle(ctx, "session.wait", waitStarted, operationErr, waitEvent)
	operationErr = errors.Join(operationErr, waitLogErr)
	eventErrMu.Lock()
	operationErr = errors.Join(operationErr, eventErr)
	eventErrMu.Unlock()
	if operationErr == nil && event != nil && s.markEvent(event.ID) {
		operationErr = s.recordAssistantEvent(event)
	}
	logErr := debuglog.CompleteLifecycle(ctx, "session.send", started, operationErr, lifecycleEvent)
	if operationErr != nil || logErr != nil {
		return nil, errors.Join(operationErr, logErr)
	}
	return event, nil
}

func (s *recordingSession) recordUserMessage(kind debuglog.SessionKind, prompt string) error {
	return s.recorder.Record(debuglog.Event{
		Kind: debuglog.EventMessage, BlockAddress: s.address, Session: kind,
		Role: debuglog.RoleUser, Content: prompt,
	})
}

func (s *recordingSession) sendWithStallWatchdog(
	ctx context.Context,
	options sdk.MessageOptions,
	recovery recoverableSession,
	kind debuglog.SessionKind,
	handleEvent func(sdk.SessionEvent),
) (*sdk.SessionEvent, error) {
	timer := time.NewTimer(s.effectiveStallTimeout())
	stopTimer(timer)
	defer stopTimer(timer)
	prompt := options
	recoveries := 0

	for {
		progress := newSessionProgress()
		stopObserving := s.subscribeToAttempt(progress, handleEvent)
		sendCtx, cancelSend := context.WithCancel(ctx)
		completed := make(chan sessionSendResult, 1)
		go func(message sdk.MessageOptions) {
			event, err := s.Session.SendAndWait(sendCtx, message)
			completed <- sessionSendResult{event: event, err: err}
		}(prompt)
		resetTimer(timer, s.effectiveStallTimeout(), s.lastActivity(progress))

		attemptComplete := false
		for !attemptComplete {
			select {
			case result := <-completed:
				cancelSend()
				if err := ctx.Err(); err != nil {
					settled := make(chan sessionSendResult, 1)
					settled <- result
					cancelErr := s.stopCanceledAttempt(
						ctx, cancelSend, settled, recovery, progress, stopObserving,
					)
					return nil, errors.Join(err, result.err, cancelErr)
				}
				stopObserving()
				return result.event, result.err
			case <-progress.activitySignal:
				resetTimer(timer, s.effectiveStallTimeout(), s.lastActivity(progress))
			case <-s.typedToolActivity.signal():
				resetTimer(timer, s.effectiveStallTimeout(), s.lastActivity(progress))
			case <-timer.C:
				// Prioritize a completed result over a stall: if timer.C and completed fire
				// simultaneously, Go's select is non-deterministic. A quick non-blocking check
				// here prevents a false "session stalled again after recovery" error.
				select {
				case result := <-completed:
					cancelSend()
					if err := ctx.Err(); err != nil {
						settled := make(chan sessionSendResult, 1)
						settled <- result
						cancelErr := s.stopCanceledAttempt(
							ctx, cancelSend, settled, recovery, progress, stopObserving,
						)
						return nil, errors.Join(err, result.err, cancelErr)
					}
					stopObserving()
					return result.event, result.err
				default:
				}
				remaining := s.effectiveStallTimeout() - time.Since(s.lastActivity(progress))
				if remaining > 0 {
					resetTimer(timer, remaining, time.Now())
					continue
				}
				stopTimer(timer)
				event := debuglog.Event{
					BlockAddress: s.address, Session: kind,
					WaitFor: "session activity", LastEvent: s.lastSDKEvent(),
				}
				if err := debuglog.Lifecycle(ctx, "session.stall_detected", debuglog.StatusCompleted, event); err != nil {
					cancelErr := s.stopCanceledAttempt(
						ctx, cancelSend, completed, recovery, progress, stopObserving,
					)
					return nil, errors.Join(err, cancelErr)
				}
				allowResume := recoveries < maxSessionStallRecoveries
				if err := s.recoverStalledAttempt(
					ctx, cancelSend, completed, recovery, progress, kind, allowResume, stopObserving,
				); err != nil {
					return nil, err
				}
				if !allowResume {
					return nil, fmt.Errorf("session stalled again after recovery")
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				s.typedToolActivity.endRecovery()
				recoveries++
				prompt = sdk.MessageOptions{Prompt: sessionStallContinuationPrompt}
				if err := s.recordUserMessage(kind, prompt.Prompt); err != nil {
					return nil, err
				}
				attemptComplete = true
			case <-ctx.Done():
				stopTimer(timer)
				cancelErr := s.stopCanceledAttempt(
					ctx, cancelSend, completed, recovery, progress, stopObserving,
				)
				return nil, errors.Join(ctx.Err(), cancelErr)
			}
		}
	}
}

func effectiveSessionStallTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	return defaultSessionStallTimeout
}

func (s *recordingSession) effectiveStallTimeout() time.Duration {
	return effectiveSessionStallTimeout(s.stallTimeout)
}

func (s *recordingSession) effectiveTerminationTimeout() time.Duration {
	if s.terminationTimeout > 0 {
		return s.terminationTimeout
	}
	return sessionRecoveryTimeout
}

func (s *recordingSession) lastActivity(progress *sessionProgress) time.Time {
	lastActivity := progress.activity()
	typedToolActivity, _ := s.typedToolActivity.state()
	if typedToolActivity.After(lastActivity) {
		return typedToolActivity
	}
	return lastActivity
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func resetTimer(timer *time.Timer, timeout time.Duration, lastActivity time.Time) {
	stopTimer(timer)
	timer.Reset(max(time.Duration(0), timeout-time.Since(lastActivity)))
}

func (s *recordingSession) subscribeToAttempt(
	progress *sessionProgress,
	handleEvent func(sdk.SessionEvent),
) func() {
	unsubscribe := func() {}
	source, ok := s.Session.(sessionEventSource)
	if ok {
		unsubscribe = source.On(func(event sdk.SessionEvent) {
			if !progress.beginCallback() {
				return
			}
			defer progress.finishCallback()
			s.setLastEvent(event.Type())
			if !s.markEvent(event.ID) {
				return
			}
			progress.track(event)
			handleEvent(event)
		})
	}
	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			progress.closeCallbackAdmission()
			unsubscribe()
		})
	}
}

func (s *recordingSession) recoverStalledAttempt(
	ctx context.Context,
	cancelSend context.CancelFunc,
	completed <-chan sessionSendResult,
	recovery recoverableSession,
	progress *sessionProgress,
	kind debuglog.SessionKind,
	allowResume bool,
	stopObserving func(),
) error {
	defer stopObserving()
	s.typedToolActivity.beginRecovery()
	recoveryCtx, cancelRecovery := context.WithTimeout(
		context.WithoutCancel(ctx),
		s.effectiveTerminationTimeout(),
	)
	defer cancelRecovery()
	event := debuglog.Event{
		BlockAddress: s.address, Session: kind,
		WaitFor: "session activity", LastEvent: s.lastSDKEvent(),
	}

	var abortErr error
	if recovery != nil {
		abortStarted := time.Now()
		if err := debuglog.Lifecycle(recoveryCtx, "session.abort", debuglog.StatusStarted, event); err != nil {
			cancelSend()
			_, stopErr := waitForSessionAttemptStop(
				recoveryCtx, completed, progress, s.typedToolActivity,
			)
			stopObserving()
			if stopErr != nil {
				s.markTainted()
				stopErr = fmt.Errorf("timed out waiting for stalled session work to stop: %w", stopErr)
			}
			return errors.Join(err, stopErr)
		}
		progress.beginRecovery()
		abortErr = runBoundedSessionOperation(recoveryCtx, recovery.Abort)
		logErr := debuglog.CompleteLifecycle(
			recoveryCtx, "session.abort", abortStarted, abortErr, event,
		)
		if errors.Is(abortErr, context.DeadlineExceeded) {
			cancelSend()
			stopObserving()
			s.markTainted()
			return errors.Join(
				fmt.Errorf("timed out aborting stalled session: %w", abortErr),
				logErr,
			)
		}
		if logErr != nil {
			cancelSend()
			_, stopErr := waitForSessionAttemptStop(
				recoveryCtx, completed, progress, s.typedToolActivity,
			)
			stopObserving()
			if stopErr != nil {
				s.markTainted()
				stopErr = fmt.Errorf("timed out waiting for stalled session work to stop: %w", stopErr)
			}
			return errors.Join(abortErr, logErr, stopErr)
		}
	}

	cancelSend()
	// SDK typed tools can outlive sendCtx, so Resume requires both barriers.
	sessionIdle, err := waitForSessionAttemptStop(
		recoveryCtx, completed, progress, s.typedToolActivity,
	)
	stopObserving()
	if err != nil {
		s.markTainted()
		return fmt.Errorf("timed out waiting for stalled session work to stop: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if abortErr != nil && !sessionIdle {
		s.markTainted()
		return fmt.Errorf("abort stalled session: %w", abortErr)
	}
	if !allowResume {
		if !sessionIdle {
			s.markTainted()
		}
		return nil
	}
	if recovery == nil {
		s.markTainted()
		return fmt.Errorf("stalled session does not support abort and resume")
	}
	if sessionIdle {
		return nil
	}

	resumeStarted := time.Now()
	if err = debuglog.Lifecycle(recoveryCtx, "session.resume", debuglog.StatusStarted, event); err != nil {
		s.markTainted()
		return err
	}
	resumeDeadline, _ := recoveryCtx.Deadline()
	resumeCtx, cancelResume := context.WithDeadline(ctx, resumeDeadline)
	resumeErr := runBoundedSessionOperation(resumeCtx, recovery.Resume)
	cancelResume()
	logErr := debuglog.CompleteLifecycle(
		recoveryCtx, "session.resume", resumeStarted, resumeErr, event,
	)
	if err = ctx.Err(); err != nil {
		s.markTainted()
		return errors.Join(err, abortErr, resumeErr, logErr)
	}
	if errors.Is(resumeErr, context.DeadlineExceeded) {
		s.markTainted()
		return errors.Join(
			fmt.Errorf("timed out resuming stalled session: %w", resumeErr),
			logErr,
		)
	}
	if resumeErr != nil || logErr != nil {
		s.markTainted()
		return fmt.Errorf("resume stalled session: %w", errors.Join(abortErr, resumeErr, logErr))
	}
	return nil
}

func runBoundedSessionOperation(
	ctx context.Context,
	operation func(context.Context) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	completed := make(chan error, 1)
	go func() {
		completed <- operation(ctx)
	}()
	select {
	case err := <-completed:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitForSessionAttemptStop(
	ctx context.Context,
	completed <-chan sessionSendResult,
	progress *sessionProgress,
	typedTools *typedToolActivity,
) (bool, error) {
	sendStopped := false
	completedSignal := completed
	for {
		_, typedToolsInactive := typedTools.state()
		if sendStopped && typedToolsInactive {
			if sessionIdle, closed := progress.tryCloseCallbackAdmission(); closed {
				return sessionIdle, nil
			}
		}
		select {
		case <-completedSignal:
			sendStopped = true
			completedSignal = nil
		case <-progress.activitySignal:
		case <-typedTools.signal():
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

func (s *recordingSession) stopCanceledAttempt(
	ctx context.Context,
	cancelSend context.CancelFunc,
	completed <-chan sessionSendResult,
	recovery recoverableSession,
	progress *sessionProgress,
	stopObserving func(),
) error {
	defer stopObserving()
	s.typedToolActivity.beginRecovery()
	terminationCtx, cancelTermination := context.WithTimeout(
		context.WithoutCancel(ctx),
		s.effectiveTerminationTimeout(),
	)
	defer cancelTermination()
	var abortErr error
	if recovery != nil {
		progress.beginRecovery()
		abortErr = runBoundedSessionOperation(terminationCtx, recovery.Abort)
	}
	cancelSend()
	_, stopErr := waitForSessionAttemptStop(
		terminationCtx, completed, progress, s.typedToolActivity,
	)
	stopObserving()
	if abortErr != nil {
		s.markTainted()
		if errors.Is(abortErr, context.DeadlineExceeded) {
			abortErr = fmt.Errorf("timed out aborting canceled session: %w", abortErr)
		} else {
			abortErr = fmt.Errorf("abort canceled session: %w", abortErr)
		}
	}
	if stopErr != nil {
		s.markTainted()
		stopErr = fmt.Errorf("timed out waiting for canceled session work to stop: %w", stopErr)
	}
	return errors.Join(abortErr, stopErr)
}

func (s *recordingSession) markTainted() {
	s.eventMu.Lock()
	s.tainted = true
	s.eventMu.Unlock()
}

func (s *recordingSession) isTainted() bool {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	return s.tainted
}

func (s *recordingSession) markEvent(id string) bool {
	if id == "" {
		return true
	}
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	if _, exists := s.seen[id]; exists {
		return false
	}
	s.seen[id] = struct{}{}
	return true
}

func (s *recordingSession) setLastEvent(eventType sdk.SessionEventType) {
	s.eventMu.Lock()
	s.lastEvent = string(eventType)
	s.eventMu.Unlock()
}

func (s *recordingSession) resetLastEvent() {
	s.eventMu.Lock()
	s.lastEvent = ""
	s.eventMu.Unlock()
}

func (s *recordingSession) lastSDKEvent() string {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	return s.lastEvent
}

func (s *recordingSession) recordSessionEvent(event *sdk.SessionEvent) error {
	if event == nil {
		return fmt.Errorf("session event is required")
	}
	content, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode session event: %w", err)
	}
	recorded := debuglog.Event{
		Timestamp:    event.Timestamp,
		Kind:         debuglog.EventLifecycle,
		Action:       string(event.Type()),
		Status:       debuglog.StatusCompleted,
		BlockAddress: s.address,
		Session:      s.currentKind(),
		SDKEvent:     content,
	}
	switch data := event.Data.(type) {
	case *sdk.SessionErrorData:
		recorded.Status = debuglog.StatusFailed
		recorded.Content = data.Message
		recorded.Error = data.Message
	case *sdk.SessionWarningData:
		recorded.Content = data.Message
	case *sdk.SessionTaskCompleteData:
		if data.Summary != nil {
			recorded.Content = *data.Summary
		}
		if data.Success != nil && !*data.Success {
			recorded.Status = debuglog.StatusFailed
		}
	}
	return s.recorder.Record(recorded)
}

func (s *recordingSession) recordToolEvent(event *sdk.SessionEvent) error {
	content, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode tool event: %w", err)
	}
	recorded := debuglog.Event{
		Timestamp: event.Timestamp, Kind: debuglog.EventTool, Action: string(event.Type()),
		BlockAddress: s.address, Session: s.currentKind(), SDKEvent: content,
	}
	s.normalizeToolEvent(&recorded, event)
	return s.recorder.Record(recorded)
}

func (s *recordingSession) normalizeToolEvent(recorded *debuglog.Event, event *sdk.SessionEvent) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if s.toolName == nil {
		s.toolName = make(map[string]string)
	}
	switch data := event.Data.(type) {
	case *sdk.AssistantToolCallDeltaData:
		recorded.ToolCallID = data.ToolCallID
		recorded.Content = data.InputDelta
		if data.ToolName != nil {
			recorded.ToolName = *data.ToolName
			s.toolName[data.ToolCallID] = *data.ToolName
		}
	case *sdk.ToolExecutionStartData:
		recorded.ToolCallID = data.ToolCallID
		recorded.ToolName = data.ToolName
		s.toolName[data.ToolCallID] = data.ToolName
		recorded.Arguments, _ = json.Marshal(data.Arguments)
	case *sdk.ToolExecutionProgressData:
		recorded.ToolCallID = data.ToolCallID
		recorded.ToolName = s.toolName[data.ToolCallID]
		recorded.Content = data.ProgressMessage
	case *sdk.ToolExecutionPartialResultData:
		recorded.ToolCallID = data.ToolCallID
		recorded.ToolName = s.toolName[data.ToolCallID]
		recorded.Content = data.PartialOutput
	case *sdk.ToolExecutionCompleteData:
		recorded.ToolCallID = data.ToolCallID
		recorded.ToolName = s.toolName[data.ToolCallID]
		recorded.Result, _ = json.Marshal(data)
		if data.Error != nil {
			recorded.Error = data.Error.Message
		}
	}
}

func (s *recordingSession) recordAssistantEvent(event *sdk.SessionEvent) error {
	if event == nil {
		return fmt.Errorf("assistant event is required")
	}
	content, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode assistant event: %w", err)
	}
	recorded := debuglog.Event{
		Timestamp: event.Timestamp, Action: string(event.Type()), SDKEvent: content,
		Kind: debuglog.EventMessage, BlockAddress: s.address, Session: s.currentKind(),
		Role: debuglog.RoleAssistant,
	}
	switch data := event.Data.(type) {
	case *sdk.AssistantMessageData:
		recorded.Content = data.Content
		recorded.MessageID = data.MessageID
	case *sdk.AssistantMessageDeltaData:
		recorded.Content = data.DeltaContent
		recorded.MessageID = data.MessageID
	case *sdk.AssistantReasoningData:
		recorded.Content = data.Content
		recorded.MessageID = data.ReasoningID
	case *sdk.AssistantReasoningDeltaData:
		recorded.Content = data.DeltaContent
		recorded.MessageID = data.ReasoningID
	case *sdk.AssistantUsageData:
		recorded.Usage = usageFromSDK(data)
	}
	return s.recorder.Record(recorded)
}

func usageFromSDK(data *sdk.AssistantUsageData) *debuglog.Usage {
	if data == nil {
		return nil
	}
	usage := &debuglog.Usage{Model: data.Model}
	if data.APICallID != nil {
		usage.APICallID = *data.APICallID
	}
	usage.InputTokens = int64Value(data.InputTokens)
	usage.OutputTokens = int64Value(data.OutputTokens)
	usage.ReasoningTokens = int64Value(data.ReasoningTokens)
	usage.CacheReadTokens = int64Value(data.CacheReadTokens)
	usage.CacheWriteTokens = int64Value(data.CacheWriteTokens)
	return usage
}

func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func (s *recordingSession) Close(ctx context.Context) error {
	if s.isTainted() {
		return nil
	}
	terminationCtx, cancelTermination := context.WithTimeout(
		context.WithoutCancel(ctx),
		s.effectiveTerminationTimeout(),
	)
	defer cancelTermination()
	terminationCtx = debuglog.WithRecorder(terminationCtx, s.recorder)
	event := debuglog.Event{BlockAddress: s.address, Session: s.currentKind()}
	started := time.Now()
	startLogErr := debuglog.Lifecycle(terminationCtx, "session.close", debuglog.StatusStarted, event)
	closeErr := runBoundedSessionOperation(terminationCtx, s.Session.Close)
	if errors.Is(closeErr, context.DeadlineExceeded) {
		s.markTainted()
		closeErr = fmt.Errorf("timed out closing session: %w", closeErr)
	}
	if startLogErr != nil {
		return errors.Join(closeErr, startLogErr)
	}
	return errors.Join(
		closeErr,
		debuglog.CompleteLifecycle(terminationCtx, "session.close", started, closeErr, event),
	)
}

func researchRetry(configValue *provider.Config, override provider.RetryOverride) (provider.RetryPolicy, error) {
	base := provider.DefaultRetryPolicy()
	var err error
	if configValue != nil {
		base, err = provider.MergeRetry(base, configValue.Retry)
		if err != nil {
			return provider.RetryPolicy{}, fmt.Errorf("provider retry: %w", err)
		}
	}
	return provider.MergeRetry(base, override)
}

func objectValue(values map[string]cty.Value) cty.Value {
	if len(values) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(values)
}

type officialSessionOpener struct {
	mu      sync.Mutex
	client  *sdk.Client
	factory *copilot.Factory
}

type stoppableCopilotClient interface {
	Stop() error
	ForceStop()
}

func newOfficialSessionOpener() *officialSessionOpener {
	return &officialSessionOpener{}
}

func (o *officialSessionOpener) Open(ctx context.Context, config copilot.SessionConfig) (Session, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.client == nil {
		o.client = sdk.NewClient(nil)
		if err := o.client.Start(ctx); err != nil {
			o.client = nil
			return nil, fmt.Errorf("start copilot client: %w", err)
		}
		o.factory = copilot.NewFactory(o.client, os.LookupEnv)
	}
	session, err := o.factory.Open(ctx, config)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func (o *officialSessionOpener) Close() error {
	o.mu.Lock()
	if o.client == nil {
		o.mu.Unlock()
		return nil
	}
	client := o.client
	o.client = nil
	o.factory = nil
	o.mu.Unlock()
	return stopCopilotClient(client, sessionRecoveryTimeout)
}

func stopCopilotClient(client stoppableCopilotClient, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	stopErr := runBoundedSessionOperation(ctx, func(context.Context) error {
		return client.Stop()
	})
	if !errors.Is(stopErr, context.DeadlineExceeded) {
		return stopErr
	}
	go client.ForceStop()
	return fmt.Errorf("timed out stopping copilot client: %w", stopErr)
}
