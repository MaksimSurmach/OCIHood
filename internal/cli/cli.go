// Package cli defines OCIHood's command-line interface.
package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/app"
	"github.com/MaksimSurmach/OCIHood/internal/config"
	"github.com/MaksimSurmach/OCIHood/internal/state"
	"github.com/spf13/cobra"
)

// Runner performs one OCIHood provisioning run.
type Runner interface {
	Run(context.Context, app.Request) (app.Result, error)
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
	var configPath string
	var account string
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
	root.PersistentFlags().StringVar(&configPath, "config", "", "configuration file (default: OS user config directory/ocihood/config.yaml)")

	start := &cobra.Command{
		Use:   "start",
		Short: "Start one provisioning run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := runner.Run(cmd.Context(), app.Request{ConfigPath: configPath, Account: account})
			if err != nil {
				return fmt.Errorf("start provisioning run: %w", err)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "account %s bootstrap complete (region %s)\n", result.Account, result.Region); err != nil {
				return fmt.Errorf("write command result: %w", err)
			}
			return nil
		},
	}
	start.Flags().StringVar(&account, "account", "", "account name")
	_ = start.MarkFlagRequired("account")
	root.AddCommand(start)
	root.AddCommand(newConfigCommand(&configPath))
	root.AddCommand(newStatusCommand(&configPath))

	return root
}

func newStatusCommand(configPath *string) *cobra.Command {
	var account string
	command := &cobra.Command{
		Use: "status", Short: "Read persisted provisioning status without contacting OCI", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.Path(*configPath)
			if err != nil {
				return err
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			effective, err := cfg.Resolve(account)
			if err != nil {
				return err
			}
			value, err := state.New(effective.StateDir).LoadAccount(account)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "account: %s\ntarget_id: %s\nstatus: %s\ninstance_id: %s\npublic_ip: %s\nlast_result: %s\nlast_error: %s\nupdated_at: %s\n", value.Account, value.TargetID, value.Lifecycle, value.InstanceID, value.PublicIP, value.LastResult, value.LastError, value.UpdatedAt.Format(time.RFC3339)); err != nil {
				return fmt.Errorf("write status: %w", err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&account, "account", "", "account name")
	_ = command.MarkFlagRequired("account")
	return command
}

func newConfigCommand(configPath *string) *cobra.Command {
	command := &cobra.Command{Use: "config", Short: "Inspect and validate configuration", Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{
		Use: "validate", Short: "Validate configuration without contacting OCI", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			path, err := config.Path(*configPath)
			if err != nil {
				return err
			}
			if _, err := config.Load(path); err != nil {
				return err
			}
			return nil
		},
	})
	var account string
	show := &cobra.Command{
		Use: "show", Short: "Show effective non-secret account configuration", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.Path(*configPath)
			if err != nil {
				return err
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			effective, err := cfg.Resolve(account)
			if err != nil {
				return err
			}
			out, err := config.MarshalEffective(effective)
			if err != nil {
				return err
			}
			if _, err := cmd.OutOrStdout().Write(out); err != nil {
				return fmt.Errorf("write effective config: %w", err)
			}
			return nil
		},
	}
	show.Flags().StringVar(&account, "account", "", "account name")
	_ = show.MarkFlagRequired("account")
	command.AddCommand(show)
	return command
}
