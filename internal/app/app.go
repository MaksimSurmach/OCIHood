package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/config"
	"github.com/MaksimSurmach/OCIHood/internal/discovery"
	"github.com/MaksimSurmach/OCIHood/internal/provisioner"
	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
	"github.com/MaksimSurmach/OCIHood/internal/state"
)

// Request identifies one configured account run.
type Request struct {
	ConfigPath string
	Account    string
}

// Result describes the completed read-only discovery and reconciliation decision.
type Result struct {
	Account  string
	Region   string
	TargetID string
	Decision reconcile.DecisionKind
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

// Discover performs the bounded read-only resource discovery used before reconciliation.
type Discover func(context.Context, provisioner.Bootstrapper, config.Effective) (discovery.Result, error)

// Runner coordinates configuration, authentication, and provisioning.
type Runner struct {
	logger       *slog.Logger
	load         Load
	authenticate Authenticate
	discover     Discover
}

// NewRunner creates the application runner.
func NewRunner(logger *slog.Logger, load Load, authenticate Authenticate, discover Discover) *Runner {
	return &Runner{logger: logger, load: load, authenticate: authenticate, discover: discover}
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
	if err := ctx.Err(); err != nil {
		return Result{}, &Error{Phase: "bootstrap", Err: err}
	}
	discovered, err := r.discover(ctx, provider, effective)
	if err != nil {
		r.logger.ErrorContext(ctx, "provisioning run failed", "account", request.Account, "phase", "discovery")
		return Result{}, &Error{Phase: "discovery", Err: err}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, &Error{Phase: "discovery", Err: err}
	}
	decision, err := reconcileAndPersist(effective, discovered)
	if err != nil {
		r.logger.ErrorContext(ctx, "provisioning run failed", "account", request.Account, "phase", "state")
		return Result{}, &Error{Phase: "state", Err: err}
	}

	r.logger.InfoContext(ctx, "provisioning run completed", "account", request.Account, "region", effective.Region)
	return Result{Account: request.Account, Region: effective.Region, TargetID: discovered.TargetID, Decision: decision.Kind}, nil
}

func reconcileAndPersist(effective config.Effective, discovered discovery.Result) (reconcile.Decision, error) {
	store := state.New(effective.StateDir)
	locked, err := store.TryLock(effective.Account, discovered.TargetID)
	if err != nil {
		return reconcile.Decision{}, err
	}
	defer func() { _ = locked.Close() }()

	value, err := store.Load(effective.Account, discovered.TargetID)
	var local *reconcile.State
	if errors.Is(err, state.ErrNotFound) {
		value = state.State{Account: effective.Account, TargetID: discovered.TargetID, Lifecycle: state.Discovered}
	} else if err != nil {
		return reconcile.Decision{}, err
	} else {
		local = value.ReconcileState()
	}
	decision := reconcile.Decide(reconcile.Input{
		TargetID: discovered.TargetID, Account: effective.Account,
		State: local, Instances: discovered.Instances, Now: time.Now().UTC(),
	})
	value.LastResult = decision.Reason
	value.UpdatedAt = time.Now().UTC()
	switch decision.Kind {
	case reconcile.DecisionAlreadySatisfied:
		value.Lifecycle, value.InstanceID = state.Running, decision.InstanceID
	case reconcile.DecisionResumeReconcile, reconcile.DecisionRetrySameAttempt:
		value.Lifecycle = state.Provisioning
	case reconcile.DecisionConflict:
		value.Lifecycle, value.LastError = state.Failed, decision.Reason
	default:
		value.Lifecycle = state.Discovered
	}
	if err := locked.Save(value); err != nil {
		return reconcile.Decision{}, err
	}
	if decision.Kind == reconcile.DecisionConflict {
		return decision, errors.New(decision.Reason)
	}
	return decision, nil
}
