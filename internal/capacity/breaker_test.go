package capacity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/breaker"
)

type breakerFakeClient struct {
	results []ProbeResult
	calls   int
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
