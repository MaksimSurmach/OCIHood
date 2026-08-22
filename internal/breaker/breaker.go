// Package breaker provides provider-independent OCI circuit breaking.
package breaker

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sony/gobreaker/v2"
)

type Config struct {
	Failures, HalfOpenRequests uint32
	Interval, OpenTimeout      time.Duration
}

type OpenError struct{ Name string }

func (e *OpenError) Error() string { return fmt.Sprintf("OCI circuit breaker %s is open", e.Name) }

type outcome struct {
	err     error
	failure bool
}

func (e *outcome) Error() string {
	if e.err == nil {
		return "excluded circuit breaker outcome"
	}
	return e.err.Error()
}
func (e *outcome) Unwrap() error { return e.err }

func Failure(err error) error { return &outcome{err: err, failure: true} }
func Exclude(err error) error { return &outcome{err: err} }

type Circuit struct {
	name string
	cb   *gobreaker.CircuitBreaker[struct{}]
}

func New(name string, cfg Config, logger *slog.Logger) (*Circuit, error) {
	if name == "" || cfg.Failures < 1 || cfg.Failures > 100 || cfg.HalfOpenRequests < 1 || cfg.HalfOpenRequests > 10 || cfg.Interval < time.Second || cfg.Interval > 24*time.Hour || cfg.OpenTimeout < time.Second || cfg.OpenTimeout > time.Hour {
		return nil, errors.New("invalid circuit breaker name or configuration")
	}
	settings := gobreaker.Settings{
		Name: name, MaxRequests: cfg.HalfOpenRequests, Interval: cfg.Interval, Timeout: cfg.OpenTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool { return counts.ConsecutiveFailures >= cfg.Failures },
		IsSuccessful: func(err error) bool {
			var marked *outcome
			return err == nil || errors.As(err, &marked) && !marked.failure
		},
		IsExcluded: func(err error) bool {
			var marked *outcome
			return errors.As(err, &marked) && !marked.failure
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			if logger != nil {
				logger.Info("OCI circuit breaker state changed", "breaker", name, "from", from.String(), "status", to.String())
			}
		},
	}
	return &Circuit{name: name, cb: gobreaker.NewCircuitBreaker[struct{}](settings)}, nil
}

func (c *Circuit) Do(fn func() error) error {
	_, err := c.cb.Execute(func() (struct{}, error) { return struct{}{}, fn() })
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		return &OpenError{Name: c.name}
	}
	var marked *outcome
	if errors.As(err, &marked) {
		return marked.err
	}
	return err
}
