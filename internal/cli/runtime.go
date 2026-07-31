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

func (*Engine) Plan(
	ctx context.Context,
	directory string,
	variables []golden.CliFlagAssignedVariables,
) (*plan.Plan, error) {
	planned, err := modulespec.PlanDirectoryWithOptions(directory, modulespec.PlanOptions{
		Context: ctx, Variables: variables,
	})
	if err != nil {
		return nil, err
	}
	return planned.Saved, nil
}

func (e *Engine) Apply(
	ctx context.Context,
	planned *plan.Plan,
	options ApplyOptions,
) (ApplyResult, error) {
	if planned == nil {
		return ApplyResult{}, fmt.Errorf("saved plan is required")
	}
	activeRun, err := run.NewManager(planned.Directory()).Create()
	if err != nil {
		return ApplyResult{}, err
	}
	recorder, err := debuglog.NewRecorder(activeRun.Directory(), options.Debug)
	if err != nil {
		return ApplyResult{}, err
	}
	sessions := e.options.Sessions
	if sessions == nil {
		sessions = newOfficialSessionOpener()
	}
	factory := &runtimeFactory{
		results: make(map[string]cty.Value), run: activeRun, sessions: sessions, recorder: recorder,
		state: new(runtimeState),
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
	if closeErr := recorder.Close(); closeErr != nil {
		warnings = append(warnings, fmt.Errorf("close debug log: %w", closeErr))
	}
	return ApplyResult{Outputs: outputs, Warnings: warnings}, applyErr
}

type runtimeFactory struct {
	mu       sync.Mutex
	results  map[string]cty.Value
	run      *run.Run
	sessions SessionOpener
	recorder *debuglog.Recorder
	state    *runtimeState
	prefix   string
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
	planned, err := modulespec.DecodeResearchPlan(node.Config)
	if err != nil {
		return nil, err
	}
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
	executionAddress := f.executionAddress(node.Address)
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
	tools, terminalType, err := f.buildTools(
		ctx, executionAddress, debuglog.SessionResearch, workspace, planned, terminal,
	)
	if err != nil {
		return nil, err
	}
	resolved := researchspec.ResolvedTools{}
	terminateName := ""
	if planned.TerminateTool != nil {
		terminateName = toolName(planned.TerminateTool.Address)
		resolved.Terminate = &researchspec.ToolPolicyRef{
			Address: planned.TerminateTool.Address, OutputType: terminalType,
		}
		resolved.TerminateSDKName = terminateName
	}
	if planned.Config.QC != nil {
		resolved.QCVerdictSDKName = "r42_qc_verdict"
	}
	if err = planned.Config.ValidateResolved(resolved); err != nil {
		return nil, err
	}
	session, err := f.sessions.Open(ctx, copilot.SessionConfig{
		Provider: planned.Provider, Retry: retry, Model: planned.Config.Model,
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
	if planned.TerminateTool != nil {
		terminalRecorder = terminal
	}
	runner := researchruntime.NewRunner(&recordingSession{
		Session: session, recorder: f.recorder, address: executionAddress, kind: debuglog.SessionResearch,
	}, terminalRecorder)
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
				f.closeAfterSetupFailure(ctx, session)
				return nil, err
			}
		}
		effective, effectiveErr := planned.Config.EffectiveQC(providerRetry)
		if effectiveErr != nil {
			f.closeAfterSetupFailure(ctx, session)
			return nil, effectiveErr
		}
		qcToolsPlan := planned
		qcToolsPlan.Tools = planned.QCTools
		qcToolsPlan.TerminateTool = nil
		qcTools, _, toolsErr := f.buildTools(
			ctx, executionAddress, debuglog.SessionQC, workspace, qcToolsPlan,
			researchruntime.NewTerminalRecorder(),
		)
		if toolsErr != nil {
			f.closeAfterSetupFailure(ctx, session)
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
		qcSession, err = f.sessions.Open(ctx, copilot.SessionConfig{
			Provider: selectedProvider, Retry: effective.Retry, Model: effective.Model,
			ReasoningEffort: qcReasoning, SystemPrompt: qcSystemPrompt, WorkingDirectory: workspace,
			Tools: qcTools, AvailableTools: slices.Clone(effective.AllowedTools), ExcludedTools: slices.Clone(effective.DisallowedTools),
			SkillDirectories: slices.Clone(effective.SkillDirectories), Skills: slices.Clone(effective.Skills),
			DisabledSkills: slices.Clone(effective.DisabledSkills),
		})
		if err != nil {
			f.closeAfterSetupFailure(ctx, session)
			return nil, err
		}
		qcRunner = qc.NewRunner(runner, &recordingSession{
			Session: qcSession, recorder: f.recorder, address: executionAddress, kind: debuglog.SessionQC,
		}, verdicts)
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
		BaseBlock: new(golden.BaseBlock), ctx: ctx, address: node.Address, session: session,
		runner: runner, qcRunner: qcRunner, qcConfig: qcConfig, qcSession: qcSession, config: researchruntime.Config{
			InitialPrompt: initialPrompt, MaxProtocolAttempts: planned.Config.MaxProtocolAttempts,
			Timeout: planned.Config.Timeout, Workspace: workspace, Artifacts: planned.Config.Artifacts,
			TerminateToolName: terminateName,
		}, publish: f.publish, cancel: blockCancel,
	}
	keepBlockContext = true
	return block, nil
}

func (f *runtimeFactory) closeAfterSetupFailure(ctx context.Context, session Session) {
	if err := session.Close(context.WithoutCancel(ctx)); err != nil {
		f.state.addWarning(fmt.Errorf("close research session after setup failure: %w", err))
	}
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
	executionAddress := f.executionAddress(node.Address)
	childFactory := &runtimeFactory{
		results: make(map[string]cty.Value), run: f.run, sessions: f.sessions,
		recorder: f.recorder, state: f.state, prefix: executionAddress,
	}
	return &moduleApplyBlock{
		BaseBlock: new(golden.BaseBlock), ctx: ctx, address: node.Address,
		planned: node.Module.Plan, timeout: node.Module.Timeout, scope: childScope,
		factory: childFactory, publish: f.publish,
	}, nil
}

func (f *runtimeFactory) executionAddress(address string) string {
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
	planned modulespec.ResearchPlan,
	terminal *researchruntime.TerminalRecorder,
) ([]sdk.Tool, cty.Type, error) {
	definitions := slices.Clone(planned.Tools)
	if planned.TerminateTool != nil && !slices.ContainsFunc(definitions, func(tool modulespec.PlannedTool) bool {
		return tool.Address == planned.TerminateTool.Address
	}) {
		definitions = append(definitions, *planned.TerminateTool)
	}
	result := make([]sdk.Tool, 0, len(definitions))
	terminalType := cty.NilType
	for _, definition := range definitions {
		if definition.Kind == string(config.AddressKindExternal) {
			tool, outputType, err := f.buildExternalTool(
				ctx, blockAddress, sessionKind, workspace, definition, planned, terminal,
			)
			if err != nil {
				return nil, cty.NilType, err
			}
			if planned.TerminateTool != nil && definition.Address == planned.TerminateTool.Address {
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
		isTerminal := planned.TerminateTool != nil && definition.Address == planned.TerminateTool.Address
		if isTerminal {
			terminalType = analysis.OutputType
		}
		result = append(result, sdk.Tool{
			Name: toolName(definition.Address), Description: definition.Description, Parameters: parameters,
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				arguments, marshalErr := json.Marshal(invocation.Arguments)
				if marshalErr != nil {
					return f.rejectedArguments(
						blockAddress, sessionKind, definition.Address, nil, marshalErr, isTerminal, terminal,
					)
				}
				value, decodeErr := ctyjson.Unmarshal(arguments, input.Type())
				if decodeErr != nil {
					return f.rejectedArguments(
						blockAddress, sessionKind, definition.Address, arguments, decodeErr, isTerminal, terminal,
					)
				}
				value, validateErr := input.Apply(value)
				if validateErr != nil {
					return f.rejectedArguments(
						blockAddress, sessionKind, definition.Address, arguments, validateErr, isTerminal, terminal,
					)
				}
				arguments, marshalErr = ctyjson.Marshal(value, input.Type())
				if marshalErr != nil {
					return sdk.ToolResult{}, fmt.Errorf("encode validated %s arguments: %w", definition.Address, marshalErr)
				}
				response, invokeErr := program.Invoke(ctx, arguments)
				if invokeErr != nil {
					var stdout string
					var stderr string
					var invocationErr *gotool.InvocationError
					if errors.As(invokeErr, &invocationErr) {
						stdout = invocationErr.Stdout()
						stderr = invocationErr.Stderr()
					}
					if recordErr := f.recordToolFailure(
						blockAddress, sessionKind, definition.Address, arguments, invokeErr, stdout, stderr,
					); recordErr != nil {
						return sdk.ToolResult{}, errors.Join(invokeErr, recordErr)
					}
					return sdk.ToolResult{}, invokeErr
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
					ToolName: toolName(definition.Address), Arguments: arguments, Result: encoded, Stderr: response.Stderr,
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

func (f *runtimeFactory) buildExternalTool(
	ctx context.Context,
	blockAddress string,
	sessionKind debuglog.SessionKind,
	workspace string,
	definition modulespec.PlannedTool,
	planned modulespec.ResearchPlan,
	terminal *researchruntime.TerminalRecorder,
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
	isTerminal := planned.TerminateTool != nil && definition.Address == planned.TerminateTool.Address
	runner := externaltool.NewRunner()
	tool := sdk.Tool{
		Name: toolName(definition.Address), Description: definition.Description, Parameters: parameters,
		Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
			arguments, marshalErr := json.Marshal(invocation.Arguments)
			if marshalErr != nil {
				return f.rejectedArguments(
					blockAddress, sessionKind, definition.Address, nil, marshalErr, isTerminal, terminal,
				)
			}
			value, decodeErr := ctyjson.Unmarshal(arguments, input.Type())
			if decodeErr != nil {
				return f.rejectedArguments(
					blockAddress, sessionKind, definition.Address, arguments, decodeErr, isTerminal, terminal,
				)
			}
			value, validateErr := input.Apply(value)
			if validateErr != nil {
				return f.rejectedArguments(
					blockAddress, sessionKind, definition.Address, arguments, validateErr, isTerminal, terminal,
				)
			}
			result, runErr := runner.Run(ctx, externaltool.Config{
				Program: definition.Program, Workspace: workspace, WorkingDir: definition.WorkingDir,
				Input: input, Output: output,
			}, value)
			if runErr != nil {
				var stdout string
				var stderr string
				var executionErr *externaltool.ExecutionError
				if errors.As(runErr, &executionErr) {
					stdout = executionErr.Stdout()
					stderr = executionErr.Stderr()
				}
				if recordErr := f.recordToolFailure(
					blockAddress, sessionKind, definition.Address, arguments, runErr, stdout, stderr,
				); recordErr != nil {
					return sdk.ToolResult{}, errors.Join(runErr, recordErr)
				}
				return sdk.ToolResult{}, runErr
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
				ToolName: toolName(definition.Address), Arguments: arguments, Result: encoded, Stderr: result.Stderr,
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

func (f *runtimeFactory) recordToolFailure(
	blockAddress string,
	sessionKind debuglog.SessionKind,
	toolAddress string,
	arguments []byte,
	cause error,
	stdout string,
	stderr string,
) error {
	failure, _ := json.Marshal(map[string]string{"error": cause.Error()})
	return f.recorder.Record(debuglog.Event{
		Kind: debuglog.EventTool, BlockAddress: blockAddress, Session: sessionKind,
		ToolName: toolName(toolAddress), Arguments: arguments, Result: failure,
		Stdout: stdout, Stderr: stderr,
	})
}

func (f *runtimeFactory) rejectedArguments(
	blockAddress string,
	sessionKind debuglog.SessionKind,
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
		ToolName: toolName(toolAddress), Arguments: arguments, Result: encoded,
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

func toolName(address string) string {
	return strings.ReplaceAll(address, ".", "_")
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
	kind, name, ok := strings.Cut(address, ".")
	if !ok {
		return
	}
	namespace, exists := contextValues[kind]
	if !exists || !namespace.Type().IsObjectType() {
		return
	}
	values := namespace.AsValueMap()
	if existing, ok := values[name]; ok && existing.Type().IsObjectType() && value.Type().IsObjectType() {
		merged := existing.AsValueMap()
		maps.Copy(merged, value.AsValueMap())
		value = cty.ObjectVal(merged)
	}
	values[name] = value
	contextValues[kind] = cty.ObjectVal(values)
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

func (*researchApplyBlock) Type() string            { return "" }
func (*researchApplyBlock) BlockType() string       { return "research" }
func (*researchApplyBlock) AddressLength() int      { return 2 }
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
}

func (s *recordingSession) SendAndWait(ctx context.Context, options sdk.MessageOptions) (*sdk.SessionEvent, error) {
	if err := s.recorder.Record(debuglog.Event{
		Kind: debuglog.EventMessage, BlockAddress: s.address, Session: s.kind,
		Role: debuglog.RoleUser, Content: options.Prompt,
	}); err != nil {
		return nil, err
	}
	event, err := s.Session.SendAndWait(ctx, options)
	if err != nil {
		return nil, err
	}
	content, marshalErr := json.Marshal(event)
	if marshalErr != nil {
		return nil, fmt.Errorf("encode assistant event: %w", marshalErr)
	}
	if err = s.recorder.Record(debuglog.Event{
		Kind: debuglog.EventMessage, BlockAddress: s.address, Session: s.kind,
		Role: debuglog.RoleAssistant, Content: string(content),
	}); err != nil {
		return nil, err
	}
	return event, nil
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
	return officialSession{session}, nil
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

type officialSession struct {
	*copilot.Session
}

func (s officialSession) Close(ctx context.Context) error {
	return s.Session.Close(ctx)
}
