package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls  int
	result string
	err    error
	run    func(context.Context) (string, error)
}

func TestConfigCommands(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("accounts:\n  test:\n    oci_profile: TEST\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "validate", args: []string{"--config", path, "config", "validate"}},
		{name: "show", args: []string{"config", "show", "--config", path, "--account", "test"}, want: "oci_profile: TEST\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeRunner{}
			var stdout, stderr bytes.Buffer
			if code := Execute(t.Context(), tt.args, runner, &stdout, &stderr); code != 0 {
				t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.want) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), tt.want)
			}
			if runner.calls != 0 {
				t.Fatalf("runner calls = %d, want 0", runner.calls)
			}
		})
	}
}

func (f *fakeRunner) Run(ctx context.Context) (string, error) {
	f.calls++
	if f.run != nil {
		return f.run(ctx)
	}
	return f.result, f.err
}

func TestHelpDoesNotRunApplication(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "root help", args: []string{"--help"}, want: "Available Commands:"},
		{name: "start help", args: []string{"start", "--help"}, want: "Start one provisioning run"},
		{name: "bare root", want: "Available Commands:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeRunner{}
			var stdout, stderr bytes.Buffer

			if code := Execute(t.Context(), tt.args, runner, &stdout, &stderr); code != 0 {
				t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.want) {
				t.Errorf("stdout = %q, want substring %q", stdout.String(), tt.want)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
			if runner.calls != 0 {
				t.Errorf("runner calls = %d, want 0", runner.calls)
			}
		})
	}
}

func TestStartSuccessWritesResultToStdout(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{result: "created instance\n"}
	var stdout, stderr bytes.Buffer

	if code := Execute(t.Context(), []string{"start"}, runner, &stdout, &stderr); code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	if runner.calls != 1 {
		t.Errorf("runner calls = %d, want 1", runner.calls)
	}
	if got := stdout.String(); got != "created instance\n" {
		t.Errorf("stdout = %q, want result", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestStartFailureWritesDiagnosticToStderr(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{err: errors.New("service unavailable")}
	var stdout, stderr bytes.Buffer

	if code := Execute(t.Context(), []string{"start"}, runner, &stdout, &stderr); code == 0 {
		t.Fatal("Execute() code = 0, want non-zero")
	}
	if runner.calls != 1 {
		t.Errorf("runner calls = %d, want 1", runner.calls)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "start provisioning run: service unavailable") {
		t.Errorf("stderr = %q, want useful diagnostic", got)
	}
}

func TestInvalidInputDoesNotRunApplication(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown command", args: []string{"missing"}, want: "unknown command"},
		{name: "unknown flag", args: []string{"start", "--missing"}, want: "unknown flag"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeRunner{}
			var stdout, stderr bytes.Buffer

			if code := Execute(t.Context(), tt.args, runner, &stdout, &stderr); code == 0 {
				t.Fatal("Execute() code = 0, want non-zero")
			}
			if runner.calls != 0 {
				t.Errorf("runner calls = %d, want 0", runner.calls)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if got := stderr.String(); !strings.Contains(got, tt.want) {
				t.Errorf("stderr = %q, want substring %q", got, tt.want)
			}
		})
	}
}

func TestStartPropagatesCancellation(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	runner := &fakeRunner{run: func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int)

	go func() {
		done <- Execute(ctx, []string{"start"}, runner, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	<-started
	cancel()

	if code := <-done; code == 0 {
		t.Fatal("Execute() code = 0, want non-zero after cancellation")
	}
	if runner.calls != 1 {
		t.Errorf("runner calls = %d, want 1", runner.calls)
	}
}
