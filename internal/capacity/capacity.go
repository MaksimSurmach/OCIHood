// Package capacity waits for advisory OCI compute capacity without creating resources.
package capacity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type Kind string

const (
	Available        Kind = "available"
	Unavailable      Kind = "unavailable"
	ProbeUnavailable Kind = "probe_unavailable"
	Throttled        Kind = "throttled"
	Transient        Kind = "transient"
	Fatal            Kind = "fatal"
	Canceled         Kind = "canceled"
)

type Request struct {
	TenancyID, AvailabilityDomain, Shape string
	OCPUs, MemoryGB                      int
}

type ProbeResult struct {
	Kind       Kind
	RetryAfter time.Duration
}

type Client interface {
	Probe(context.Context, Request) (ProbeResult, error)
}

type State struct {
	TargetID    string
	LastAD      string
	NextAD      int
	RetryCount  int
	NextAttempt time.Time
	Status      Kind
}

type Store interface {
	Save(State) error
}

type Sleeper interface {
	Sleep(context.Context, time.Duration) error
}

type Random interface {
	Float64() float64
}

type Config struct {
	RequestTimeout, InitialInterval, MaxInterval time.Duration
	Jitter                                       float64
}

type Input struct {
	TargetID, TenancyID, Shape string
	AvailabilityDomains        []string
	OCPUs, MemoryGB            int
	Resume                     State
}

type Result struct {
	Kind               Kind
	AvailabilityDomain string
}

// Error is a classifiable terminal watcher failure.
type Error struct {
	Kind Kind
	AD   string
	Err  error
}

func (e *Error) Error() string {
	return fmt.Sprintf("capacity probe %s failed (%s): %v", e.AD, e.Kind, e.Err)
}
func (e *Error) Unwrap() error { return e.Err }

type Watcher struct {
	Client  Client
	Store   Store
	Sleeper Sleeper
	Random  Random
	Logger  *slog.Logger
	Now     func() time.Time
	Config  Config
}

// Watch rotates through every AD and returns once capacity or probe fallback is available.
func (w Watcher) Watch(ctx context.Context, in Input) (Result, error) {
	if err := w.validate(in); err != nil {
		return Result{}, &Error{Kind: Fatal, Err: err}
	}
	state := in.Resume
	state.TargetID = in.TargetID
	if state.NextAD < 0 || state.NextAD >= len(in.AvailabilityDomains) {
		state.NextAD = 0
	}
	if !state.NextAttempt.IsZero() && state.NextAttempt.After(w.Now()) {
		if err := w.Sleeper.Sleep(ctx, state.NextAttempt.Sub(w.Now())); err != nil {
			return Result{}, canceled(err)
		}
	}
	lastLogged := state.Status
	for {
		ad := in.AvailabilityDomains[state.NextAD]
		requestCtx, cancel := context.WithTimeout(ctx, w.Config.RequestTimeout)
		probe, err := w.Client.Probe(requestCtx, Request{TenancyID: in.TenancyID, AvailabilityDomain: ad, Shape: in.Shape, OCPUs: in.OCPUs, MemoryGB: in.MemoryGB})
		cancel()
		if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && ctx.Err() != nil {
			return Result{}, canceled(ctx.Err())
		}
		if err != nil && probe.Kind == "" {
			probe.Kind = Transient
		}
		switch probe.Kind {
		case Available, ProbeUnavailable:
			state.LastAD, state.Status, state.NextAttempt = ad, probe.Kind, time.Time{}
			if err := w.Store.Save(state); err != nil {
				return Result{}, &Error{Kind: Fatal, AD: ad, Err: fmt.Errorf("persist watcher state: %w", err)}
			}
			w.logTransition(ctx, lastLogged, probe.Kind, ad)
			return Result{Kind: probe.Kind, AvailabilityDomain: ad}, nil
		case Fatal:
			state.LastAD, state.Status, state.NextAttempt = ad, Fatal, time.Time{}
			if saveErr := w.Store.Save(state); saveErr != nil {
				return Result{}, &Error{Kind: Fatal, AD: ad, Err: fmt.Errorf("persist fatal watcher state: %w", saveErr)}
			}
			return Result{}, &Error{Kind: Fatal, AD: ad, Err: err}
		case Canceled:
			return Result{}, canceled(err)
		case Unavailable, Throttled, Transient:
		default:
			return Result{}, &Error{Kind: Fatal, AD: ad, Err: fmt.Errorf("unknown probe result %q", probe.Kind)}
		}

		state.LastAD, state.Status = ad, probe.Kind
		state.NextAD = (state.NextAD + 1) % len(in.AvailabilityDomains)
		if state.NextAD != 0 && probe.RetryAfter <= 0 {
			continue
		}
		state.RetryCount++
		delay := w.backoff(state.RetryCount)
		if probe.RetryAfter > delay {
			delay = probe.RetryAfter
		}
		state.NextAttempt = w.Now().Add(delay)
		if err := w.Store.Save(state); err != nil {
			return Result{}, &Error{Kind: Fatal, AD: ad, Err: fmt.Errorf("persist watcher state: %w", err)}
		}
		w.logTransition(ctx, lastLogged, probe.Kind, ad)
		lastLogged = probe.Kind
		if err := w.Sleeper.Sleep(ctx, delay); err != nil {
			return Result{}, canceled(err)
		}
	}
}

func (w Watcher) validate(in Input) error {
	if w.Client == nil || w.Store == nil || w.Sleeper == nil || w.Random == nil || w.Now == nil || len(in.AvailabilityDomains) == 0 || in.TargetID == "" || in.TenancyID == "" || in.Shape == "" || in.OCPUs <= 0 || in.MemoryGB <= 0 {
		return errors.New("complete watcher dependencies and capacity input are required")
	}
	if w.Config.RequestTimeout <= 0 || w.Config.InitialInterval <= 0 || w.Config.MaxInterval < w.Config.InitialInterval || w.Config.Jitter < 0 || w.Config.Jitter > 1 {
		return errors.New("invalid watcher timeout, retry, or jitter configuration")
	}
	return nil
}

func (w Watcher) backoff(retry int) time.Duration {
	delay := w.Config.InitialInterval
	for i := 1; i < retry && delay < w.Config.MaxInterval; i++ {
		if delay > w.Config.MaxInterval/2 {
			delay = w.Config.MaxInterval
		} else {
			delay *= 2
		}
	}
	factor := 1 - w.Config.Jitter + 2*w.Config.Jitter*w.Random.Float64()
	delay = time.Duration(float64(delay) * factor)
	if delay > w.Config.MaxInterval {
		return w.Config.MaxInterval
	}
	return delay
}

func (w Watcher) logTransition(ctx context.Context, previous, next Kind, ad string) {
	if w.Logger != nil && previous != next {
		w.Logger.InfoContext(ctx, "capacity state changed", "status", next, "availability_domain", ad)
	}
}

func canceled(err error) error {
	if err == nil {
		err = context.Canceled
	}
	return &Error{Kind: Canceled, Err: err}
}
