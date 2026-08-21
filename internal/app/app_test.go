package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestRunnerRun(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	runner := NewRunner(slog.New(slog.NewTextHandler(&logs, nil)))

	result, err := runner.Run(t.Context())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != "ocihood\n" {
		t.Errorf("result = %q, want %q", result, "ocihood\n")
	}
	if !strings.Contains(logs.String(), "level=INFO msg=\"OCIHood started\"") {
		t.Errorf("logs = %q, want start message", logs.String())
	}
}

func TestRunnerRunCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	runner := NewRunner(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if _, err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}
