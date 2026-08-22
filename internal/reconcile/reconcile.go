// Package reconcile defines provider-independent target identity and reconciliation decisions.
package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	ManagedTag  = "ocihood.managed"
	TargetIDTag = "ocihood.target-id"
	AccountTag  = "ocihood.account"
)

// Target is the stable desired-instance identity. Runtime and retry data do not belong here.
type Target struct {
	Account       string `json:"account"`
	Region        string `json:"region"`
	CompartmentID string `json:"compartment_id"`
	SubnetID      string `json:"subnet_id"`
	ImageID       string `json:"image_id"`
	Shape         string `json:"shape"`
	OCPUs         int    `json:"ocpus"`
	MemoryGB      int    `json:"memory_gb"`
	BootVolumeGB  int    `json:"boot_volume_gb"`
	PublicIP      bool   `json:"public_ip"`
}

// ID returns the SHA-256 identity of the target's canonical JSON representation.
func (t Target) ID() string {
	t.Account = strings.ToLower(strings.TrimSpace(t.Account))
	t.Region = strings.ToLower(strings.TrimSpace(t.Region))
	t.CompartmentID = strings.TrimSpace(t.CompartmentID)
	t.SubnetID = strings.TrimSpace(t.SubnetID)
	t.ImageID = strings.TrimSpace(t.ImageID)
	t.Shape = strings.TrimSpace(t.Shape)
	b, _ := json.Marshal(t) // Target contains only JSON-safe value fields.
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// OwnershipTags returns the complete free-form tag contract for a managed instance.
func OwnershipTags(targetID, account string) map[string]string {
	return map[string]string{
		ManagedTag: "true", TargetIDTag: targetID,
		AccountTag: strings.ToLower(strings.TrimSpace(account)),
	}
}

// IsOwned reports whether tags exactly satisfy the OCIHood ownership contract.
func IsOwned(tags map[string]string, targetID, account string) bool {
	expected := OwnershipTags(targetID, account)
	return tags[ManagedTag] == expected[ManagedTag] &&
		tags[TargetIDTag] == expected[TargetIDTag] &&
		tags[AccountTag] == expected[AccountTag]
}

// Attempt identifies one logical launch and its OCI retry-token validity window.
type Attempt struct {
	ID         string
	RetryToken string
	ValidUntil time.Time
}

// NewAttempt creates one logical launch identity. Retry the same Attempt after ambiguous results.
func NewAttempt(random io.Reader, now time.Time, validity time.Duration) (Attempt, error) {
	if validity <= 0 {
		return Attempt{}, fmt.Errorf("attempt validity must be positive")
	}
	b := make([]byte, 32)
	if _, err := io.ReadFull(random, b); err != nil {
		return Attempt{}, fmt.Errorf("generate attempt identity: %w", err)
	}
	return Attempt{
		ID: hex.EncodeToString(b[:16]), RetryToken: hex.EncodeToString(b),
		ValidUntil: now.Add(validity),
	}, nil
}

// Lifecycle is the provider-observed instance lifecycle relevant to reconciliation.
type Lifecycle uint8

const (
	LifecycleUnknown Lifecycle = iota
	LifecycleActive
	LifecycleTerminated
)

// Instance is the provider observation needed by the pure decision layer.
type Instance struct {
	ID        string
	Lifecycle Lifecycle
	Tags      map[string]string
}

// State is the durable local state consumed by reconciliation.
type State struct {
	TargetID   string
	InstanceID string
	Attempt    *Attempt
}

// DecisionKind is the exact next action selected by reconciliation.
type DecisionKind uint8

const (
	DecisionUnknown DecisionKind = iota
	DecisionCreate
	DecisionAlreadySatisfied
	DecisionResumeReconcile
	DecisionRetrySameAttempt
	DecisionNewAttemptSafe
	DecisionConflict
)

// Decision is deterministic for the same input and time.
type Decision struct {
	Kind       DecisionKind
	InstanceID string
	Attempt    *Attempt
	Reason     string
}

// Input combines durable state with current provider observations.
type Input struct {
	TargetID  string
	Account   string
	State     *State
	Instances []Instance
	Now       time.Time
}

// Decide returns the fail-safe action for one desired target without provider I/O.
func Decide(in Input) Decision {
	if in.State != nil && in.State.TargetID != in.TargetID {
		return Decision{Kind: DecisionConflict, Reason: "local state belongs to a different target"}
	}

	active := make([]Instance, 0, 1)
	unknown := make([]Instance, 0, 1)
	for _, instance := range in.Instances {
		if !IsOwned(instance.Tags, in.TargetID, in.Account) {
			continue
		}
		switch instance.Lifecycle {
		case LifecycleActive:
			active = append(active, instance)
		case LifecycleUnknown:
			unknown = append(unknown, instance)
		}
	}
	if len(active)+len(unknown) > 1 {
		return Decision{Kind: DecisionConflict, Reason: fmt.Sprintf("found %d active or unknown managed instances for target", len(active)+len(unknown))}
	}
	if len(unknown) == 1 {
		return Decision{Kind: DecisionResumeReconcile, InstanceID: unknown[0].ID, Reason: "managed instance lifecycle is unknown"}
	}
	if len(active) == 1 {
		if in.State != nil && in.State.InstanceID != "" && in.State.InstanceID != active[0].ID {
			return Decision{Kind: DecisionResumeReconcile, InstanceID: active[0].ID, Reason: "provider instance differs from local state"}
		}
		return Decision{Kind: DecisionAlreadySatisfied, InstanceID: active[0].ID, Reason: "one active managed instance satisfies target"}
	}
	if in.State == nil {
		return Decision{Kind: DecisionCreate, Reason: "no local state or active managed instance"}
	}
	if in.State.Attempt != nil {
		if strings.TrimSpace(in.State.Attempt.ID) == "" || strings.TrimSpace(in.State.Attempt.RetryToken) == "" || in.State.Attempt.ValidUntil.IsZero() {
			return Decision{Kind: DecisionConflict, Reason: "local state contains an incomplete launch attempt"}
		}
		if in.Now.Before(in.State.Attempt.ValidUntil) {
			return Decision{Kind: DecisionRetrySameAttempt, Attempt: in.State.Attempt, Reason: "in-flight attempt remains retryable"}
		}
		return Decision{Kind: DecisionNewAttemptSafe, Reason: "previous attempt expired and reconciliation found no active instance"}
	}
	return Decision{Kind: DecisionNewAttemptSafe, Reason: "local instance is absent and reconciliation found no active instance"}
}
