package cli

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/app"
	"github.com/MaksimSurmach/OCIHood/internal/config"
	"github.com/MaksimSurmach/OCIHood/internal/discovery"
	"github.com/MaksimSurmach/OCIHood/internal/provisioner"
	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
	"github.com/MaksimSurmach/OCIHood/internal/state"
)

type fakeRunner struct {
	calls   int
	request app.Request
	result  app.Result
	plan    app.Plan
	err     error
	run     func(context.Context, app.Request) (app.Result, error)
}

func (f *fakeRunner) Plan(_ context.Context, request app.Request) (app.Plan, error) {
	f.calls++
	f.request = request
	return f.plan, f.err
}

func TestPlanCommandRendersDeterministicIntent(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{plan: app.Plan{Account: "personal", TargetID: "target", Region: "region", CompartmentID: "compartment", Shape: "shape", OCPUs: 2, MemoryGB: 12, ImageID: "image", VCNID: "vcn", SubnetID: "subnet", BootVolumeGB: 50, PublicIP: true, AvailabilityDomains: []string{"AD-1", "AD-2"}, Action: reconcile.DecisionCreate, Reason: "no active instance"}}
	var stdout, stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"--config", "config.yaml", "plan", "--account", "personal"}, runner, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"target_id: target", "availability_domains: AD-1,AD-2", "action: create", "reason: no active instance"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("output missing %q: %s", want, stdout.String())
		}
	}
}

type integrationBootstrapper struct{ calls int }

func (f *integrationBootstrapper) Validate(context.Context) error {
	f.calls++
	return nil
}

func TestStartCommandToProvisioner(t *testing.T) {
	t.Parallel()
	provider := &integrationBootstrapper{}
	runner := app.NewRunner(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), func(_ context.Context, path, account string) (config.Effective, error) {
		if path != "config.yaml" || account != "personal" {
			t.Fatalf("load(%q, %q)", path, account)
		}
		return config.Effective{Account: account, Region: "eu-frankfurt-1", StateDir: t.TempDir()}, nil
	}, func(context.Context, config.Effective) (provisioner.Bootstrapper, error) { return provider, nil }, func(context.Context, provisioner.Bootstrapper, config.Effective) (discovery.Result, error) {
		return discovery.Result{TargetID: "target"}, nil
	})
	var stdout, stderr bytes.Buffer

	if code := Execute(t.Context(), []string{"--config", "config.yaml", "start", "--account", "personal"}, runner, &stdout, &stderr); code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
}

func TestConfiglessFlagsReachSameEffectiveConfigAsFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	stateDir := filepath.Join(dir, "state")
	publicKey := filepath.Join(dir, "id.pub")
	if err := os.WriteFile(publicKey, []byte("ssh-ed25519 test"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "defaults:\n  state_dir: " + stateDir + "\naccounts:\n  personal:\n    oci_profile: TEST\n    region: eu-zurich-1\n    ssh_public_key_path: " + publicKey + "\n    compartment_id: compartment\n    image_id: image\n    subnet_id: subnet\n    overrides:\n      public_ip: false\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var effective []config.Effective
	provider := &integrationBootstrapper{}
	runner := app.NewRunner(slog.Default(), func(_ context.Context, path, account string) (config.Effective, error) {
		cfg, err := config.Load(path)
		if err != nil {
			return config.Effective{}, err
		}
		return cfg.Resolve(account)
	}, func(_ context.Context, got config.Effective) (provisioner.Bootstrapper, error) {
		effective = append(effective, got)
		return provider, nil
	}, func(context.Context, provisioner.Bootstrapper, config.Effective) (discovery.Result, error) {
		return discovery.Result{TargetID: "target"}, nil
	})

	commands := [][]string{
		{"--config", configPath, "start", "--account", "personal"},
		{"start", "--account", "personal", "--oci-profile", "TEST", "--region", "eu-zurich-1", "--ssh-public-key", publicKey, "--compartment-id", "compartment", "--image-id", "image", "--subnet-id", "subnet", "--state-dir", stateDir, "--public-ip=false"},
	}
	for _, args := range commands {
		var stdout, stderr bytes.Buffer
		if code := Execute(t.Context(), args, runner, &stdout, &stderr); code != 0 {
			t.Fatalf("Execute(%v) = %d, stderr %q", args, code, stderr.String())
		}
	}
	if len(effective) != 2 || !reflect.DeepEqual(effective[0], effective[1]) {
		t.Fatalf("effective configs differ: %#v / %#v", effective[0], effective[1])
	}
}

func TestDefaultConfigReceivesCLIOverrides(t *testing.T) {
	t.Parallel()
	key := filepath.Join(t.TempDir(), "id.pub")
	if err := os.WriteFile(key, []byte("ssh-ed25519 test"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &integrationBootstrapper{}
	runner := app.NewRunner(slog.Default(), func(_ context.Context, path, account string) (config.Effective, error) {
		if path != "" || account != "personal" {
			t.Fatalf("load(%q, %q)", path, account)
		}
		return config.Effective{Account: account, CompartmentID: "compartment", SSHPublicKeyPath: key, PublicIP: true, StateDir: t.TempDir()}, nil
	}, func(_ context.Context, got config.Effective) (provisioner.Bootstrapper, error) {
		if got.PublicIP {
			t.Fatal("default config did not receive CLI override")
		}
		return provider, nil
	}, func(context.Context, provisioner.Bootstrapper, config.Effective) (discovery.Result, error) {
		return discovery.Result{TargetID: "target"}, nil
	})
	var stdout, stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"start", "--account", "personal", "--public-ip=false"}, runner, &stdout, &stderr); code != 0 {
		t.Fatalf("Execute() = %d, stderr = %q", code, stderr.String())
	}
}

func TestConfiglessValidationAndHelp(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"start", "--help"}, runner, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	for _, want := range []string{"--public-ip", "--ssh-public-key", "Explicit flags override", "ocihood start --account"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help missing %q: %s", want, stdout.String())
		}
	}
}

func TestConfigShowAcceptsConfiglessOverridesWithoutReadingReferences(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	key := filepath.Join(dir, "private-key")
	secret := "SECRET-KEY-CONTENTS"
	if err := os.WriteFile(key, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"config", "show", "--account", "personal", "--ssh-private-key", key, "--ssh-public-key", "/key.pub", "--public-ip=false"}
	if code := Execute(t.Context(), args, &fakeRunner{}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), secret) || !strings.Contains(stdout.String(), "public_ip: false") || !strings.Contains(stdout.String(), key) {
		t.Fatalf("unsafe or incomplete output: %q", stdout.String())
	}
}

func TestConfigShowDefaultFileReceivesCLIOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "defaults:\n  shape: file-shape\n  public_ip: true\naccounts:\n  personal:\n    oci_profile: FILE\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"config", "show", "--account", "personal", "--public-ip=false"}, &fakeRunner{}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{"oci_profile: FILE", "shape: file-shape", "public_ip: false"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("output missing %q: %s", want, stdout.String())
		}
	}
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

func TestStatusCommandIsReadOnlyAndRendersLifecycles(t *testing.T) {
	t.Parallel()
	for _, lifecycle := range []state.Lifecycle{state.Discovered, state.Waiting, state.Provisioning, state.Running, state.Failed} {
		t.Run(string(lifecycle), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			stateDir := filepath.Join(dir, "state")
			contents := "defaults:\n  state_dir: " + stateDir + "\naccounts:\n  personal: {}\n"
			if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			store := state.New(stateDir)
			locked, err := store.TryLock("personal", "target")
			if err != nil {
				t.Fatal(err)
			}
			if err := locked.Save(state.State{Account: "personal", TargetID: "target", Lifecycle: lifecycle, UpdatedAt: time.Date(2026, 8, 22, 6, 30, 0, 0, time.UTC)}); err != nil {
				t.Fatal(err)
			}
			if err := locked.Close(); err != nil {
				t.Fatal(err)
			}
			matches, err := filepath.Glob(filepath.Join(stateDir, "*", "target.json"))
			if err != nil || len(matches) != 1 {
				t.Fatalf("state file matches = %v, error = %v", matches, err)
			}
			before, err := os.Stat(matches[0])
			if err != nil {
				t.Fatal(err)
			}
			runner := &fakeRunner{}
			var stdout, stderr bytes.Buffer
			if code := Execute(t.Context(), []string{"--config", configPath, "status", "--account", "personal"}, runner, &stdout, &stderr); code != 0 {
				t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), "status: "+string(lifecycle)) || !strings.Contains(stdout.String(), "target_id: target") {
				t.Fatalf("stdout = %q", stdout.String())
			}
			if runner.calls != 0 {
				t.Fatalf("runner calls = %d, want zero provider/application calls", runner.calls)
			}
			after, err := os.Stat(matches[0])
			if err != nil {
				t.Fatal(err)
			}
			if !before.ModTime().Equal(after.ModTime()) {
				t.Fatal("status mutated persisted state")
			}
		})
	}
}

func (f *fakeRunner) Run(ctx context.Context, request app.Request) (app.Result, error) {
	f.calls++
	f.request = request
	if f.run != nil {
		return f.run(ctx, request)
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

	runner := &fakeRunner{result: app.Result{Account: "personal", Region: "eu-frankfurt-1", InstanceID: "ocid.instance", InstanceState: "RUNNING", PublicIP: "203.0.113.1"}}
	var stdout, stderr bytes.Buffer

	if code := Execute(t.Context(), []string{"--config", "config.yaml", "start", "--account", "personal"}, runner, &stdout, &stderr); code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	if runner.calls != 1 {
		t.Errorf("runner calls = %d, want 1", runner.calls)
	}
	if runner.request != (app.Request{ConfigPath: "config.yaml", Account: "personal"}) {
		t.Errorf("request = %+v", runner.request)
	}
	if got := stdout.String(); got != "account personal provisioning complete (region eu-frankfurt-1, instance ocid.instance, state RUNNING, public_ip 203.0.113.1)\n" {
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

	if code := Execute(t.Context(), []string{"start", "--account", "personal"}, runner, &stdout, &stderr); code == 0 {
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
		{name: "unknown flag", args: []string{"start", "--account", "personal", "--missing"}, want: "unknown flag"},
		{name: "missing account", args: []string{"start"}, want: "required flag"},
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
	runner := &fakeRunner{run: func(ctx context.Context, _ app.Request) (app.Result, error) {
		close(started)
		<-ctx.Done()
		return app.Result{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int)

	go func() {
		done <- Execute(ctx, []string{"start", "--account", "personal"}, runner, &bytes.Buffer{}, &bytes.Buffer{})
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
