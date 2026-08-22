// Package launch turns a reconciled target into one running instance.
package launch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
)

type Kind string

const (
	Accepted      Kind = "accepted"
	OutOfCapacity Kind = "out_of_capacity"
	Transient     Kind = "transient"
	Ambiguous     Kind = "ambiguous"
	Fatal         Kind = "fatal"
	LimitExceeded Kind = "limit_exceeded"
	Canceled      Kind = "canceled"
)

type Request struct {
	TargetID, Account, CompartmentID, AvailabilityDomain string
	Shape, ImageID, SubnetID, SSHPublicKey               string
	OCPUs, MemoryGB, BootVolumeGB                        int
	PublicIP                                             bool
	Attempt                                              reconcile.Attempt
}

type Instance struct{ ID, State, PublicIP string }
type Result struct {
	Kind       Kind
	Instance   Instance
	RetryAfter time.Duration
}

type Provider interface {
	Launch(context.Context, Request) (Result, error)
	Reconcile(context.Context, Request) (Instance, bool, error)
	Get(context.Context, string, string) (Instance, error)
}

type Store interface {
	Accepted(instanceID string) error
	Finished(instance Instance) error
	Failed(error) error
}

type Sleeper interface {
	Sleep(context.Context, time.Duration) error
}

type Input struct {
	Request                            Request
	ExistingInstanceID                 string
	RequestTimeout, RetryMin, RetryMax time.Duration
}

type Orchestrator struct {
	Provider Provider
	Store    Store
	Sleeper  Sleeper
}

func (o Orchestrator) Run(ctx context.Context, in Input) (Instance, error) {
	if o.Provider == nil || o.Store == nil || o.Sleeper == nil || in.RequestTimeout <= 0 || in.RetryMin <= 0 || in.RetryMax < in.RetryMin {
		return Instance{}, errors.New("complete launch dependencies and retry configuration are required")
	}
	instanceID := in.ExistingInstanceID
	for retry := 0; instanceID == ""; retry++ {
		requestCtx, cancel := context.WithTimeout(ctx, in.RequestTimeout)
		result, err := o.Provider.Launch(requestCtx, in.Request)
		requestErr := requestCtx.Err()
		cancel()
		if ctx.Err() != nil {
			return Instance{}, ctx.Err()
		}
		if requestErr == context.DeadlineExceeded && result.Kind == "" {
			result.Kind = Ambiguous
		}
		switch result.Kind {
		case Accepted:
			instanceID = result.Instance.ID
			if instanceID == "" {
				return Instance{}, errors.New("launch accepted without instance id")
			}
			if err := o.Store.Accepted(instanceID); err != nil {
				return Instance{}, fmt.Errorf("persist accepted instance: %w", err)
			}
		case OutOfCapacity:
			return Instance{}, &Error{Kind: OutOfCapacity, Err: err}
		case LimitExceeded:
			requestCtx, cancel := context.WithTimeout(ctx, in.RequestTimeout)
			instance, found, reconcileErr := o.Provider.Reconcile(requestCtx, in.Request)
			cancel()
			if ctx.Err() != nil {
				return Instance{}, ctx.Err()
			}
			if reconcileErr != nil {
				return Instance{}, fmt.Errorf("reconcile service limit: %w", reconcileErr)
			}
			if found {
				instanceID = instance.ID
				if err := o.Store.Accepted(instanceID); err != nil {
					return Instance{}, fmt.Errorf("persist reconciled instance: %w", err)
				}
				break
			}
			_ = o.Store.Failed(err)
			return Instance{}, &Error{Kind: LimitExceeded, Err: err}
		case Fatal:
			_ = o.Store.Failed(err)
			return Instance{}, &Error{Kind: result.Kind, Err: err}
		case Canceled:
			return Instance{}, &Error{Kind: Canceled, Err: err}
		case Transient, Ambiguous, "":
			delay := backoff(in.RetryMin, in.RetryMax, retry)
			if delay == in.RetryMax {
				if err == nil {
					err = errors.New("launch retry budget exhausted")
				} else {
					err = fmt.Errorf("launch retry budget exhausted: %w", err)
				}
				return Instance{}, &Error{Kind: result.Kind, Err: err}
			}
			if result.RetryAfter > delay {
				delay = result.RetryAfter
			}
			if sleepErr := o.Sleeper.Sleep(ctx, delay); sleepErr != nil {
				return Instance{}, sleepErr
			}
		default:
			return Instance{}, fmt.Errorf("unknown launch result %q", result.Kind)
		}
	}
	for {
		requestCtx, cancel := context.WithTimeout(ctx, in.RequestTimeout)
		instance, err := o.Provider.Get(requestCtx, in.Request.CompartmentID, instanceID)
		cancel()
		if ctx.Err() != nil {
			return Instance{}, ctx.Err()
		}
		if err != nil {
			var classified *Error
			if errors.As(err, &classified) && (classified.Kind == Fatal || classified.Kind == LimitExceeded || classified.Kind == Canceled) {
				return Instance{}, err
			}
			if sleepErr := o.Sleeper.Sleep(ctx, in.RetryMin); sleepErr != nil {
				return Instance{}, sleepErr
			}
			continue
		}
		switch instance.State {
		case "RUNNING":
			if in.Request.PublicIP && instance.PublicIP == "" {
				for retry := 0; ; retry++ {
					delay := backoff(in.RetryMin, in.RetryMax, retry)
					if sleepErr := o.Sleeper.Sleep(ctx, delay); sleepErr != nil {
						return Instance{}, sleepErr
					}
					requestCtx, cancel := context.WithTimeout(ctx, in.RequestTimeout)
					lookedUp, lookupErr := o.Provider.Get(requestCtx, in.Request.CompartmentID, instanceID)
					cancel()
					if ctx.Err() != nil {
						return Instance{}, ctx.Err()
					}
					if lookupErr == nil {
						instance = lookedUp
						if instance.PublicIP != "" {
							break
						}
					}
					if delay == in.RetryMax {
						break
					}
				}
			}
			if err := o.Store.Finished(instance); err != nil {
				return Instance{}, fmt.Errorf("persist running instance: %w", err)
			}
			return instance, nil
		case "TERMINATED", "TERMINATING":
			err := fmt.Errorf("instance %s reached terminal state %s", instance.ID, instance.State)
			_ = o.Store.Failed(err)
			return Instance{}, err
		}
		if err := o.Sleeper.Sleep(ctx, in.RetryMin); err != nil {
			return Instance{}, err
		}
	}
}

type Error struct {
	Kind Kind
	Err  error
}

func (e *Error) Error() string { return fmt.Sprintf("instance launch failed (%s): %v", e.Kind, e.Err) }
func (e *Error) Unwrap() error { return e.Err }

func backoff(minimum, maximum time.Duration, retry int) time.Duration {
	d := minimum
	for i := 0; i < retry && d < maximum; i++ {
		if d > maximum/2 {
			return maximum
		}
		d *= 2
	}
	if d > maximum {
		return maximum
	}
	return d
}
