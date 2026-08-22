package capacity

import (
	"testing"
	"time"

	localstate "github.com/MaksimSurmach/OCIHood/internal/state"
)

func TestStateStorePersistsWatcherProgressWithoutSecrets(t *testing.T) {
	now := time.Date(2026, 8, 22, 8, 0, 0, 0, time.UTC)
	store := localstate.New(t.TempDir())
	locked, err := store.TryLock("account", "target")
	if err != nil {
		t.Fatal(err)
	}
	if err := locked.Save(localstate.State{Account: "account", TargetID: "target", Lifecycle: localstate.Provisioning, AttemptID: "attempt", RetryToken: "token", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := locked.Close(); err != nil {
		t.Fatal(err)
	}
	adapter := StateStore{Store: store, Account: "account", Now: func() time.Time { return now.Add(time.Minute) }}
	progress := State{TargetID: "target", LastAD: "AD-2", RetryCount: 4, NextAttempt: now.Add(time.Hour), Status: Throttled}
	if err := adapter.Save(progress); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load("account", "target")
	if err != nil {
		t.Fatal(err)
	}
	if got.Lifecycle != localstate.Waiting || got.SelectedAD != "AD-2" || got.RetryCount != 4 || got.LastResult != string(Throttled) || got.AttemptID != "attempt" || got.RetryToken != "token" {
		t.Fatalf("state = %+v", got)
	}
	resumed, err := adapter.Load("target")
	if err != nil || resumed.LastAD != "AD-2" || resumed.NextAttempt != progress.NextAttempt {
		t.Fatalf("resume=%+v err=%v", resumed, err)
	}
	progress.Status = Available
	if err := adapter.Save(progress); err != nil {
		t.Fatal(err)
	}
	got, err = store.Load("account", "target")
	if err != nil || got.Lifecycle != localstate.Provisioning {
		t.Fatalf("available state=%+v err=%v", got, err)
	}
}
