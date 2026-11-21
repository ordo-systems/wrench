// Command wrench runs the Wrench server, its backups and the development mocks.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

const (
	exitError = 1
	exitUsage = 2
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := execute(ctx, newRootCmd(), os.Args[1:], os.Stderr)
	stop()
	os.Exit(code)
}

// execute runs the command named by args and maps its error to the process exit code: usage mistakes print the
// command's usage and exit with exitUsage, every other failure exits with exitError.
func execute(ctx context.Context, root *cobra.Command, args []string, stderr io.Writer) int {
	root.SetArgs(args)
	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return 0
	}
	if ue := (usageError{}); errors.As(err, &ue) {
		_, _ = fmt.Fprintf(stderr, "wrench: %v\n\n%s", err, cmd.UsageString())
		return exitUsage
	}
	_, _ = fmt.Fprintf(stderr, "wrench: %v\n", err)
	return exitError
}

func newRootCmd() *cobra.Command {
	root := newGroupCmd("wrench", "Wrench server, backup and development mocks")
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.AddCommand(
		newRunCmd(),
		newBackupCmd(),
		newRestoreCmd(),
		newMockCmd(),
		newHealthcheckCmd(),
		newVersionCmd(),
	)
	return root
}

// configFlag registers --config; resolveConfig applies its WRENCH_CONFIG fallback.
func configFlag(c *cobra.Command, path *string) {
	c.Flags().StringVar(path, "config", "config.yaml", "application policy (default from WRENCH_CONFIG)")
}

func resolveConfig(c *cobra.Command, path string) string {
	if !c.Flags().Changed("config") {
		if v, ok := os.LookupEnv("WRENCH_CONFIG"); ok && v != "" {
			return v
		}
	}
	return path
}

// usageError marks invocation mistakes, which exit with exitUsage after printing the command's usage.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

// newGroupCmd is a command that only holds subcommands; running it without a known subcommand is a usage error.
func newGroupCmd(use, short string) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usagef("unknown command %q", args[0])
			}
			return nil
		},
		RunE:              func(*cobra.Command, []string) error { return usagef("a subcommand is required") },
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
}

func noArgs(c *cobra.Command, args []string) error { return wrapArgs(cobra.NoArgs)(c, args) }

func exactArgs(n int) cobra.PositionalArgs { return wrapArgs(cobra.ExactArgs(n)) }

func maxArgs(n int) cobra.PositionalArgs { return wrapArgs(cobra.MaximumNArgs(n)) }

func wrapArgs(check cobra.PositionalArgs) cobra.PositionalArgs {
	return func(c *cobra.Command, args []string) error {
		if err := check(c, args); err != nil {
			return usageError{err}
		}
		return nil
	}
}
