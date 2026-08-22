package capacity

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/breaker"
)

type breakerFakeClient struct {
	results []ProbeResult
	calls   int
}

func TestWatcherSharesBreakerAcrossADRotationAndOwnsBackoff(t *testing.T) {
	now := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	provider := &fakeClient{
		results: []ProbeResult{{Kind: Transient}, {Kind: Transient}},
		errs:    []error{errors.New("503"), errors.New("503")},
	}
	circuit, err := breaker.New("capacity", breaker.Config{Failures: 2, HalfOpenRequests: 1, Interval: time.Minute, OpenTimeout: time.Minute}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	sleeper := &fakeSleeper{now: &now}
	sleeper.after = func() {
		if len(sleeper.durations) == 3 {
			cancel()
		}
	}

	_, err = newWatcher(BreakerClient{Client: provider, Circuit: circuit}, &fakeStore{}, sleeper, &now).Watch(ctx, input())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	gotADs := []string{provider.requests[0].AvailabilityDomain, provider.requests[1].AvailabilityDomain}
	if len(provider.requests) != 2 || !reflect.DeepEqual(gotADs, []string{"AD-1", "AD-2"}) || !reflect.DeepEqual(sleeper.durations, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}) {
		t.Fatalf("provider ADs=%v sleeps=%v", gotADs, sleeper.durations)
	}
}

func (f *breakerFakeClient) Probe(context.Context, Request) (ProbeResult, error) {
	r := f.results[f.calls]
	f.calls++
	if r.Kind == Transient {
		return r, errors.New("503")
	}
	return r, nil
}

func TestBreakerClientExcludesNoCapacityAndFastFailsTransient(t *testing.T) {
	circuit, err := breaker.New("capacity", breaker.Config{Failures: 2, HalfOpenRequests: 1, Interval: time.Minute, OpenTimeout: time.Minute}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &breakerFakeClient{results: []ProbeResult{{Kind: Unavailable}, {Kind: Unavailable}, {Kind: Transient}, {Kind: Transient}}}
	client := BreakerClient{Client: fake, Circuit: circuit}
	for range 4 {
		_, _ = client.Probe(context.Background(), Request{})
	}
	result, err := client.Probe(context.Background(), Request{})
	var open *breaker.OpenError
	if result.Kind != Transient || !errors.As(err, &open) || fake.calls != 4 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, fake.calls)
	}
}
