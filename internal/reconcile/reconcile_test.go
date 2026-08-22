package reconcile

import (
	"bytes"
	"testing"
	"time"
)

func TestTargetID(t *testing.T) {
	base := Target{Account: " Personal ", Region: " EU-FRANKFURT-1 ", CompartmentID: " compartment ", SubnetID: "subnet", ImageID: "image", Shape: " VM.Standard.A1.Flex ", OCPUs: 2, MemoryGB: 12, BootVolumeGB: 50, PublicIP: true}
	normalized := base
	normalized.Account, normalized.Region, normalized.CompartmentID, normalized.Shape = "personal", "eu-frankfurt-1", "compartment", "VM.Standard.A1.Flex"
	if base.ID() != normalized.ID() {
		t.Fatal("equivalent normalized targets produced different ids")
	}
	changed := normalized
	changed.MemoryGB++
	if normalized.ID() == changed.ID() {
		t.Fatal("different logical targets produced the same id")
	}
}

func TestOwnershipTags(t *testing.T) {
	tags := OwnershipTags("target", " Personal ")
	if !IsOwned(tags, "target", "personal") {
		t.Fatal("generated ownership tags did not match")
	}
	for _, key := range []string{ManagedTag, TargetIDTag, AccountTag} {
		t.Run("reject missing "+key, func(t *testing.T) {
			partial := OwnershipTags("target", "personal")
			delete(partial, key)
			if IsOwned(partial, "target", "personal") {
				t.Fatalf("ownership matched without %s", key)
			}
		})
	}
	spoof := OwnershipTags("other", "personal")
	if IsOwned(spoof, "target", "personal") {
		t.Fatal("ownership matched a different target")
	}
}

func TestNewAttempt(t *testing.T) {
	now := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	attempt, err := NewAttempt(bytes.NewReader(make([]byte, 32)), now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempt.ID) != 32 || len(attempt.RetryToken) != 64 || !attempt.ValidUntil.Equal(now.Add(time.Hour)) {
		t.Fatalf("unexpected attempt: %+v", attempt)
	}
}

func TestDecide(t *testing.T) {
	now := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	owned := func(id string, lifecycle Lifecycle) Instance {
		return Instance{ID: id, Lifecycle: lifecycle, Tags: OwnershipTags("target", "personal")}
	}
	validAttempt := &Attempt{ID: "attempt", RetryToken: "token", ValidUntil: now.Add(time.Hour)}
	tests := []struct {
		name             string
		input            Input
		expected         DecisionKind
		expectedInstance string
		expectedAttempt  *Attempt
	}{
		{name: "no state and no match", input: Input{}, expected: DecisionCreate},
		{name: "no state and one active match", input: Input{Instances: []Instance{owned("one", LifecycleActive)}}, expected: DecisionAlreadySatisfied, expectedInstance: "one"},
		{name: "state points to active match", input: Input{State: &State{TargetID: "target", InstanceID: "one"}, Instances: []Instance{owned("one", LifecycleActive)}}, expected: DecisionAlreadySatisfied, expectedInstance: "one"},
		{name: "state instance missing", input: Input{State: &State{TargetID: "target", InstanceID: "missing"}}, expected: DecisionNewAttemptSafe},
		{name: "ambiguous launch retries same attempt", input: Input{State: &State{TargetID: "target", Attempt: validAttempt}}, expected: DecisionRetrySameAttempt, expectedAttempt: validAttempt},
		{name: "expired attempt permits new attempt", input: Input{State: &State{TargetID: "target", Attempt: &Attempt{ValidUntil: now}}}, expected: DecisionNewAttemptSafe},
		{name: "terminated match does not satisfy", input: Input{Instances: []Instance{owned("old", LifecycleTerminated)}}, expected: DecisionCreate},
		{name: "multiple active matches conflict", input: Input{Instances: []Instance{owned("one", LifecycleActive), owned("two", LifecycleActive)}}, expected: DecisionConflict},
		{name: "similarly named unrelated instance ignored", input: Input{Instances: []Instance{{ID: "lookalike", Lifecycle: LifecycleActive, Tags: map[string]string{ManagedTag: "true"}}}}, expected: DecisionCreate},
		{name: "stale incompatible state conflicts", input: Input{State: &State{TargetID: "old"}}, expected: DecisionConflict},
		{name: "provider match repairs stale instance reference", input: Input{State: &State{TargetID: "target", InstanceID: "old"}, Instances: []Instance{owned("new", LifecycleActive)}}, expected: DecisionResumeReconcile, expectedInstance: "new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.TargetID, tt.input.Account, tt.input.Now = "target", "personal", now
			got := Decide(tt.input)
			if got.Kind != tt.expected || got.InstanceID != tt.expectedInstance || got.Attempt != tt.expectedAttempt {
				t.Fatalf("got %+v, want kind=%v instance=%q attempt=%p", got, tt.expected, tt.expectedInstance, tt.expectedAttempt)
			}
			if got.Reason == "" {
				t.Fatal("decision has no diagnostic reason")
			}
		})
	}
}
