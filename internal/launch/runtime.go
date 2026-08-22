package launch

import (
	"context"
	"time"

	localstate "github.com/MaksimSurmach/OCIHood/internal/state"
)

type TimerSleeper struct{}

func (TimerSleeper) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type StateStore struct {
	Store             *localstate.Store
	Account, TargetID string
	Now               func() time.Time
}

func (s StateStore) update(fn func(*localstate.State)) error {
	locked, err := s.Store.TryLock(s.Account, s.TargetID)
	if err != nil {
		return err
	}
	defer func() { _ = locked.Close() }()
	value, err := s.Store.Load(s.Account, s.TargetID)
	if err != nil {
		return err
	}
	fn(&value)
	value.UpdatedAt = s.Now().UTC()
	return locked.Save(value)
}
func (s StateStore) Accepted(id string) error {
	return s.update(func(v *localstate.State) {
		v.Lifecycle = localstate.Provisioning
		v.InstanceID = id
		v.LastResult = "launch accepted"
	})
}
func (s StateStore) Finished(i Instance) error {
	return s.update(func(v *localstate.State) {
		v.Lifecycle = localstate.Running
		v.InstanceID, v.PublicIP, v.LastResult, v.LastError = i.ID, i.PublicIP, i.State, ""
	})
}
func (s StateStore) Failed(err error) error {
	return s.update(func(v *localstate.State) {
		v.Lifecycle = localstate.Failed
		if err != nil {
			v.LastError = err.Error()
		}
	})
}
