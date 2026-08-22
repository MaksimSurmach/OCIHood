package launch

import (
	"errors"
	"testing"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/state"
)

func TestStateStorePersistsAcceptedAndFinalTransitions(t *testing.T) {
	root := t.TempDir()
	store := state.New(root)
	locked, err := store.TryLock("account", "target")
	if err != nil {
		t.Fatal(err)
	}
	if err := locked.Save(state.State{Account: "account", TargetID: "target", Lifecycle: state.Provisioning, AttemptID: "attempt", RetryToken: "token", AttemptValidTo: time.Now().Add(time.Hour), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := locked.Close(); err != nil {
		t.Fatal(err)
	}
	runtime := StateStore{Store: store, Account: "account", TargetID: "target", Now: func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) }}
	if err := runtime.Accepted("instance"); err != nil {
		t.Fatal(err)
	}
	accepted, err := store.Load("account", "target")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.InstanceID != "instance" || accepted.Lifecycle != state.Provisioning || accepted.AttemptID != "attempt" {
		t.Fatalf("accepted=%+v", accepted)
	}
	if err := runtime.Finished(Instance{ID: "instance", State: "RUNNING", PublicIP: "203.0.113.1"}); err != nil {
		t.Fatal(err)
	}
	finished, err := store.Load("account", "target")
	if err != nil {
		t.Fatal(err)
	}
	if finished.Lifecycle != state.Running || finished.InstanceID != "instance" || finished.PublicIP != "203.0.113.1" || finished.LastResult != "RUNNING" || finished.LastError != "" {
		t.Fatalf("finished=%+v", finished)
	}
	if err := runtime.Failed(errors.New("fatal request")); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Load("account", "target")
	if err != nil {
		t.Fatal(err)
	}
	if failed.Lifecycle != state.Failed || failed.LastError != "fatal request" {
		t.Fatalf("failed=%+v", failed)
	}
}
