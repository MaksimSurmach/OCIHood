package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunSeparatesResultAndDiagnostics(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if err := Run(&stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := stdout.String(), "ocihood\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got := stderr.String(); !strings.Contains(got, "level=INFO msg=\"OCIHood started\"") {
		t.Errorf("stderr = %q, want text slog message", got)
	}
}
