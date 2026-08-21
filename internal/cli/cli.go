// Package cli defines OCIHood's command-line interface.
package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// Runner performs one OCIHood provisioning run.
type Runner interface {
	Run(context.Context) (string, error)
}

// Execute runs the CLI and returns its process exit code.
func Execute(ctx context.Context, args []string, runner Runner, stdout, stderr io.Writer) int {
	cmd := newRootCommand(runner)
	cmd.SetContext(ctx)
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	if err := cmd.Execute(); err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func newRootCommand(runner Runner) *cobra.Command {
	root := &cobra.Command{
		Use:           "ocihood",
		Short:         "Provision Oracle Cloud Infrastructure resources",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	root.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start one provisioning run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := runner.Run(cmd.Context())
			if err != nil {
				return fmt.Errorf("start provisioning run: %w", err)
			}
			if result != "" {
				if _, err := io.WriteString(cmd.OutOrStdout(), result); err != nil {
					return fmt.Errorf("write command result: %w", err)
				}
			}
			return nil
		},
	})

	return root
}
