package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/capacity"
	"github.com/MaksimSurmach/OCIHood/internal/config"
	"github.com/MaksimSurmach/OCIHood/internal/discovery"
	"github.com/MaksimSurmach/OCIHood/internal/provisioner"
	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
	"github.com/MaksimSurmach/OCIHood/internal/state"
)

type fakeBootstrapper struct {
	run func(context.Context) error
}

func TestRunnerRestartLoadsAttemptBeforeCreateDecision(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	effective := config.Effective{Account: "personal", Region: "eu-frankfurt-1", StateDir: root}
	store := state.New(root)
	now := time.Date(2026, 8, 22, 6, 30, 0, 0, time.UTC)
	provider := &fakeBootstrapper{run: func(context.Context) error { return nil }}
	newProcess := func() *Runner {
		runner := NewRunner(slog.Default(), func(context.Context, string, string) (config.Effective, error) {
			return effective, nil
		}, func(context.Context, config.Effective) (provisioner.Bootstrapper, error) {
			return provider, nil
		}, func(context.Context, provisioner.Bootstrapper, config.Effective) (discovery.Result, error) {
			return discovery.Result{TargetID: "target"}, nil
		})
		runner.random = bytes.NewReader(make([]byte, 32))
		runner.now = func() time.Time { return now }
		return runner
	}
	first, err := newProcess().Run(t.Context(), Request{Account: "personal"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision != reconcile.DecisionCreate || first.Attempt == nil {
		t.Fatalf("fresh decision = %+v, want create with durable attempt", first)
	}
	got, err := store.Load("personal", "target")
	if err != nil {
		t.Fatal(err)
	}
	if got.AttemptID != first.Attempt.ID || got.RetryToken != first.Attempt.RetryToken || got.Lifecycle != state.Provisioning {
		t.Fatalf("persisted state = %+v, want returned attempt before handoff", got)
	}
	restarted, err := newProcess().Run(t.Context(), Request{Account: "personal"})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Decision != reconcile.DecisionRetrySameAttempt || restarted.Attempt == nil || restarted.Attempt.ID != first.Attempt.ID {
		t.Fatalf("restart decision = %+v, want retry original attempt", restarted)
	}
}

func TestRunnerRunsCapacityWatcherOnlyForCreateSafeDecision(t *testing.T) {
	t.Parallel()
	effective := config.Effective{Account: "personal", Region: "region", StateDir: t.TempDir()}
	provider := &fakeBootstrapper{run: func(context.Context) error { return nil }}
	calls := 0
	runner := NewRunner(slog.Default(), func(context.Context, string, string) (config.Effective, error) { return effective, nil }, func(context.Context, config.Effective) (provisioner.Bootstrapper, error) { return provider, nil }, func(context.Context, provisioner.Bootstrapper, config.Effective) (discovery.Result, error) {
		return discovery.Result{TargetID: "target", TenancyID: "root", AvailabilityDomains: []string{"AD-1"}}, nil
	}, func(ctx context.Context, gotProvider provisioner.Bootstrapper, gotEffective config.Effective, gotDiscovery discovery.Result) (capacity.Result, error) {
		calls++
		if gotProvider != provider || gotEffective.Account != "personal" || gotDiscovery.TargetID != "target" || ctx.Err() != nil {
			t.Fatalf("capacity inputs are wrong")
		}
		return capacity.Result{Kind: capacity.Available, AvailabilityDomain: "AD-1"}, nil
	})
	got, err := runner.Run(t.Context(), Request{Account: "personal"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got.Capacity != capacity.Available || got.AvailabilityDomain != "AD-1" {
		t.Fatalf("result=%+v calls=%d", got, calls)
	}
}

func (f *fakeBootstrapper) Validate(ctx context.Context) error {
	return f.run(ctx)
}

func TestRunner_Run(t *testing.T) {
	errConfig := errors.New("unknown account")
	errAuth := errors.New("bad credentials")
	errProvider := errors.New("service unavailable")
	errDiscovery := errors.New("discovery unavailable")
	tests := []struct {
		name         string
		loadErr      error
		authErr      error
		providerErr  error
		discoveryErr error
		wantErr      error
		wantPhase    string
		wantOrder    []string
	}{
		{name: "success", wantOrder: []string{"config", "auth", "bootstrap", "discovery"}},
		{name: "config failure", loadErr: errConfig, wantErr: errConfig, wantPhase: "config", wantOrder: []string{"config"}},
		{name: "auth failure", authErr: errAuth, wantErr: errAuth, wantPhase: "authentication", wantOrder: []string{"config", "auth"}},
		{name: "provider failure", providerErr: errProvider, wantErr: errProvider, wantPhase: "bootstrap", wantOrder: []string{"config", "auth", "bootstrap"}},
		{name: "discovery failure", discoveryErr: errDiscovery, wantErr: errDiscovery, wantPhase: "discovery", wantOrder: []string{"config", "auth", "bootstrap", "discovery"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var order []string
			provider := &fakeBootstrapper{run: func(context.Context) error {
				order = append(order, "bootstrap")
				return tt.providerErr
			}}
			var logs bytes.Buffer
			runner := NewRunner(slog.New(slog.NewTextHandler(&logs, nil)), func(_ context.Context, path, account string) (config.Effective, error) {
				order = append(order, "config")
				if path != "config.yaml" || account != "personal" {
					t.Fatalf("load(%q, %q)", path, account)
				}
				return config.Effective{Account: account, Region: "eu-frankfurt-1", StateDir: filepath.Join(t.TempDir(), "state")}, tt.loadErr
			}, func(_ context.Context, effective config.Effective) (provisioner.Bootstrapper, error) {
				order = append(order, "auth")
				if effective.Account != "personal" {
					t.Fatalf("authenticate account = %q", effective.Account)
				}
				return provider, tt.authErr
			}, func(context.Context, provisioner.Bootstrapper, config.Effective) (discovery.Result, error) {
				order = append(order, "discovery")
				return discovery.Result{TargetID: "target"}, tt.discoveryErr
			})

			result, err := runner.Run(t.Context(), Request{ConfigPath: "config.yaml", Account: "personal"})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Run() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				var appErr *Error
				if !errors.As(err, &appErr) || appErr.Phase != tt.wantPhase {
					t.Fatalf("Run() error = %#v, want app phase %q", err, tt.wantPhase)
				}
			}
			if !reflect.DeepEqual(order, tt.wantOrder) {
				t.Fatalf("call order = %v, want %v", order, tt.wantOrder)
			}
			if tt.wantErr == nil {
				if result.Account != "personal" || result.Region != "eu-frankfurt-1" || result.TargetID != "target" || result.Decision != reconcile.DecisionCreate || result.Attempt == nil {
					t.Errorf("result = %+v", result)
				}
				for _, message := range []string{"provisioning run started", "provider bootstrap started", "provider bootstrap completed", "provisioning run completed"} {
					if !strings.Contains(logs.String(), message) {
						t.Errorf("logs missing %q: %s", message, logs.String())
					}
				}
			}
		})
	}
}

func TestRunner_RunCancellation(t *testing.T) {
	t.Run("before run", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		loadCalls := 0
		runner := NewRunner(slog.Default(), func(context.Context, string, string) (config.Effective, error) {
			loadCalls++
			return config.Effective{}, nil
		}, nil, nil)
		if _, err := runner.Run(ctx, Request{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
		if loadCalls != 0 {
			t.Fatalf("load calls = %d", loadCalls)
		}
	})

	t.Run("during config", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		authCalls := 0
		runner := NewRunner(slog.Default(), func(loadCtx context.Context, _, _ string) (config.Effective, error) {
			cancel()
			if loadCtx != ctx {
				t.Fatal("load received different context")
			}
			return config.Effective{}, nil
		}, func(context.Context, config.Effective) (provisioner.Bootstrapper, error) {
			authCalls++
			return nil, nil
		}, nil)
		if _, err := runner.Run(ctx, Request{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
		if authCalls != 0 {
			t.Fatalf("auth calls = %d", authCalls)
		}
	})

	t.Run("during authentication", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		providerCalls := 0
		provider := &fakeBootstrapper{run: func(context.Context) error { providerCalls++; return nil }}
		runner := NewRunner(slog.Default(), func(context.Context, string, string) (config.Effective, error) {
			return config.Effective{}, nil
		}, func(authCtx context.Context, _ config.Effective) (provisioner.Bootstrapper, error) {
			cancel()
			if authCtx != ctx {
				t.Fatal("authenticate received different context")
			}
			return provider, nil
		}, nil)
		if _, err := runner.Run(ctx, Request{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
		if providerCalls != 0 {
			t.Fatalf("provider calls = %d", providerCalls)
		}
	})

	t.Run("during provider call", func(t *testing.T) {
		started := make(chan struct{})
		provider := &fakeBootstrapper{run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}}
		runner := NewRunner(slog.Default(), func(context.Context, string, string) (config.Effective, error) {
			return config.Effective{Account: "personal"}, nil
		}, func(context.Context, config.Effective) (provisioner.Bootstrapper, error) { return provider, nil }, nil)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		go func() { _, err := runner.Run(ctx, Request{Account: "personal"}); done <- err }()
		<-started
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	})
}
