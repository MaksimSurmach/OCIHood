package breaker

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestCircuitOpenRecoveryAndLogs(t *testing.T) {
	var logs bytes.Buffer
	circuit, err := New("account/region/compute", Config{Failures: 2, HalfOpenRequests: 1, Interval: time.Minute, OpenTimeout: time.Second}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	providerErr := errors.New("service unavailable")
	for range 2 {
		if err := circuit.Do(func() error { return Failure(providerErr) }); !errors.Is(err, providerErr) {
			t.Fatalf("failure = %v", err)
		}
	}
	called := false
	var open *OpenError
	if err := circuit.Do(func() error { called = true; return nil }); !errors.As(err, &open) || called {
		t.Fatalf("open err=%v called=%v", err, called)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := circuit.Do(func() error { called = true; return nil }); err != nil || !called {
		t.Fatalf("recovery err=%v called=%v", err, called)
	}
	if got := logs.String(); !strings.Contains(got, "status=open") || !strings.Contains(got, "status=half-open") || !strings.Contains(got, "status=closed") {
		t.Fatalf("missing transitions: %s", got)
	}
}

func TestCircuitExcludedAndFailedHalfOpen(t *testing.T) {
	circuit, err := New("test", Config{Failures: 1, HalfOpenRequests: 1, Interval: time.Minute, OpenTimeout: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := circuit.Do(func() error { return Exclude(nil) }); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("timeout")
	if err := circuit.Do(func() error { return Failure(failure) }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := circuit.Do(func() error { return Failure(failure) }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	var open *OpenError
	if err := circuit.Do(func() error { return nil }); !errors.As(err, &open) {
		t.Fatalf("failed probe did not reopen: %v", err)
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	_, err := New("test", Config{Failures: 0, HalfOpenRequests: 1, Interval: time.Second, OpenTimeout: time.Second}, nil)
	if err == nil {
		t.Fatal("expected validation error")
	}
}
