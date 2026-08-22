// Package state persists one OCIHood target's local provisioning state.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
	"github.com/gofrs/flock"
)

const SchemaVersion = 1

var ErrNotFound = errors.New("no local state")

type Lifecycle string

const (
	Discovered   Lifecycle = "discovered"
	Waiting      Lifecycle = "waiting"
	Provisioning Lifecycle = "provisioning"
	Running      Lifecycle = "running"
	Failed       Lifecycle = "failed"
)

// State is the complete durable record for one account and target.
type State struct {
	SchemaVersion  int       `json:"schema_version"`
	Account        string    `json:"account"`
	Lifecycle      Lifecycle `json:"lifecycle"`
	TargetID       string    `json:"target_id"`
	AttemptID      string    `json:"attempt_id,omitempty"`
	RetryToken     string    `json:"retry_token,omitempty"`
	AttemptValidTo time.Time `json:"attempt_valid_to,omitempty"`
	LastAttempt    time.Time `json:"last_attempt,omitempty"`
	LastResult     string    `json:"last_result,omitempty"`
	SelectedAD     string    `json:"selected_ad,omitempty"`
	RetryCount     int       `json:"retry_count,omitempty"`
	NextAttempt    time.Time `json:"next_attempt,omitempty"`
	InstanceID     string    `json:"instance_id,omitempty"`
	PublicIP       string    `json:"public_ip,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (s State) ReconcileState() *reconcile.State {
	r := &reconcile.State{TargetID: s.TargetID, InstanceID: s.InstanceID}
	if s.AttemptID != "" || s.RetryToken != "" || !s.AttemptValidTo.IsZero() {
		r.Attempt = &reconcile.Attempt{ID: s.AttemptID, RetryToken: s.RetryToken, ValidUntil: s.AttemptValidTo}
	}
	return r
}

type Store struct {
	root   string
	rename func(string, string) error
}

func New(root string) *Store { return &Store{root: root, rename: os.Rename} }

// Locked is the exclusive same-host writer for one account and target.
type Locked struct {
	store   *Store
	account string
	target  string
	lock    *flock.Flock
	mu      sync.Mutex
	closed  bool
}

// Guard excludes concurrent same-host provisioning runs for one account and target.
type Guard struct{ lock *flock.Flock }

func (s *Store) TryRunLock(account, targetID string) (*Guard, error) {
	path, err := s.path(account, targetID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	lock := flock.New(path + ".run.lock")
	ok, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock provisioning run: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("provisioning run for account %q target %s is already active", account, targetID)
	}
	if err := os.Chmod(path+".run.lock", 0o600); err != nil {
		_ = lock.Unlock()
		return nil, fmt.Errorf("restrict provisioning lock permissions: %w", err)
	}
	return &Guard{lock: lock}, nil
}

func (g *Guard) Close() error { return g.lock.Unlock() }

func (s *Store) TryLock(account, targetID string) (*Locked, error) {
	path, err := s.path(account, targetID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	lock := flock.New(path + ".lock")
	ok, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock state: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("state for account %q target %s is locked by another writer", account, targetID)
	}
	if err := os.Chmod(path+".lock", 0o600); err != nil {
		_ = lock.Unlock()
		return nil, fmt.Errorf("restrict state lock permissions: %w", err)
	}
	return &Locked{store: s, account: account, target: targetID, lock: lock}, nil
}

func (l *Locked) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.lock.Unlock()
}

func (l *Locked) Save(value State) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || !l.lock.Locked() {
		return errors.New("state writer no longer owns the lock")
	}
	if value.Account != l.account || value.TargetID != l.target {
		return errors.New("state identity does not match locked account and target")
	}
	value.SchemaVersion = SchemaVersion
	if err := validate(value); err != nil {
		return err
	}
	path, _ := l.store.path(l.account, l.target)
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restrict temporary state permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary state: %w", err)
	}
	if err := l.store.rename(tmpName, path); err != nil {
		return fmt.Errorf("replace state atomically: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open state directory for sync: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return nil
}

func (s *Store) Load(account, targetID string) (State, error) {
	path, err := s.path(account, targetID)
	if err != nil {
		return State{}, err
	}
	return load(path, account, targetID)
}

// LoadAccount loads the sole target state for account and fails on ambiguity.
func (s *Store) LoadAccount(account string) (State, error) {
	dir := filepath.Join(s.root, accountKey(account))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, ErrNotFound
	}
	if err != nil {
		return State{}, fmt.Errorf("list account state: %w", err)
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	if len(paths) == 0 {
		return State{}, ErrNotFound
	}
	if len(paths) > 1 {
		return State{}, fmt.Errorf("account %q has %d target states; select a target explicitly", account, len(paths))
	}
	return load(paths[0], account, strings.TrimSuffix(filepath.Base(paths[0]), ".json"))
}

func load(path, account, targetID string) (State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, ErrNotFound
	}
	if err != nil {
		return State{}, fmt.Errorf("read state: %w", err)
	}
	var value State
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return State{}, fmt.Errorf("decode state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return State{}, errors.New("decode state: trailing data")
	}
	if value.SchemaVersion != SchemaVersion {
		return State{}, fmt.Errorf("unsupported state schema version %d (want %d)", value.SchemaVersion, SchemaVersion)
	}
	if value.Account != account || value.TargetID != targetID {
		return State{}, errors.New("state identity does not match its path")
	}
	if err := validate(value); err != nil {
		return State{}, err
	}
	return value, nil
}

func validate(s State) error {
	if strings.TrimSpace(s.Account) == "" || strings.TrimSpace(s.TargetID) == "" {
		return errors.New("state account and target_id are required")
	}
	switch s.Lifecycle {
	case Discovered, Waiting, Provisioning, Running, Failed:
	default:
		return fmt.Errorf("unsupported state lifecycle %q", s.Lifecycle)
	}
	if s.UpdatedAt.IsZero() {
		return errors.New("state updated_at is required")
	}
	return nil
}

func (s *Store) path(account, targetID string) (string, error) {
	if strings.TrimSpace(account) == "" || strings.TrimSpace(targetID) == "" || strings.ContainsAny(targetID, `/\\`) || targetID == "." || targetID == ".." {
		return "", errors.New("account and safe target ID are required")
	}
	return filepath.Join(s.root, accountKey(account), targetID+".json"), nil
}

func accountKey(account string) string {
	sum := sha256.Sum256([]byte(account))
	return hex.EncodeToString(sum[:16])
}
