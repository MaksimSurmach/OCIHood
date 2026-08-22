package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/capacity"
	"github.com/MaksimSurmach/OCIHood/internal/config"
	"github.com/MaksimSurmach/OCIHood/internal/discovery"
	"github.com/MaksimSurmach/OCIHood/internal/launch"
	"github.com/MaksimSurmach/OCIHood/internal/provisioner"
	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
	"github.com/MaksimSurmach/OCIHood/internal/state"
)

type crashCloud struct {
	mu         sync.Mutex
	byToken    map[string]string
	instances  map[string]launch.Instance
	requests   []launch.Request
	launchHook func(context.Context, launch.Request, int) (launch.Result, error)
}

func (c *crashCloud) Launch(ctx context.Context, request launch.Request) (launch.Result, error) {
	c.mu.Lock()
	call := len(c.requests)
	c.requests = append(c.requests, request)
	hook := c.launchHook
	c.mu.Unlock()
	if hook != nil {
		return hook(ctx, request, call)
	}
	return c.accept(request), nil
}

func (c *crashCloud) accept(request launch.Request) launch.Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.byToken[request.Attempt.RetryToken]
	if id == "" {
		id = fmt.Sprintf("instance-%d", len(c.byToken)+1)
		c.byToken[request.Attempt.RetryToken] = id
		c.instances[id] = launch.Instance{ID: id, State: "RUNNING"}
	}
	return launch.Result{Kind: launch.Accepted, Instance: launch.Instance{ID: id}}
}

func (c *crashCloud) Reconcile(_ context.Context, request launch.Request) (launch.Instance, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.byToken[request.Attempt.RetryToken]
	instance, ok := c.instances[id]
	return instance, ok, nil
}

func (c *crashCloud) Get(_ context.Context, _, id string) (launch.Instance, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	instance, ok := c.instances[id]
	if !ok {
		return launch.Instance{}, errors.New("instance not found")
	}
	return instance, nil
}

func (c *crashCloud) observations(targetID, account string) []reconcile.Instance {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := []reconcile.Instance{{ID: "unrelated", Lifecycle: reconcile.LifecycleActive, Tags: map[string]string{"owner": "other"}}}
	for id, instance := range c.instances {
		lifecycle := reconcile.LifecycleActive
		if instance.State == "TERMINATED" {
			lifecycle = reconcile.LifecycleTerminated
		}
		result = append(result, reconcile.Instance{ID: id, Lifecycle: lifecycle, Tags: reconcile.OwnershipTags(targetID, account)})
	}
	return result
}

func (c *crashCloud) activeOwned() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	active := 0
	for _, instance := range c.instances {
		if instance.State != "TERMINATED" {
			active++
		}
	}
	return active
}

func (c *crashCloud) launchRequests() []launch.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]launch.Request(nil), c.requests...)
}

type injectedStore struct {
	next        launch.Store
	acceptedErr error
	finishedErr error
}

func (s injectedStore) Accepted(id string) error {
	if s.acceptedErr != nil {
		return s.acceptedErr
	}
	return s.next.Accepted(id)
}
func (s injectedStore) Finished(instance launch.Instance) error {
	if s.finishedErr != nil {
		return s.finishedErr
	}
	return s.next.Finished(instance)
}
func (s injectedStore) Failed(err error) error { return s.next.Failed(err) }

type instantSleeper struct{ stop error }

func (s instantSleeper) Sleep(context.Context, time.Duration) error { return s.stop }

type crashHarness struct {
	t         *testing.T
	effective config.Effective
	targetID  string
	now       time.Time
	cloud     *crashCloud
	discover  func() []reconcile.Instance
	random    *bytes.Reader
}

func newCrashHarness(t *testing.T) *crashHarness {
	t.Helper()
	key := filepath.Join(t.TempDir(), "id.pub")
	if err := os.WriteFile(key, []byte("ssh-ed25519 test"), 0o600); err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 512)
	for i := range random {
		random[i] = byte(i/32 + 1)
	}
	return &crashHarness{
		t: t, effective: config.Effective{Account: "account", Region: "region", StateDir: t.TempDir(), SSHPublicKeyPath: key},
		targetID: "target", now: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC),
		cloud:  &crashCloud{byToken: map[string]string{}, instances: map[string]launch.Instance{}},
		random: bytes.NewReader(random),
	}
}

func (h *crashHarness) runner(store func(launch.StateStore) launch.Store, sleeper launch.Sleeper, watch WatchCapacity) *Runner {
	if watch == nil {
		watch = func(context.Context, provisioner.Bootstrapper, config.Effective, discovery.Result, bool) (capacity.Result, error) {
			return capacity.Result{Kind: capacity.Available, AvailabilityDomain: "AD-1"}, nil
		}
	}
	runner := NewRunner(slog.Default(),
		func(context.Context, string, string) (config.Effective, error) { return h.effective, nil },
		func(context.Context, config.Effective) (provisioner.Bootstrapper, error) {
			return &fakeBootstrapper{run: func(context.Context) error { return nil }}, nil
		},
		func(context.Context, provisioner.Bootstrapper, config.Effective) (discovery.Result, error) {
			instances := h.cloud.observations(h.targetID, h.effective.Account)
			if h.discover != nil {
				instances = h.discover()
			}
			return discovery.Result{TargetID: h.targetID, CompartmentID: "compartment", Image: discovery.Image{ID: "image"}, Subnet: discovery.Subnet{ID: "subnet"}, AvailabilityDomains: []string{"AD-1"}, Instances: instances}, nil
		}, watch)
	runner.random = h.random
	runner.now = func() time.Time { return h.now }
	runner.SetLaunch(func(ctx context.Context, _ provisioner.Bootstrapper, effective config.Effective, discovered discovery.Result, decision reconcile.Decision, placement capacity.Result, key string) (launch.Instance, error) {
		stateStore := launch.StateStore{Store: state.New(effective.StateDir), Account: effective.Account, TargetID: discovered.TargetID, Now: func() time.Time { return h.now }}
		var launchStore launch.Store = stateStore
		if store != nil {
			launchStore = store(stateStore)
		}
		attempt := decision.Attempt
		if attempt == nil {
			attempt = &reconcile.Attempt{}
		}
		return (launch.Orchestrator{Provider: h.cloud, Store: launchStore, Sleeper: sleeper}).Run(ctx, launch.Input{
			Request:            launch.Request{TargetID: discovered.TargetID, Account: effective.Account, CompartmentID: discovered.CompartmentID, AvailabilityDomain: placement.AvailabilityDomain, Shape: effective.Shape, ImageID: discovered.Image.ID, SubnetID: discovered.Subnet.ID, SSHPublicKey: key, OCPUs: effective.OCPUs, MemoryGB: effective.MemoryGB, BootVolumeGB: effective.BootVolumeGB, PublicIP: effective.PublicIP, Attempt: *attempt},
			ExistingInstanceID: decision.InstanceID, RequestTimeout: time.Second, RetryMin: time.Millisecond, RetryMax: 4 * time.Millisecond,
		})
	})
	return runner
}

func (h *crashHarness) run(runner *Runner) (Result, error) {
	return runner.Run(h.t.Context(), Request{Account: h.effective.Account})
}

func (h *crashHarness) durable() state.State {
	h.t.Helper()
	value, err := state.New(h.effective.StateDir).Load(h.effective.Account, h.targetID)
	if err != nil {
		h.t.Fatal(err)
	}
	if value.AttemptID == "" || value.RetryToken == "" || value.AttemptValidTo.IsZero() {
		h.t.Fatalf("inconsistent durable attempt: %+v", value)
	}
	return value
}

func (h *crashHarness) assertSafe() {
	h.t.Helper()
	if count := h.cloud.activeOwned(); count > 1 {
		h.t.Fatalf("active owned instances=%d", count)
	}
	_ = h.durable()
}

func TestCrashSafeProvisioningBoundaries(t *testing.T) {
	t.Run("1 stop before launch", func(t *testing.T) {
		h := newCrashHarness(t)
		first := h.runner(nil, instantSleeper{}, nil)
		first.SetLaunch(nil)
		result, err := h.run(first)
		if err != nil || result.Attempt == nil {
			t.Fatalf("first result=%+v err=%v", result, err)
		}
		attempt := h.durable()
		if _, err := h.run(h.runner(nil, instantSleeper{}, nil)); err != nil {
			t.Fatal(err)
		}
		if h.cloud.requests[0].Attempt.ID != attempt.AttemptID {
			t.Fatal("restart did not reuse persisted attempt")
		}
		h.assertSafe()
	})

	t.Run("2 success response lost", func(t *testing.T) {
		h := newCrashHarness(t)
		h.cloud.launchHook = func(_ context.Context, request launch.Request, _ int) (launch.Result, error) {
			h.cloud.accept(request)
			return launch.Result{Kind: launch.Ambiguous}, errors.New("response lost")
		}
		if _, err := h.run(h.runner(nil, instantSleeper{stop: context.Canceled}, nil)); !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		h.cloud.launchHook = nil
		if _, err := h.run(h.runner(nil, instantSleeper{}, nil)); err != nil {
			t.Fatal(err)
		}
		h.assertSafe()
	})

	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "3 created before instance persistence", err: errors.New("process stopped")},
		{name: "4 accepted state write fails", err: errors.New("disk full")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCrashHarness(t)
			h.cloud.launchHook = func(_ context.Context, request launch.Request, _ int) (launch.Result, error) {
				return h.cloud.accept(request), nil
			}
			_, err := h.run(h.runner(func(next launch.StateStore) launch.Store { return injectedStore{next: next, acceptedErr: tc.err} }, instantSleeper{}, nil))
			if err == nil {
				t.Fatal("interrupted run succeeded")
			}
			if _, err := h.run(h.runner(nil, instantSleeper{}, nil)); err != nil {
				t.Fatal(err)
			}
			h.assertSafe()
		})
	}

	t.Run("5 starting before lifecycle completion", func(t *testing.T) {
		h := newCrashHarness(t)
		h.cloud.launchHook = func(_ context.Context, request launch.Request, _ int) (launch.Result, error) {
			result := h.cloud.accept(request)
			h.cloud.mu.Lock()
			h.cloud.instances[result.Instance.ID] = launch.Instance{ID: result.Instance.ID, State: "STARTING"}
			h.cloud.mu.Unlock()
			return result, nil
		}
		if _, err := h.run(h.runner(nil, instantSleeper{stop: context.Canceled}, nil)); !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		h.cloud.mu.Lock()
		for id := range h.cloud.instances {
			h.cloud.instances[id] = launch.Instance{ID: id, State: "RUNNING"}
		}
		h.cloud.mu.Unlock()
		if _, err := h.run(h.runner(nil, instantSleeper{}, nil)); err != nil {
			t.Fatal(err)
		}
		h.assertSafe()
	})

	t.Run("6 running before final persistence", func(t *testing.T) {
		h := newCrashHarness(t)
		_, err := h.run(h.runner(func(next launch.StateStore) launch.Store {
			return injectedStore{next: next, finishedErr: errors.New("disk full")}
		}, instantSleeper{}, nil))
		if err == nil {
			t.Fatal("final write failure was ignored")
		}
		if _, err := h.run(h.runner(nil, instantSleeper{}, nil)); err != nil {
			t.Fatal(err)
		}
		h.assertSafe()
	})

	t.Run("7 temporarily stale provider observation", func(t *testing.T) {
		h := newCrashHarness(t)
		first := h.runner(nil, instantSleeper{}, nil)
		first.SetLaunch(nil)
		_, _ = h.run(first)
		attempt := h.durable()
		h.discover = func() []reconcile.Instance {
			return []reconcile.Instance{{ID: "unrelated", Lifecycle: reconcile.LifecycleActive}}
		}
		if _, err := h.run(h.runner(nil, instantSleeper{}, nil)); err != nil {
			t.Fatal(err)
		}
		if h.cloud.requests[0].Attempt.RetryToken != attempt.RetryToken {
			t.Fatal("stale observation created a new attempt")
		}
		h.assertSafe()
	})

	t.Run("8 transient retry interrupted", func(t *testing.T) {
		h := newCrashHarness(t)
		h.cloud.launchHook = func(_ context.Context, request launch.Request, call int) (launch.Result, error) {
			if call == 0 {
				return launch.Result{Kind: launch.Transient}, errors.New("network")
			}
			return h.cloud.accept(request), nil
		}
		if _, err := h.run(h.runner(nil, instantSleeper{stop: context.Canceled}, nil)); !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		attempt := h.durable()
		if _, err := h.run(h.runner(nil, instantSleeper{}, nil)); err != nil {
			t.Fatal(err)
		}
		if h.cloud.requests[1].Attempt.RetryToken != attempt.RetryToken {
			t.Fatal("transient restart changed retry token")
		}
		h.assertSafe()
	})

	t.Run("9 capacity rotation interrupted", func(t *testing.T) {
		h := newCrashHarness(t)
		h.cloud.launchHook = func(context.Context, launch.Request, int) (launch.Result, error) {
			return launch.Result{Kind: launch.OutOfCapacity}, errors.New("capacity")
		}
		watches := 0
		watch := func(context.Context, provisioner.Bootstrapper, config.Effective, discovery.Result, bool) (capacity.Result, error) {
			watches++
			if watches == 2 {
				return capacity.Result{}, errors.New("process stopped")
			}
			return capacity.Result{Kind: capacity.Available, AvailabilityDomain: "AD-1"}, nil
		}
		if _, err := h.run(h.runner(nil, instantSleeper{}, watch)); err == nil {
			t.Fatal("interrupted rotation succeeded")
		}
		rotated := h.durable()
		h.cloud.launchHook = nil
		if _, err := h.run(h.runner(nil, instantSleeper{}, nil)); err != nil {
			t.Fatal(err)
		}
		if h.cloud.requests[1].Attempt.RetryToken != rotated.RetryToken || h.cloud.requests[0].Attempt.RetryToken == rotated.RetryToken {
			t.Fatal("capacity rotation attempt continuity is wrong")
		}
		h.assertSafe()
	})

	t.Run("10 concurrent runners", func(t *testing.T) {
		h := newCrashHarness(t)
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		h.cloud.launchHook = func(_ context.Context, request launch.Request, _ int) (launch.Result, error) {
			entered <- struct{}{}
			<-release
			return h.cloud.accept(request), nil
		}
		errs := make(chan error, 2)
		go func() { _, err := h.run(h.runner(nil, instantSleeper{}, nil)); errs <- err }()
		<-entered
		go func() { _, err := h.run(h.runner(nil, instantSleeper{}, nil)); errs <- err }()
		if err := <-errs; err == nil {
			t.Fatal("concurrent runner entered mutating transaction")
		}
		close(release)
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		requests := h.cloud.launchRequests()
		if len(requests) != 1 {
			t.Fatalf("concurrent requests=%+v", requests)
		}
		h.assertSafe()
	})
}

func TestCrashRestartRejectsInvalidDurableState(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
	}{
		{name: "corrupt", data: "{"},
		{name: "unsupported", data: `{"schema_version":999,"account":"account","lifecycle":"provisioning","target_id":"target","updated_at":"2026-08-22T12:00:00Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCrashHarness(t)
			locked, err := state.New(h.effective.StateDir).TryLock(h.effective.Account, h.targetID)
			if err != nil {
				t.Fatal(err)
			}
			if err := locked.Save(state.State{Account: h.effective.Account, TargetID: h.targetID, Lifecycle: state.Provisioning, AttemptID: "attempt", RetryToken: "token", AttemptValidTo: h.now.Add(time.Hour), UpdatedAt: h.now}); err != nil {
				t.Fatal(err)
			}
			_ = locked.Close()
			files, _ := filepath.Glob(filepath.Join(h.effective.StateDir, "*", "*.json"))
			if len(files) != 1 || os.WriteFile(files[0], []byte(tc.data), 0o600) != nil {
				t.Fatalf("state files=%v", files)
			}
			if _, err := h.run(h.runner(nil, instantSleeper{}, nil)); err == nil || len(h.cloud.requests) != 0 {
				t.Fatalf("err=%v launches=%d", err, len(h.cloud.requests))
			}
		})
	}
}
