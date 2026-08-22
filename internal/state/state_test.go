package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
)

func TestStoreRoundTripAndPermissions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 22, 6, 30, 0, 0, time.UTC)
	want := State{
		Account: "personal", Lifecycle: Provisioning, TargetID: "target-1",
		AttemptID: "attempt", RetryToken: "retry", AttemptValidTo: now.Add(time.Hour),
		LastAttempt: now, LastResult: "accepted", SelectedAD: "AD-2", RetryCount: 3,
		NextAttempt: now.Add(time.Minute), InstanceID: "instance", PublicIP: "192.0.2.1",
		LastError: "capacity unavailable", UpdatedAt: now,
	}
	store := New(t.TempDir())
	locked, err := store.TryLock(want.Account, want.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if err := locked.Save(want); err != nil {
		t.Fatal(err)
	}
	if err := locked.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(want.Account, want.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	want.SchemaVersion = SchemaVersion
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
	path, _ := store.path(want.Account, want.TargetID)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private_key", "telegram_token", "credential"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("serialized state contains secret field %q", secret)
		}
	}
}

func TestStoreRejectsMissingCorruptAndUnsupportedState(t *testing.T) {
	t.Parallel()
	store := New(t.TempDir())
	if _, err := store.Load("personal", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error = %v, want ErrNotFound", err)
	}
	for _, tt := range []struct {
		name string
		data string
		want string
	}{
		{name: "truncated", data: `{"schema_version":1`, want: "decode state"},
		{name: "unsupported", data: `{"schema_version":2,"account":"personal","lifecycle":"waiting","target_id":"target","updated_at":"2026-08-22T06:30:00Z"}`, want: "unsupported state schema version"},
		{name: "unknown field", data: `{"schema_version":1,"account":"personal","lifecycle":"waiting","target_id":"target","updated_at":"2026-08-22T06:30:00Z","secret":"x"}`, want: "decode state"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, _ := store.path("personal", "target")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load("personal", "target"); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestStoreLockingAndAtomicFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := New(root)
	first, err := store.TryLock("account", "target")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	if _, err := store.TryLock("account", "target"); err == nil || !strings.Contains(err.Error(), "another writer") {
		t.Fatalf("same-target lock error = %v", err)
	}
	independent, err := store.TryLock("account", "other")
	if err != nil {
		t.Fatalf("independent target lock: %v", err)
	}
	_ = independent.Close()
	otherAccount, err := store.TryLock("other-account", "target")
	if err != nil {
		t.Fatalf("independent account lock: %v", err)
	}
	_ = otherAccount.Close()

	now := time.Now().UTC()
	old := State{Account: "account", TargetID: "target", Lifecycle: Waiting, LastResult: "old", UpdatedAt: now}
	if err := first.Save(old); err != nil {
		t.Fatal(err)
	}
	store.rename = func(string, string) error { return errors.New("injected failure") }
	if err := first.Save(State{Account: "account", TargetID: "target", Lifecycle: Running, LastResult: "new", UpdatedAt: now}); err == nil {
		t.Fatal("Save() succeeded, want injected replacement failure")
	}
	got, err := New(root).Load("account", "target")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastResult != "old" {
		t.Fatalf("preserved result = %q, want old", got.LastResult)
	}
}

func TestReconcileStatePreservesRestartIdentity(t *testing.T) {
	t.Parallel()
	validTo := time.Now().Add(time.Hour)
	persisted := State{TargetID: "target", InstanceID: "stale", AttemptID: "attempt", RetryToken: "retry", AttemptValidTo: validTo}
	got := reconcile.Decide(reconcile.Input{
		TargetID: "target", Account: "account", State: persisted.ReconcileState(),
		Instances: []reconcile.Instance{{ID: "actual", Lifecycle: reconcile.LifecycleActive, Tags: reconcile.OwnershipTags("target", "account")}},
		Now:       time.Now(),
	})
	if got.Kind != reconcile.DecisionResumeReconcile || got.InstanceID != "actual" {
		t.Fatalf("Decide() = %+v, want provider reconciliation", got)
	}
}
