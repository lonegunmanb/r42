package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/lonegunmanb/r42/internal/cli"
)

func main() {
	os.Exit(mainExitCode())
}

func mainExitCode() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return run(ctx, os.Args[1:], os.Stdout, os.Stderr, cli.NewRuntime())
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, runtime cli.Runtime) int {
	command := cli.NewCommand(runtime)
	command.SetArgs(args)
	command.SetOut(stdout)
	command.SetErr(stderr)
	err := command.ExecuteContext(ctx)
	if err == nil {
		return cli.ExitSuccess
	}
	writeError(stderr, err)
	if errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled) {
		return 130
	}
	return cli.ExitCode(err)
}

func writeError(writer io.Writer, err error) {
	var diagnostics hcl.Diagnostics
	if !errors.As(err, &diagnostics) {
		_, _ = fmt.Fprintf(writer, "error: %v\n", err)
		return
	}
	context := strings.TrimSuffix(err.Error(), diagnostics.Error())
	for _, diagnostic := range diagnostics {
		_, _ = fmt.Fprintf(writer, "error: %s%s\n", context, diagnostic)
	}
}
