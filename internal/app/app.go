package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/capacity"
	"github.com/MaksimSurmach/OCIHood/internal/config"
	"github.com/MaksimSurmach/OCIHood/internal/discovery"
	"github.com/MaksimSurmach/OCIHood/internal/launch"
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
	Account            string
	Region             string
	TargetID           string
	Decision           reconcile.DecisionKind
	Attempt            *reconcile.Attempt
	Capacity           capacity.Kind
	AvailabilityDomain string
	InstanceID         string
	InstanceState      string
	PublicIP           string
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

// WatchCapacity waits for advisory capacity after a create-safe reconciliation decision.
type WatchCapacity func(context.Context, provisioner.Bootstrapper, config.Effective, discovery.Result) (capacity.Result, error)

// LaunchInstance executes the mutating and lifecycle portion after reconciliation.
type LaunchInstance func(context.Context, provisioner.Bootstrapper, config.Effective, discovery.Result, reconcile.Decision, capacity.Result, string) (launch.Instance, error)

// Runner coordinates configuration, authentication, and provisioning.
type Runner struct {
	logger         *slog.Logger
	load           Load
	authenticate   Authenticate
	discover       Discover
	watchCapacity  WatchCapacity
	launchInstance LaunchInstance
	random         io.Reader
	now            func() time.Time
}

// SetLaunch enables the production launch phase while preserving lightweight read-only runners in tests.
func (r *Runner) SetLaunch(launchInstance LaunchInstance) { r.launchInstance = launchInstance }

// NewRunner creates the application runner.
func NewRunner(logger *slog.Logger, load Load, authenticate Authenticate, discover Discover, watchCapacity ...WatchCapacity) *Runner {
	runner := &Runner{logger: logger, load: load, authenticate: authenticate, discover: discover, random: rand.Reader, now: time.Now}
	if len(watchCapacity) > 0 {
		runner.watchCapacity = watchCapacity[0]
	}
	return runner
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
	decision, err := reconcileAndPersist(effective, discovered, r.random, r.now().UTC())
	if err != nil {
		r.logger.ErrorContext(ctx, "provisioning run failed", "account", request.Account, "phase", "state")
		return Result{}, &Error{Phase: "state", Err: err}
	}
	var capacityResult capacity.Result
	var instance launch.Instance
	createSafe := decision.Kind == reconcile.DecisionCreate || decision.Kind == reconcile.DecisionNewAttemptSafe || decision.Kind == reconcile.DecisionRetrySameAttempt
	var sshKey []byte
	if r.launchInstance != nil && createSafe {
		sshKey, err = os.ReadFile(effective.SSHPublicKeyPath)
		if err != nil {
			return Result{}, &Error{Phase: "launch", Err: fmt.Errorf("read SSH public key: %w", err)}
		}
	}
	for {
		if r.watchCapacity != nil && createSafe {
			capacityResult, err = r.watchCapacity(ctx, provider, effective, discovered)
			if err != nil {
				return Result{}, &Error{Phase: "capacity", Err: err}
			}
		}
		if r.launchInstance == nil || (!createSafe && decision.InstanceID == "") {
			break
		}
		instance, err = r.launchInstance(ctx, provider, effective, discovered, decision, capacityResult, string(sshKey))
		var launchErr *launch.Error
		if errors.As(err, &launchErr) && launchErr.Kind == launch.OutOfCapacity && createSafe {
			continue
		}
		if err != nil {
			return Result{}, &Error{Phase: "launch", Err: err}
		}
		break
	}

	r.logger.InfoContext(ctx, "provisioning run completed", "account", request.Account, "region", effective.Region)
	return Result{Account: request.Account, Region: effective.Region, TargetID: discovered.TargetID, Decision: decision.Kind, Attempt: decision.Attempt, Capacity: capacityResult.Kind, AvailabilityDomain: capacityResult.AvailabilityDomain, InstanceID: instance.ID, InstanceState: instance.State, PublicIP: instance.PublicIP}, nil
}

const attemptValidity = 24 * time.Hour

func reconcileAndPersist(effective config.Effective, discovered discovery.Result, random io.Reader, now time.Time) (reconcile.Decision, error) {
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
		State: local, Instances: discovered.Instances, Now: now,
	})
	value.LastResult = decision.Reason
	value.UpdatedAt = now
	switch decision.Kind {
	case reconcile.DecisionAlreadySatisfied:
		value.Lifecycle, value.InstanceID = state.Running, decision.InstanceID
	case reconcile.DecisionResumeReconcile, reconcile.DecisionRetrySameAttempt:
		value.Lifecycle = state.Provisioning
	case reconcile.DecisionConflict:
		value.Lifecycle, value.LastError = state.Failed, decision.Reason
	case reconcile.DecisionCreate, reconcile.DecisionNewAttemptSafe:
		attempt, err := reconcile.NewAttempt(random, now, attemptValidity)
		if err != nil {
			return reconcile.Decision{}, err
		}
		decision.Attempt = &attempt
		value.Lifecycle, value.AttemptID, value.RetryToken = state.Provisioning, attempt.ID, attempt.RetryToken
		value.AttemptValidTo, value.LastAttempt = attempt.ValidUntil, now
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
