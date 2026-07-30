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
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/spf13/cobra"
	"github.com/zclconf/go-cty/cty"
)

const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitUsage   = 2
)

type ApplyOptions struct {
	Parallelism int
	Debug       bool
}

type ApplyResult struct {
	Outputs  map[string]cty.Value
	Warnings []error
}

type Runtime interface {
	Plan(context.Context, string, []golden.CliFlagAssignedVariables) (*plan.Plan, error)
	Apply(context.Context, *plan.Plan, ApplyOptions) (ApplyResult, error)
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
	var outputPath string
	var variables []string
	var variableFiles []string
	command := &cobra.Command{
		Use:   "plan DIRECTORY",
		Short: "Create and save an immutable research plan",
		Args:  usageArgs(cobra.ExactArgs(1)),
		PreRunE: func(*cobra.Command, []string) error {
			if strings.TrimSpace(outputPath) == "" {
				return &usageError{err: fmt.Errorf("--out is required")}
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			if runtime == nil {
				return fmt.Errorf("runtime is required")
			}
			planned, err := runtime.Plan(command.Context(), args[0], goldenVariables(variables, variableFiles))
			if err != nil {
				return fmt.Errorf("plan directory %q: %w", args[0], err)
			}
			warning, err := plan.Save(outputPath, planned)
			if err != nil {
				return fmt.Errorf("save plan %q: %w", outputPath, err)
			}
			if warning != "" {
				writeWarning(command.ErrOrStderr(), warning)
			}
			display, err := plan.Display(planned)
			if err != nil {
				return fmt.Errorf("display plan: %w", err)
			}
			_, err = io.WriteString(command.OutOrStdout(), display)
			return err
		},
	}
	command.Flags().StringVar(&outputPath, "out", "", "saved plan path")
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
			planned, err := loadOrPlan(
				ctx, runtime, args[0], goldenVariables(variables, variableFiles),
			)
			if err != nil {
				return err
			}
			if debug {
				writeWarning(command.ErrOrStderr(), "debug output contains sensitive prompts, transcripts, and tool data")
			}
			result, err := runtime.Apply(ctx, planned, ApplyOptions{Parallelism: parallelism, Debug: debug})
			for _, warning := range result.Warnings {
				if warning != nil {
					writeWarning(command.ErrOrStderr(), warning.Error())
				}
			}
			if err != nil {
				return err
			}
			return writeOutputs(command.OutOrStdout(), result.Outputs)
		},
	}
	command.Flags().IntVar(&parallelism, "parallelism", parallelism, "maximum concurrent research blocks")
	command.Flags().DurationVar(&timeout, "timeout", 0, "overall apply timeout")
	command.Flags().BoolVar(&debug, "debug", false, "persist sensitive debug events")
	command.Flags().StringArrayVar(&variables, "var", nil, "set a Golden input variable for directory apply (name=value)")
	command.Flags().StringArrayVar(&variableFiles, "var-file", nil, "load Golden input variables for directory apply")
	return command
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
	ctx context.Context,
	runtime Runtime,
	target string,
	variables []golden.CliFlagAssignedVariables,
) (*plan.Plan, error) {
	info, err := os.Stat(target)
	if err == nil && info.IsDir() {
		planned, planErr := runtime.Plan(ctx, target, variables)
		if planErr != nil {
			return nil, fmt.Errorf("plan directory %q: %w", target, planErr)
		}
		return planned, nil
	}
	if err == nil {
		planned, loadErr := plan.Load(target)
		if loadErr != nil {
			return nil, fmt.Errorf("load plan %q: %w", target, loadErr)
		}
		return planned, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect apply target %q: %w", target, err)
	}
	planned, loadErr := plan.Load(target)
	if loadErr != nil {
		return nil, fmt.Errorf("load plan %q: %w", target, loadErr)
	}
	return planned, nil
}

func writeWarning(writer io.Writer, message string) {
	_, _ = fmt.Fprintf(writer, "warning: %s\n", message)
}

func writeOutputs(writer io.Writer, outputs map[string]cty.Value) error {
	display, err := plan.DisplayValues(outputs)
	if err != nil {
		return fmt.Errorf("display apply outputs: %w", err)
	}
	_, err = io.WriteString(writer, display)
	return err
}
