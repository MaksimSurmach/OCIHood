package launch

import (
	"context"
	"errors"

	"github.com/MaksimSurmach/OCIHood/internal/breaker"
)

// BreakerProvider protects one account/region compute operation family.
type BreakerProvider struct {
	Provider Provider
	Circuit  *breaker.Circuit
}

func (p BreakerProvider) Launch(ctx context.Context, request Request) (result Result, err error) {
	err = p.Circuit.Do(func() error {
		result, err = p.Provider.Launch(ctx, request)
		return classifyBreaker(result.Kind, err)
	})
	var open *breaker.OpenError
	if errors.As(err, &open) {
		return Result{Kind: Transient}, err
	}
	return result, err
}

func (p BreakerProvider) Reconcile(ctx context.Context, request Request) (instance Instance, found bool, err error) {
	err = p.Circuit.Do(func() error {
		instance, found, err = p.Provider.Reconcile(ctx, request)
		return classifyError(err)
	})
	return instance, found, err
}

func (p BreakerProvider) Get(ctx context.Context, compartment, instanceID string) (instance Instance, err error) {
	err = p.Circuit.Do(func() error {
		instance, err = p.Provider.Get(ctx, compartment, instanceID)
		return classifyError(err)
	})
	return instance, err
}

func classifyError(err error) error {
	if err == nil {
		return nil
	}
	var classified *Error
	if errors.As(err, &classified) {
		return classifyBreaker(classified.Kind, err)
	}
	return breaker.Failure(err)
}

func classifyBreaker(kind Kind, err error) error {
	switch kind {
	case Transient, Ambiguous:
		return breaker.Failure(err)
	case OutOfCapacity, Fatal, LimitExceeded, Canceled:
		return breaker.Exclude(err)
	default:
		return err
	}
}

var _ Provider = BreakerProvider{}
