package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Azure/golden"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/spf13/cobra"
	"github.com/zclconf/go-cty/cty"
)

const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitUsage   = 2
)

type Runtime interface {
	Config(string, executor.ResearchConfigOptions) (*executor.ResearchConfig, error)
	ConfigFromPlan(*plan.Plan, executor.ResearchConfigOptions) (*executor.ResearchConfig, error)
}

type usageError struct {
	err error
}

func (e *usageError) Error() string { return e.err.Error() }

func (e *usageError) Unwrap() error { return e.err }

func ExitCode(err error) int {
	if err == nil {
		return ExitSuccess
	}
	var usage *usageError
	if errors.As(err, &usage) {
		return ExitUsage
	}
	return ExitFailure
}

func NewCommand(runtime Runtime) *cobra.Command {
	root := &cobra.Command{
		Use:           "r42",
		Short:         "Plan and apply research DAGs",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &usageError{err: err}
	})
	root.AddCommand(newPlanCommand(runtime), newApplyCommand(runtime))
	return root
}

func newPlanCommand(runtime Runtime) *cobra.Command {
	directory := "."
	var outputPath string
	var debug bool
	var variables []string
	var variableFiles []string
	command := &cobra.Command{
		Use:   "plan",
		Short: "Create an immutable research plan",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(command *cobra.Command, _ []string) error {
			if runtime == nil {
				return fmt.Errorf("runtime is required")
			}
			ctx := command.Context()
			debugState := &debugRun{enabled: debug}
			if debug {
				writeWarning(command.ErrOrStderr(), "debug output contains sensitive configuration, prompts, transcripts, and tool data")
				ctx = withDebugRun(ctx, debugState)
			}
			options := executor.ResearchConfigOptions{
				Context: ctx, Variables: goldenVariables(variables, variableFiles), RunDirectory: ".",
			}
			config, err := planConfig(
				runtime,
				directory,
				options,
			)
			if err != nil {
				return errors.Join(fmt.Errorf("plan directory %q: %w", directory, err), closeCommandDebug(command, debugState))
			}
			planned := config.Plan().SavedPlan()
			ctx, _, _, err = debugState.ensure(ctx, options.RunDirectory)
			if err != nil {
				return errors.Join(err, closeCommandDebug(command, debugState))
			}
			if err = writePlan(ctx, command.OutOrStdout(), planned, directory); err != nil {
				return errors.Join(err, closeCommandDebug(command, debugState))
			}
			if strings.TrimSpace(outputPath) != "" {
				saveStarted := time.Now()
				if err = debuglog.Lifecycle(ctx, "plan.save", debuglog.StatusStarted, debuglog.Event{Path: outputPath}); err != nil {
					return errors.Join(err, closeCommandDebug(command, debugState))
				}
				warning, saveErr := plan.Save(outputPath, planned)
				saveLogErr := debuglog.CompleteLifecycle(ctx, "plan.save", saveStarted, saveErr, debuglog.Event{Path: outputPath})
				if saveErr != nil {
					return errors.Join(fmt.Errorf("save plan %q: %w", outputPath, saveErr), saveLogErr, closeCommandDebug(command, debugState))
				}
				if saveLogErr != nil {
					return errors.Join(saveLogErr, closeCommandDebug(command, debugState))
				}
				if warning != "" {
					writeWarning(command.ErrOrStderr(), warning)
				}
			}
			return closeCommandDebug(command, debugState)
		},
	}
	command.Flags().StringVarP(&directory, "directory", "d", directory, "directory containing .r42.hcl files")
	command.Flags().StringVar(&outputPath, "out", "", "saved plan path")
	command.Flags().BoolVar(&debug, "debug", false, "persist sensitive planning debug events")
	command.Flags().StringArrayVar(&variables, "var", nil, "set a Golden input variable (name=value)")
	command.Flags().StringArrayVar(&variableFiles, "var-file", nil, "load Golden input variables from a file")
	return command
}

func newApplyCommand(runtime Runtime) *cobra.Command {
	parallelism := 10
	var timeout time.Duration
	var debug bool
	var variables []string
	var variableFiles []string
	command := &cobra.Command{
		Use:   "apply PLAN_OR_DIRECTORY",
		Short: "Apply a saved plan or plan and apply a directory",
		Args:  usageArgs(cobra.ExactArgs(1)),
		PreRunE: func(command *cobra.Command, _ []string) error {
			if parallelism <= 0 {
				return &usageError{err: fmt.Errorf("parallelism must be positive")}
			}
			if command.Flags().Changed("timeout") && timeout <= 0 {
				return &usageError{err: fmt.Errorf("timeout must be positive")}
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			if runtime == nil {
				return fmt.Errorf("runtime is required")
			}
			ctx := command.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			debugState := &debugRun{enabled: debug}
			if debug {
				ctx = withDebugRun(ctx, debugState)
				writeWarning(command.ErrOrStderr(), "debug output contains sensitive configuration, prompts, transcripts, and tool data")
			}
			options := executor.ResearchConfigOptions{
				Context: ctx, Variables: goldenVariables(variables, variableFiles),
				RunDirectory: ".", Parallelism: parallelism, Debug: debug,
			}
			config, err := loadOrPlan(
				runtime, args[0], options,
			)
			if err != nil {
				return errors.Join(err, closeCommandDebug(command, debugState))
			}
			planned := config.Plan()
			if planned == nil {
				return errors.Join(fmt.Errorf("research config has no plan"), closeCommandDebug(command, debugState))
			}
			ctx, _, _, err = debugState.ensure(ctx, options.RunDirectory)
			if err != nil {
				return errors.Join(err, closeCommandDebug(command, debugState))
			}
			if err = writePlan(ctx, command.OutOrStdout(), planned.SavedPlan(), args[0]); err != nil {
				return errors.Join(err, closeCommandDebug(command, debugState))
			}
			err = planned.Apply()
			closeErr := closeCommandDebug(command, debugState)
			for _, warning := range config.Warnings() {
				if warning != nil {
					writeWarning(command.ErrOrStderr(), warning.Error())
				}
			}
			if err != nil {
				return errors.Join(err, closeErr)
			}
			if closeErr != nil {
				return closeErr
			}
			return writeOutputs(command.OutOrStdout(), config.Outputs())
		},
	}
	command.Flags().IntVar(&parallelism, "parallelism", parallelism, "maximum concurrent research blocks")
	command.Flags().DurationVar(&timeout, "timeout", time.Hour, "overall apply timeout")
	command.Flags().BoolVar(&debug, "debug", false, "persist sensitive debug events")
	command.Flags().StringArrayVar(&variables, "var", nil, "set a Golden input variable for directory apply (name=value)")
	command.Flags().StringArrayVar(&variableFiles, "var-file", nil, "load Golden input variables for directory apply")
	return command
}

func closeCommandDebug(command *cobra.Command, state *debugRun) error {
	path := state.path()
	err := state.close()
	if path != "" {
		writeWarning(command.ErrOrStderr(), "debug log: "+path)
	}
	if err != nil {
		return fmt.Errorf("close debug log: %w", err)
	}
	return nil
}

func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(command *cobra.Command, args []string) error {
		if err := validate(command, args); err != nil {
			return &usageError{err: err}
		}
		return nil
	}
}

func goldenVariables(values, files []string) []golden.CliFlagAssignedVariables {
	result := make([]golden.CliFlagAssignedVariables, 0, len(values)+len(files))
	for _, value := range values {
		name, raw, found := strings.Cut(value, "=")
		if !found {
			name, raw = value, ""
		}
		result = append(result, golden.NewCliFlagAssignedVariable(name, raw))
	}
	for _, path := range files {
		result = append(result, golden.NewCliFlagAssignedVariableFile(path))
	}
	return result
}

func loadOrPlan(
	runtime Runtime,
	target string,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("inspect apply target %q: %w", target, err)
	}
	if info.IsDir() {
		config, err := planConfig(runtime, target, options)
		if err != nil {
			return nil, fmt.Errorf("plan directory %q: %w", target, err)
		}
		return config, nil
	}
	planned, err := plan.Load(target)
	if err != nil {
		return nil, fmt.Errorf("load plan %q: %w", target, err)
	}
	config, err := runtime.ConfigFromPlan(planned, options)
	if err != nil {
		return nil, fmt.Errorf("configure plan %q: %w", target, err)
	}
	return config, nil
}

func planConfig(
	runtime Runtime,
	directory string,
	options executor.ResearchConfigOptions,
) (*executor.ResearchConfig, error) {
	config, err := runtime.Config(directory, options)
	if err != nil {
		return nil, err
	}
	ctx := config.Context()
	started := time.Now()
	if err = debuglog.Lifecycle(ctx, "plan", debuglog.StatusStarted, debuglog.Event{Path: directory}); err != nil {
		return nil, err
	}
	if config.Plan() == nil {
		_, err = executor.RunResearchPlan(config)
	}
	logErr := recordLifecycleCompletion(ctx, "plan", started, err, debuglog.Event{Path: directory})
	if err != nil {
		return nil, errors.Join(err, logErr)
	}
	return config, logErr
}

func writeWarning(writer io.Writer, message string) {
	_, _ = fmt.Fprintf(writer, "warning: %s\n", message)
}

func writePlan(ctx context.Context, writer io.Writer, planned *plan.Plan, path string) error {
	started := time.Now()
	if err := debuglog.Lifecycle(ctx, "plan.display", debuglog.StatusStarted, debuglog.Event{Path: path}); err != nil {
		return err
	}
	display, err := plan.Display(planned)
	logErr := debuglog.CompleteLifecycle(ctx, "plan.display", started, err, debuglog.Event{
		Path: path, Bytes: len(display),
	})
	if err != nil {
		return errors.Join(fmt.Errorf("display plan: %w", err), logErr)
	}
	if logErr != nil {
		return logErr
	}
	if _, err = io.WriteString(writer, display); err != nil {
		return fmt.Errorf("write plan: %w", err)
	}
	return nil
}

func writeOutputs(writer io.Writer, outputs map[string]cty.Value) error {
	display, err := plan.DisplayValues(outputs)
	if err != nil {
		return fmt.Errorf("display apply outputs: %w", err)
	}
	_, err = io.WriteString(writer, display)
	return err
}
