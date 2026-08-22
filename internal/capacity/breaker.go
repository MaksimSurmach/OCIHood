package capacity

import (
	"context"
	"errors"

	"github.com/MaksimSurmach/OCIHood/internal/breaker"
)

// BreakerClient applies fast-fail protection without owning retry timing.
type BreakerClient struct {
	Client  Client
	Circuit *breaker.Circuit
}

func (c BreakerClient) Probe(ctx context.Context, request Request) (result ProbeResult, err error) {
	err = c.Circuit.Do(func() error {
		result, err = c.Client.Probe(ctx, request)
		switch result.Kind {
		case Transient, Throttled:
			return breaker.Failure(err)
		case Unavailable, ProbeUnavailable, Fatal, Canceled:
			return breaker.Exclude(err)
		default:
			return err
		}
	})
	var open *breaker.OpenError
	if errors.As(err, &open) {
		return ProbeResult{Kind: Transient}, err
	}
	return result, err
}

var _ Client = BreakerClient{}
