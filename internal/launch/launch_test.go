package launch

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
)

type fakeProvider struct {
	launches       []Result
	launchErrors   []error
	gets           []Instance
	getErrors      []error
	launchRequests []Request
	getCalls       int
}

func (f *fakeProvider) Launch(_ context.Context, r Request) (Result, error) {
	f.launchRequests = append(f.launchRequests, r)
	i := len(f.launchRequests) - 1
	return f.launches[i], f.launchErrors[i]
}
func (f *fakeProvider) Get(_ context.Context, _, id string) (Instance, error) {
	i := f.getCalls
	f.getCalls++
	if i >= len(f.gets) {
		return Instance{}, errors.New("not found")
	}
	got := f.gets[i]
	if i < len(f.getErrors) && f.getErrors[i] != nil {
		return Instance{}, f.getErrors[i]
	}
	if got.ID == "" {
		got.ID = id
	}
	return got, nil
}

type fakeStore struct{ events []string }

func (s *fakeStore) Accepted(string) error   { s.events = append(s.events, "accepted"); return nil }
func (s *fakeStore) Finished(Instance) error { s.events = append(s.events, "running"); return nil }
func (s *fakeStore) Failed(error) error      { s.events = append(s.events, "failed"); return nil }

type fakeSleeper struct {
	calls  int
	cancel context.CancelFunc
}

func (s *fakeSleeper) Sleep(ctx context.Context, _ time.Duration) error {
	s.calls++
	if s.cancel != nil {
		s.cancel()
	}
	return ctx.Err()
}

func validInput() Input {
	return Input{Request: Request{TargetID: "target", Account: "acct", CompartmentID: "compartment", AvailabilityDomain: "AD-1", Shape: "shape", ImageID: "image", SubnetID: "subnet", SSHPublicKey: "ssh-ed25519 key", OCPUs: 2, MemoryGB: 12, BootVolumeGB: 50, PublicIP: true, Attempt: reconcile.Attempt{ID: "attempt", RetryToken: "token"}}, RequestTimeout: time.Second, RetryMin: time.Millisecond, RetryMax: 4 * time.Millisecond}
}

func TestOrchestratorHappyPathAndRestart(t *testing.T) {
	for _, tt := range []struct {
		name         string
		existing     string
		launches     []Result
		wantLaunches int
	}{
		{name: "new", launches: []Result{{Kind: Accepted, Instance: Instance{ID: "instance"}}}, wantLaunches: 1},
		{name: "existing", existing: "instance", wantLaunches: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := &fakeProvider{launches: tt.launches, launchErrors: make([]error, len(tt.launches)), gets: []Instance{{State: "STARTING"}, {State: "RUNNING", PublicIP: "203.0.113.1"}}}
			store := &fakeStore{}
			sleeper := &fakeSleeper{}
			in := validInput()
			in.ExistingInstanceID = tt.existing
			got, err := (Orchestrator{Provider: provider, Store: store, Sleeper: sleeper}).Run(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != "RUNNING" || got.PublicIP != "203.0.113.1" || len(provider.launchRequests) != tt.wantLaunches {
				t.Fatalf("got=%+v launches=%d", got, len(provider.launchRequests))
			}
			wantEvents := []string{"accepted", "running"}
			if tt.existing != "" {
				wantEvents = []string{"running"}
			}
			if !reflect.DeepEqual(store.events, wantEvents) {
				t.Fatalf("events=%v", store.events)
			}
		})
	}
}

func TestOrchestratorClassifiesAndCancels(t *testing.T) {
	t.Run("capacity race", func(t *testing.T) {
		provider := &fakeProvider{launches: []Result{{Kind: OutOfCapacity}}, launchErrors: []error{errors.New("capacity")}}
		_, err := (Orchestrator{Provider: provider, Store: &fakeStore{}, Sleeper: &fakeSleeper{}}).Run(t.Context(), validInput())
		var got *Error
		if !errors.As(err, &got) || got.Kind != OutOfCapacity {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("transient retry same request", func(t *testing.T) {
		provider := &fakeProvider{launches: []Result{{Kind: Transient}, {Kind: Accepted, Instance: Instance{ID: "i"}}}, launchErrors: []error{errors.New("network"), nil}, gets: []Instance{{State: "RUNNING"}}}
		in := validInput()
		_, err := (Orchestrator{Provider: provider, Store: &fakeStore{}, Sleeper: &fakeSleeper{}}).Run(t.Context(), in)
		if err != nil {
			t.Fatal(err)
		}
		if len(provider.launchRequests) != 2 || provider.launchRequests[0].Attempt != provider.launchRequests[1].Attempt {
			t.Fatalf("requests=%+v", provider.launchRequests)
		}
	})
	t.Run("cancellation during retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		provider := &fakeProvider{launches: []Result{{Kind: Transient}}, launchErrors: []error{errors.New("network")}}
		_, err := (Orchestrator{Provider: provider, Store: &fakeStore{}, Sleeper: &fakeSleeper{cancel: cancel}}).Run(ctx, validInput())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	})
	for _, kind := range []Kind{Fatal, LimitExceeded, Canceled} {
		t.Run(string(kind), func(t *testing.T) {
			cause := errors.New("classified")
			provider := &fakeProvider{launches: []Result{{Kind: kind}}, launchErrors: []error{cause}}
			store := &fakeStore{}
			_, err := (Orchestrator{Provider: provider, Store: store, Sleeper: &fakeSleeper{}}).Run(t.Context(), validInput())
			var got *Error
			if !errors.As(err, &got) || got.Kind != kind || !errors.Is(err, cause) {
				t.Fatalf("err=%v", err)
			}
			if kind != Canceled && !reflect.DeepEqual(store.events, []string{"failed"}) {
				t.Fatalf("events=%v", store.events)
			}
		})
	}
	t.Run("terminal lifecycle", func(t *testing.T) {
		store := &fakeStore{}
		provider := &fakeProvider{gets: []Instance{{State: "TERMINATED"}}}
		in := validInput()
		in.ExistingInstanceID = "instance"
		if _, err := (Orchestrator{Provider: provider, Store: store, Sleeper: &fakeSleeper{}}).Run(t.Context(), in); err == nil || !reflect.DeepEqual(store.events, []string{"failed"}) {
			t.Fatalf("err=%v events=%v", err, store.events)
		}
	})
}
