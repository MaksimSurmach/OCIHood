package launch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/breaker"
)

type breakerFakeProvider struct {
	launches int
	results  []Result
}

func (f *breakerFakeProvider) Launch(context.Context, Request) (Result, error) {
	r := f.results[f.launches]
	f.launches++
	return r, errors.New("provider")
}
func (*breakerFakeProvider) Reconcile(context.Context, Request) (Instance, bool, error) {
	return Instance{}, false, nil
}
func (*breakerFakeProvider) Get(context.Context, string, string) (Instance, error) {
	return Instance{}, nil
}

func TestBreakerProviderOpenDoesNotLaunchAgain(t *testing.T) {
	circuit, err := breaker.New("launch", breaker.Config{Failures: 1, HalfOpenRequests: 1, Interval: time.Minute, OpenTimeout: time.Minute}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &breakerFakeProvider{results: []Result{{Kind: Transient}}}
	provider := BreakerProvider{Provider: fake, Circuit: circuit}
	_, _ = provider.Launch(context.Background(), Request{})
	result, err := provider.Launch(context.Background(), Request{})
	var open *breaker.OpenError
	if result.Kind != Transient || !errors.As(err, &open) || fake.launches != 1 {
		t.Fatalf("result=%+v err=%v launches=%d", result, err, fake.launches)
	}
}

func TestBreakerProviderExcludesOutOfCapacityAndFatal(t *testing.T) {
	circuit, err := breaker.New("launch", breaker.Config{Failures: 1, HalfOpenRequests: 1, Interval: time.Minute, OpenTimeout: time.Minute}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &breakerFakeProvider{results: []Result{{Kind: OutOfCapacity}, {Kind: Fatal}, {Kind: Accepted}}}
	provider := BreakerProvider{Provider: fake, Circuit: circuit}
	for range 3 {
		_, _ = provider.Launch(context.Background(), Request{})
	}
	if fake.launches != 3 {
		t.Fatalf("excluded outcomes opened breaker: launches=%d", fake.launches)
	}
}
