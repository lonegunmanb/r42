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

	"github.com/Azure/golden"
	sdk "github.com/github/copilot-sdk/go"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/lonegunmanb/hclfuncs"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	"github.com/lonegunmanb/r42/internal/config"
	"github.com/lonegunmanb/r42/internal/copilot"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/executor"
	module "github.com/lonegunmanb/r42/internal/module"
	modulespec "github.com/lonegunmanb/r42/internal/module/spec"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/provider"
	"github.com/lonegunmanb/r42/internal/qc"
	researchruntime "github.com/lonegunmanb/r42/internal/research/runtime"
	researchspec "github.com/lonegunmanb/r42/internal/research/spec"
	"github.com/lonegunmanb/r42/internal/run"
	corespec "github.com/lonegunmanb/r42/internal/spec"
	externaltool "github.com/lonegunmanb/r42/internal/tool/external"
	"github.com/lonegunmanb/r42/internal/tool/gotool"
	toolspec "github.com/lonegunmanb/r42/internal/tool/spec"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

const researchSystemProtocol = "You are executing an unattended r42 research DAG block. Follow the configured tool and completion protocol."

type Session interface {
	SendAndWait(context.Context, sdk.MessageOptions) (*sdk.SessionEvent, error)
	Close(context.Context) error
}

type SessionOpener interface {
	Open(context.Context, copilot.SessionConfig) (Session, error)
}

type RuntimeOptions struct {
	Sessions SessionOpener
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

func (*Engine) InitModules(
	ctx context.Context,
	directory string,
	modulesDirectory string,
	upgrade bool,
) error {
	return module.Init(ctx, directory, module.InitOptions{
		ModulesDirectory: modulesDirectory,
		Upgrade:          upgrade,
	})
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
	mu               sync.Mutex
	results          map[string]cty.Value
	run              *run.Run
	sessions         SessionOpener
	recorder         *debuglog.Recorder
	state            *runtimeState
	prefix           string
	tools            map[string]plan.ToolSpec
	directory        string
	contextValues    map[string]cty.Value
	localExpressions map[string]string
}

type runtimeState struct {
	compilerMu sync.Mutex
	compiler   *gotool.Compiler
	warningsMu sync.Mutex
	warnings   []error
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
		planned, err = planned.Resolve(configValue)
		if err != nil {
			return nil, err
		}
		if err = modulespec.ValidateResearchToolIDs(configValue, f.tools); err != nil {
			return nil, err
		}
	}
	return f.newResearchBlock(ctx, node.Address, planned, f.publish)
}

func (f *runtimeFactory) newResearchBlock(
	ctx context.Context,
	address string,
	planned modulespec.ResearchPlan,
	publish func(string, cty.Value),
) (golden.ApplyBlock, error) {
	var blockCancel context.CancelFunc
	if planned.Config.Timeout != nil {
		ctx, blockCancel = context.WithTimeout(ctx, *planned.Config.Timeout)
	}
	keepBlockContext := false
	defer func() {
		if !keepBlockContext && blockCancel != nil {
			blockCancel()
		}
	}()
	executionAddress := f.CanonicalAddress(address)
	workspace, err := f.run.Workspace(executionAddress)
	if err != nil {
		return nil, err
	}
	retry, err := researchRetry(planned.Provider, planned.Config.Retry)
	if err != nil {
		return nil, err
	}
	reasoning := ""
	if planned.Config.ReasoningEffort != nil {
		reasoning = *planned.Config.ReasoningEffort
	}
	systemPrompt := researchSystemProtocol + "\n\n" + planned.Config.SystemPrompt
	if recordErr := f.recorder.Record(debuglog.Event{
		Kind: debuglog.EventMessage, BlockAddress: executionAddress, Session: debuglog.SessionResearch,
		Role: debuglog.RoleSystem, Content: systemPrompt,
	}); recordErr != nil {
		return nil, recordErr
	}
	terminal := researchruntime.NewTerminalRecorder()
	researchQuota := newToolCallQuota(planned.Config.Policy.TypedToolCallQuota)
	tools, terminalType, err := f.buildTools(
		ctx, executionAddress, debuglog.SessionResearch, workspace,
		planned.Config.Policy.ToolIDs, planned.Config.TerminateToolID, terminal, researchQuota,
	)
	if err != nil {
		return nil, err
	}
	resolved := researchspec.ResolvedTools{}
	terminateName := ""
	if planned.Config.TerminateToolID != nil {
		terminateName = *planned.Config.TerminateToolID
		definition := f.tools[terminateName]
		resolved.Terminate = &researchspec.ToolPolicyRef{
			ID: definition.ID, Address: definition.Address, OutputType: terminalType,
		}
		resolved.TerminateSDKName = terminateName
	}
	if planned.Config.QC != nil {
		resolved.QCVerdictSDKName = "r42_qc_verdict"
	}
	if err = planned.Config.ValidateResolved(resolved); err != nil {
		return nil, err
	}
	session, err := f.openSession(ctx, executionAddress, debuglog.SessionResearch, copilot.SessionConfig{
		Provider: planned.Provider, Retry: retry, Model: planned.Config.Model, Profile: planned.Config.ProfileName(),
		ReasoningEffort: reasoning, SystemPrompt: systemPrompt, WorkingDirectory: workspace,
		AvailableTools:   slices.Clone(planned.Config.Policy.AllowedTools),
		ExcludedTools:    slices.Clone(planned.Config.Policy.DisallowedTools),
		SkillDirectories: slices.Clone(planned.Config.Policy.SkillDirectories),
		Skills:           slices.Clone(planned.Config.Policy.Skills), DisabledSkills: slices.Clone(planned.Config.Policy.DisabledSkills),
		Tools: tools,
	})
	if err != nil {
		return nil, err
	}
	initialPrompt := "Begin the configured research task."
	if planned.Config.Prompt != nil {
		initialPrompt = *planned.Config.Prompt
	}
	var terminalRecorder *researchruntime.TerminalRecorder
	if planned.Config.TerminateToolID != nil {
		terminalRecorder = terminal
	}
	recordedSession := &recordingSession{
		Session: session, recorder: f.recorder, address: executionAddress, kind: debuglog.SessionResearch,
	}
	runner := researchruntime.NewRunner(recordedSession, terminalRecorder)
	var qcRunner *qc.Runner
	var qcConfig qc.Config
	var qcSession Session
	if planned.Config.QC != nil {
		providerRetry := provider.DefaultRetryPolicy()
		selectedProvider := planned.Provider
		if planned.QCProvider != nil {
			selectedProvider = planned.QCProvider
		}
		if selectedProvider != nil {
			providerRetry, err = provider.MergeRetry(providerRetry, selectedProvider.Retry)
			if err != nil {
				f.closeAfterSetupFailure(ctx, recordedSession)
				return nil, err
			}
		}
		effective, effectiveErr := planned.Config.EffectiveQC(providerRetry)
		if effectiveErr != nil {
			f.closeAfterSetupFailure(ctx, recordedSession)
			return nil, effectiveErr
		}
		qcTools, _, toolsErr := f.buildTools(
			ctx, executionAddress, debuglog.SessionQC, workspace, effective.ToolIDs, nil,
			researchruntime.NewTerminalRecorder(), newToolCallQuota(effective.TypedToolCallQuota),
		)
		if toolsErr != nil {
			f.closeAfterSetupFailure(ctx, recordedSession)
			return nil, toolsErr
		}
		verdicts := qc.NewVerdictRecorder()
		qcTools = append(qcTools, qcVerdictTool(executionAddress, f.recorder, verdicts))
		qcReasoning := ""
		if effective.ReasoningEffort != nil {
			qcReasoning = *effective.ReasoningEffort
		}
		qcSystemPrompt := "You are the independent QC session for an unattended r42 research block. Call r42_qc_verdict with pass or repair issues."
		_ = f.recorder.Record(debuglog.Event{
			Kind: debuglog.EventMessage, BlockAddress: executionAddress, Session: debuglog.SessionQC,
			Role: debuglog.RoleSystem, Content: qcSystemPrompt,
		})
		qcSession, err = f.openSession(ctx, executionAddress, debuglog.SessionQC, copilot.SessionConfig{
			Provider: selectedProvider, Retry: effective.Retry, Model: effective.Model, Profile: effective.Profile,
			ReasoningEffort: qcReasoning, SystemPrompt: qcSystemPrompt, WorkingDirectory: workspace,
			Tools: qcTools, AvailableTools: slices.Clone(effective.AllowedTools), ExcludedTools: slices.Clone(effective.DisallowedTools),
			SkillDirectories: slices.Clone(effective.SkillDirectories), Skills: slices.Clone(effective.Skills),
			DisabledSkills: slices.Clone(effective.DisabledSkills),
		})
		if err != nil {
			f.closeAfterSetupFailure(ctx, recordedSession)
			return nil, err
		}
		qcSession = &recordingSession{
			Session: qcSession, recorder: f.recorder, address: executionAddress, kind: debuglog.SessionQC,
		}
		qcRunner = qc.NewRunner(&phasedResearch{research: runner, session: recordedSession}, qcSession, verdicts)
		qcConfig = qc.Config{
			Task:     qc.Task{SystemPrompt: planned.Config.SystemPrompt, Prompt: planned.Config.Prompt},
			Criteria: effective.Criteria, Artifacts: planned.Config.Artifacts,
			Research: researchruntime.Config{
				InitialPrompt: initialPrompt, TerminateToolName: terminateName,
				MaxProtocolAttempts: planned.Config.MaxProtocolAttempts, Timeout: planned.Config.Timeout,
				Workspace: workspace, Artifacts: planned.Config.Artifacts,
			},
			MaxRounds: effective.MaxRounds, MaxProtocolAttempts: researchspec.DefaultMaxProtocolAttempts,
			VerdictToolName: "r42_qc_verdict",
		}
	}
	block := &researchApplyBlock{
		BaseBlock: new(golden.BaseBlock), ctx: ctx, address: address, session: recordedSession,
		runner: runner, qcRunner: qcRunner, qcConfig: qcConfig, qcSession: qcSession, config: researchruntime.Config{
			InitialPrompt: initialPrompt, MaxProtocolAttempts: planned.Config.MaxProtocolAttempts,
			Timeout: planned.Config.Timeout, Workspace: workspace, Artifacts: planned.Config.Artifacts,
			TerminateToolName: terminateName,
		}, publish: publish, cancel: blockCancel,
	}
	keepBlockContext = true
	return block, nil
}

func (f *runtimeFactory) closeAfterSetupFailure(ctx context.Context, session Session) {
	if err := session.Close(context.WithoutCancel(ctx)); err != nil {
		f.state.addWarning(fmt.Errorf("close research session after setup failure: %w", err))
	}
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
		localExpressions: node.Module.Plan.LocalExpressions(),
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
			Name: definition.ID, Description: definition.Description, Parameters: parameters,
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
		Name: definition.ID, Description: definition.Description, Parameters: parameters,
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

type toolCallQuota struct {
	mu     sync.Mutex
	limits map[string]int
	used   map[string]int
}

func newToolCallQuota(limits map[string]int) *toolCallQuota {
	return &toolCallQuota{limits: maps.Clone(limits), used: make(map[string]int)}
}

func (q *toolCallQuota) reserve(toolID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	limit, configured := q.limits[toolID]
	if !configured {
		return nil
	}
	if q.used[toolID] >= limit {
		return fmt.Errorf("typed tool %q call quota exhausted (limit %d)", toolID, limit)
	}
	q.used[toolID]++
	return nil
}

func (q *toolCallQuota) rollback(toolID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, configured := q.limits[toolID]; configured && q.used[toolID] > 0 {
		q.used[toolID]--
	}
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
		Description: "Pass the candidate or return concrete repair issues",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pass":   map[string]any{"type": "boolean"},
				"issues": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			},
			"required": []string{"pass"},
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
			if recordErr := verdicts.Record(verdict); recordErr != nil {
				return sdk.ToolResult{}, recordErr
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

type researchApplyBlock struct {
	*golden.BaseBlock
	ctx       context.Context
	address   string
	session   Session
	runner    *researchruntime.Runner
	qcSession Session
	qcRunner  *qc.Runner
	qcConfig  qc.Config
	config    researchruntime.Config
	publish   func(string, cty.Value)
	cancel    context.CancelFunc
}

func (*researchApplyBlock) Type() string            { return "static" }
func (*researchApplyBlock) BlockType() string       { return "research" }
func (*researchApplyBlock) AddressLength() int      { return 3 }
func (*researchApplyBlock) CanExecutePrePlan() bool { return false }
func (b *researchApplyBlock) Address() string       { return b.address }

func (b *researchApplyBlock) Apply() error {
	var result researchruntime.Result
	var err error
	if b.qcRunner == nil {
		result, err = b.runner.Run(b.ctx, b.config)
	} else {
		var reviewed qc.Result
		reviewed, err = b.qcRunner.Run(b.ctx, b.qcConfig)
		result = reviewed.Candidate
	}
	if err != nil {
		return err
	}
	value := map[string]cty.Value{"artifact": researchspec.ArtifactsValue(b.config.Artifacts, result.Artifacts)}
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
	var qcErr error
	if b.qcSession != nil {
		qcErr = b.qcSession.Close(ctx)
	}
	return errors.Join(qcErr, b.session.Close(ctx))
}

type recordingSession struct {
	Session
	recorder *debuglog.Recorder
	address  string
	kind     debuglog.SessionKind
	eventMu  sync.Mutex
	kindMu   sync.RWMutex
	seen     map[string]struct{}
	toolName map[string]string
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
	research qc.Research
	session  *recordingSession
	mu       sync.Mutex
	calls    int
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
	return r.research.Run(ctx, config)
}

type sessionEventSource interface {
	On(sdk.SessionEventHandler) func()
}

func (s *recordingSession) SendAndWait(ctx context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	kind := s.currentKind()
	if err := s.recorder.Record(debuglog.Event{
		Kind: debuglog.EventMessage, BlockAddress: s.address, Session: kind,
		Role: debuglog.RoleUser, Content: options.Prompt,
	}); err != nil {
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
	unsubscribe := func() {}
	if source, ok := s.Session.(sessionEventSource); ok {
		unsubscribe = source.On(func(event sdk.SessionEvent) {
			if !s.markEvent(event.ID) {
				return
			}
			var err error
			switch event.Type() {
			case sdk.SessionEventTypeAssistantMessage,
				sdk.SessionEventTypeAssistantMessageDelta,
				sdk.SessionEventTypeAssistantReasoning,
				sdk.SessionEventTypeAssistantReasoningDelta,
				sdk.SessionEventTypeAssistantUsage:
				err = s.recordAssistantEvent(&event)
			case sdk.SessionEventTypeAssistantToolCallDelta,
				sdk.SessionEventTypeToolSearchActivated,
				sdk.SessionEventTypeToolUserRequested,
				sdk.SessionEventTypeToolExecutionStart,
				sdk.SessionEventTypeToolExecutionProgress,
				sdk.SessionEventTypeToolExecutionPartialResult,
				sdk.SessionEventTypeToolExecutionComplete:
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
		})
	}
	event, operationErr := s.Session.SendAndWait(ctx, options)
	unsubscribe()
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
	ctx = debuglog.WithRecorder(ctx, s.recorder)
	event := debuglog.Event{BlockAddress: s.address, Session: s.currentKind()}
	started := time.Now()
	startLogErr := debuglog.Lifecycle(ctx, "session.close", debuglog.StatusStarted, event)
	closeErr := s.Session.Close(ctx)
	if startLogErr != nil {
		return errors.Join(closeErr, startLogErr)
	}
	return errors.Join(closeErr, debuglog.CompleteLifecycle(ctx, "session.close", started, closeErr, event))
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
	defer o.mu.Unlock()
	if o.client == nil {
		return nil
	}
	err := o.client.Stop()
	o.client = nil
	o.factory = nil
	return err
}
