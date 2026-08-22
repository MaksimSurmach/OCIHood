package capacity

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"

	localstate "github.com/MaksimSurmach/OCIHood/internal/state"
)

type TimerSleeper struct{}

func (TimerSleeper) Sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type CryptoRandom struct{}

func (CryptoRandom) Float64() float64 {
	var data [8]byte
	if _, err := rand.Read(data[:]); err != nil {
		return .5
	}
	return float64(binary.LittleEndian.Uint64(data[:])>>11) / (1 << 53)
}

// StateStore maps watcher progress onto the existing atomic per-target state record.
type StateStore struct {
	Store   *localstate.Store
	Account string
	Now     func() time.Time
}

func (s StateStore) Load(targetID string) (State, error) {
	value, err := s.Store.Load(s.Account, targetID)
	if err != nil {
		return State{}, err
	}
	return State{TargetID: value.TargetID, LastAD: value.SelectedAD, RetryCount: value.RetryCount, NextAttempt: value.NextAttempt, Status: Kind(value.LastResult)}, nil
}

func (s StateStore) Save(progress State) error {
	locked, err := s.Store.TryLock(s.Account, progress.TargetID)
	if err != nil {
		return err
	}
	defer func() { _ = locked.Close() }()
	value, err := s.Store.Load(s.Account, progress.TargetID)
	if err != nil {
		return fmt.Errorf("load provisioning state: %w", err)
	}
	switch progress.Status {
	case Available, ProbeUnavailable:
		value.Lifecycle = localstate.Provisioning
	case Fatal:
		value.Lifecycle = localstate.Failed
	default:
		value.Lifecycle = localstate.Waiting
	}
	value.SelectedAD, value.RetryCount = progress.LastAD, progress.RetryCount
	value.NextAttempt, value.LastResult, value.UpdatedAt = progress.NextAttempt, string(progress.Status), s.Now().UTC()
	return locked.Save(value)
}

var _ Sleeper = TimerSleeper{}
var _ Random = CryptoRandom{}
var _ Store = StateStore{}
