package capacity

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeClient struct {
	results  []ProbeResult
	errs     []error
	requests []Request
	call     func(context.Context) (ProbeResult, error)
}

func (f *fakeClient) Probe(ctx context.Context, request Request) (ProbeResult, error) {
	f.requests = append(f.requests, request)
	if f.call != nil {
		return f.call(ctx)
	}
	i := len(f.requests) - 1
	return f.results[i], f.errs[i]
}

type fakeStore struct {
	states []State
	err    error
}

func (f *fakeStore) Save(state State) error { f.states = append(f.states, state); return f.err }

type fakeSleeper struct {
	now       *time.Time
	durations []time.Duration
	after     func()
}

func (f *fakeSleeper) Sleep(ctx context.Context, d time.Duration) error {
	f.durations = append(f.durations, d)
	if f.after != nil {
		f.after()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	*f.now = f.now.Add(d)
	return nil
}

type fixedRandom float64

func (f fixedRandom) Float64() float64 { return float64(f) }

func newWatcher(client Client, store Store, sleeper Sleeper, now *time.Time) Watcher {
	return Watcher{Client: client, Store: store, Sleeper: sleeper, Random: fixedRandom(.5), Now: func() time.Time { return *now }, Config: Config{RequestTimeout: time.Second, InitialInterval: time.Second, MaxInterval: 4 * time.Second, Jitter: .2}}
}
func input() Input {
	return Input{TargetID: "target", TenancyID: "root", Shape: "VM.Standard.A1.Flex", AvailabilityDomains: []string{"AD-1", "AD-2"}, OCPUs: 2, MemoryGB: 12}
}

func TestWatcherRotationBackoffAndRequest(t *testing.T) {
	now := time.Date(2026, 8, 22, 7, 0, 0, 0, time.UTC)
	client := &fakeClient{results: []ProbeResult{{Kind: Unavailable}, {Kind: Unavailable}, {Kind: Unavailable}, {Kind: Available}}, errs: make([]error, 4)}
	store := &fakeStore{}
	sleeper := &fakeSleeper{now: &now}
	got, err := newWatcher(client, store, sleeper, &now).Watch(t.Context(), input())
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != Available || got.AvailabilityDomain != "AD-2" {
		t.Fatalf("result = %+v", got)
	}
	wantADs := []string{"AD-1", "AD-2", "AD-1", "AD-2"}
	var gotADs []string
	for _, request := range client.requests {
		gotADs = append(gotADs, request.AvailabilityDomain)
		if request.TenancyID != "root" || request.Shape != "VM.Standard.A1.Flex" || request.OCPUs != 2 || request.MemoryGB != 12 {
			t.Fatalf("request = %+v", request)
		}
	}
	if !reflect.DeepEqual(gotADs, wantADs) {
		t.Fatalf("AD rotation = %v, want %v", gotADs, wantADs)
	}
	if !reflect.DeepEqual(sleeper.durations, []time.Duration{time.Second}) {
		t.Fatalf("sleeps = %v", sleeper.durations)
	}
	if len(store.states) != 2 || store.states[0].RetryCount != 1 || store.states[1].Status != Available {
		t.Fatalf("states = %+v", store.states)
	}
}

func TestWatcherClassificationRestartAndCancellation(t *testing.T) {
	t.Run("probe unavailable returns safe fallback", func(t *testing.T) {
		now := time.Now()
		store := &fakeStore{}
		client := &fakeClient{results: []ProbeResult{{Kind: ProbeUnavailable}}, errs: []error{errors.New("forbidden")}}
		got, err := newWatcher(client, store, &fakeSleeper{now: &now}, &now).Watch(t.Context(), input())
		if err != nil || got.Kind != ProbeUnavailable {
			t.Fatalf("result = %+v, err = %v", got, err)
		}
	})
	t.Run("fatal terminates", func(t *testing.T) {
		now := time.Now()
		cause := errors.New("unauthorized")
		client := &fakeClient{results: []ProbeResult{{Kind: Fatal}}, errs: []error{cause}}
		_, err := newWatcher(client, &fakeStore{}, &fakeSleeper{now: &now}, &now).Watch(t.Context(), input())
		var got *Error
		if !errors.As(err, &got) || got.Kind != Fatal || !errors.Is(err, cause) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("restart honors persisted delay and AD", func(t *testing.T) {
		now := time.Now()
		resume := input()
		resume.Resume = State{NextAD: 1, NextAttempt: now.Add(3 * time.Second), RetryCount: 2}
		client := &fakeClient{results: []ProbeResult{{Kind: Available}}, errs: []error{nil}}
		sleeper := &fakeSleeper{now: &now}
		got, err := newWatcher(client, &fakeStore{}, sleeper, &now).Watch(t.Context(), resume)
		if err != nil || got.AvailabilityDomain != "AD-2" || !reflect.DeepEqual(sleeper.durations, []time.Duration{3 * time.Second}) {
			t.Fatalf("result=%+v sleeps=%v err=%v", got, sleeper.durations, err)
		}
	})
	t.Run("sleep cancellation is immediate", func(t *testing.T) {
		now := time.Now()
		ctx, cancel := context.WithCancel(t.Context())
		sleeper := &fakeSleeper{now: &now, after: cancel}
		client := &fakeClient{results: []ProbeResult{{Kind: Unavailable}, {Kind: Unavailable}}, errs: make([]error, 2)}
		_, err := newWatcher(client, &fakeStore{}, sleeper, &now).Watch(ctx, input())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("api cancellation is immediate", func(t *testing.T) {
		now := time.Now()
		ctx, cancel := context.WithCancel(t.Context())
		client := &fakeClient{call: func(callCtx context.Context) (ProbeResult, error) {
			cancel()
			<-callCtx.Done()
			return ProbeResult{Kind: Canceled}, callCtx.Err()
		}}
		_, err := newWatcher(client, &fakeStore{}, &fakeSleeper{now: &now}, &now).Watch(ctx, input())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("request timeout retries with backoff", func(t *testing.T) {
		now := time.Now()
		calls := 0
		client := &fakeClient{call: func(ctx context.Context) (ProbeResult, error) {
			calls++
			if calls == 1 {
				<-ctx.Done()
				return ProbeResult{Kind: Canceled}, ctx.Err()
			}
			return ProbeResult{Kind: Available}, nil
		}}
		sleeper := &fakeSleeper{now: &now}
		w := newWatcher(client, &fakeStore{}, sleeper, &now)
		w.Config.RequestTimeout = 10 * time.Millisecond
		got, err := w.Watch(t.Context(), Input{TargetID: "target", TenancyID: "root", Shape: "shape", AvailabilityDomains: []string{"AD"}, OCPUs: 1, MemoryGB: 1})
		if err != nil || got.Kind != Available || calls != 2 || !reflect.DeepEqual(sleeper.durations, []time.Duration{time.Second}) {
			t.Fatalf("result=%+v calls=%d sleeps=%v err=%v", got, calls, sleeper.durations, err)
		}
	})
}

func TestWatcherJitterRetryAfterAndQuietTransitions(t *testing.T) {
	now := time.Now()
	ctx, cancel := context.WithCancel(t.Context())
	client := &fakeClient{results: []ProbeResult{{Kind: Throttled, RetryAfter: 3 * time.Second}, {Kind: Throttled}, {Kind: Throttled}}, errs: []error{errors.New("429"), errors.New("429"), errors.New("429")}}
	store := &fakeStore{}
	sleeper := &fakeSleeper{now: &now, after: func() {
		if len(client.requests) == 3 {
			cancel()
		}
	}}
	var logs bytes.Buffer
	w := newWatcher(client, store, sleeper, &now)
	w.Random = fixedRandom(1)
	w.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	_, err := w.Watch(ctx, Input{TargetID: "target", TenancyID: "root", Shape: "shape", AvailabilityDomains: []string{"AD"}, OCPUs: 1, MemoryGB: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(sleeper.durations, []time.Duration{3 * time.Second, 2400 * time.Millisecond, 4 * time.Second}) {
		t.Fatalf("sleeps = %v", sleeper.durations)
	}
	if strings.Count(logs.String(), "capacity state changed") != 1 {
		t.Fatalf("logs = %s", logs.String())
	}
}
