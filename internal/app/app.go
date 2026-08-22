package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/MaksimSurmach/OCIHood/internal/config"
	"github.com/MaksimSurmach/OCIHood/internal/provisioner"
)

// Request identifies one configured account run.
type Request struct {
	ConfigPath string
	Account    string
}

// Result describes a completed read-only bootstrap run.
type Result struct {
	Account string
	Region  string
}

// Error identifies the application phase that failed.
type Error struct {
	Phase string
	Err   error
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %v", e.Phase, e.Err) }
func (e *Error) Unwrap() error { return e.Err }

// Load resolves one account's effective configuration.
type Load func(context.Context, string, string) (config.Effective, error)

// Authenticate constructs isolated provider dependencies for one account.
type Authenticate func(context.Context, config.Effective) (provisioner.Bootstrapper, error)

// Runner coordinates configuration, authentication, and provisioning.
type Runner struct {
	logger       *slog.Logger
	load         Load
	authenticate Authenticate
}

// NewRunner creates the application runner.
func NewRunner(logger *slog.Logger, load Load, authenticate Authenticate) *Runner {
	return &Runner{logger: logger, load: load, authenticate: authenticate}
}

// Run performs one authenticated, read-only bootstrap run.
func (r *Runner) Run(ctx context.Context, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, &Error{Phase: "start", Err: err}
	}
	r.logger.InfoContext(ctx, "provisioning run started", "account", request.Account)

	effective, err := r.load(ctx, request.ConfigPath, request.Account)
	if err != nil {
		r.logger.ErrorContext(ctx, "provisioning run failed", "account", request.Account, "phase", "config")
		return Result{}, &Error{Phase: "config", Err: err}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, &Error{Phase: "config", Err: err}
	}
	provider, err := r.authenticate(ctx, effective)
	if err != nil {
		r.logger.ErrorContext(ctx, "provisioning run failed", "account", request.Account, "phase", "authentication")
		return Result{}, &Error{Phase: "authentication", Err: err}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, &Error{Phase: "authentication", Err: err}
	}

	run := provisioner.Run{
		Account: request.Account, Settings: effective, Logger: r.logger, Bootstrapper: provider,
	}
	if err := run.Execute(ctx); err != nil {
		r.logger.ErrorContext(ctx, "provisioning run failed", "account", request.Account, "phase", "bootstrap")
		return Result{}, &Error{Phase: "bootstrap", Err: err}
	}

	r.logger.InfoContext(ctx, "provisioning run completed", "account", request.Account, "region", effective.Region)
	return Result{Account: request.Account, Region: effective.Region}, nil
}
