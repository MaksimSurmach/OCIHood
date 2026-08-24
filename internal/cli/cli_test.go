package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/app"
	"github.com/MaksimSurmach/OCIHood/internal/capacity"
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
	images  app.ImageList
	err     error
	run     func(context.Context, app.Request) (app.Result, error)
}

func (f *fakeRunner) Plan(_ context.Context, request app.Request) (app.Plan, error) {
	f.calls++
	f.request = request
	return f.plan, f.err
}

func (f *fakeRunner) Images(_ context.Context, request app.Request) (app.ImageList, error) {
	f.calls++
	f.request = request
	return f.images, f.err
}

func TestImagesListRendersTextAndJSON(t *testing.T) {
	created := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	runner := &fakeRunner{images: app.ImageList{Account: "personal", Region: "eu-paris-1", Shape: "VM.Standard.A1.Flex", Images: []discovery.Image{{ID: "image-1", Name: "Oracle-Linux-9", OperatingSystem: "Oracle Linux", OSVersion: "9", CreatedAt: created}}}}
	var textOutput, jsonOutput, stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"--config", "config.yaml", "images", "list", "--account", "personal"}, runner, &textOutput, &stderr); code != 0 {
		t.Fatalf("text code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"ID", "image-1", "Oracle-Linux-9", "Oracle Linux", "2026-08-14T12:00:00Z"} {
		if !strings.Contains(textOutput.String(), want) {
			t.Fatalf("text output missing %q: %s", want, textOutput.String())
		}
	}
	stderr.Reset()
	if code := Execute(t.Context(), []string{"images", "list", "--account", "personal", "--oci-profile", "DEFAULT", "--compartment-id", "compartment", "--shape", "shape", "--output=json"}, runner, &jsonOutput, &stderr); code != 0 {
		t.Fatalf("JSON code=%d stderr=%q", code, stderr.String())
	}
	var document imageListOutputDocument
	if err := json.Unmarshal(jsonOutput.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Schema != imageListSchema || document.Region != "eu-paris-1" || len(document.Images) != 1 || document.Images[0].ID != "image-1" {
		t.Fatalf("document=%+v", document)
	}
	if runner.request.Account != "personal" || !runner.request.Configless || runner.request.Overrides.OCIProfile == nil || *runner.request.Overrides.OCIProfile != "DEFAULT" || runner.request.Overrides.CompartmentID == nil || *runner.request.Overrides.CompartmentID != "compartment" || runner.request.Overrides.Settings.Shape == nil || *runner.request.Overrides.Settings.Shape != "shape" {
		t.Fatalf("request=%+v", runner.request)
	}
}

func TestPlanCommandRendersDeterministicIntent(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{plan: app.Plan{Account: "personal", TargetID: "target", Region: "region", CompartmentID: "compartment", Shape: "shape", ShapeArchitecture: "aarch64", OCPUs: 2, MemoryGB: 12, ImageID: "image", ImageName: "Ubuntu-24.04-aarch64", OperatingSystem: "Canonical Ubuntu", OSVersion: "24.04", VCNID: "vcn", SubnetID: "subnet", BootVolumeGB: 50, PublicIP: true, Policy: config.PolicyDecision{Violations: []string{"ocpus 2 exceeds maximum 1"}}, AvailabilityDomains: []string{"AD-1", "AD-2"}, Action: reconcile.DecisionCreate, Reason: "no active instance"}}
	var stdout, stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"--config", "config.yaml", "plan", "--account", "personal"}, runner, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"target_id: target", "shape_architecture: aarch64", "image_name: Ubuntu-24.04-aarch64", "os_version: 24.04", "policy_decision: rejected", "policy_violations: ocpus 2 exceeds maximum 1", "availability_domains: AD-1,AD-2", "action: create", "reason: no active instance"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("output missing %q: %s", want, stdout.String())
		}
	}
}

func TestPlanCommandRendersJSON(t *testing.T) {
	plan := app.Plan{Account: "personal", TargetID: "target", Shape: "VM.Standard.A1.Flex", ShapeArchitecture: "aarch64", OCPUs: 2, MemoryGB: 24, BootVolumeGB: 100, PublicIP: true, ImageID: "image", ImageName: "Ubuntu-24.04-aarch64", OperatingSystem: "Canonical Ubuntu", OSVersion: "24.04", Action: reconcile.DecisionCreate}
	runner := &fakeRunner{plan: plan}
	var textOutput, jsonOutput, stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"plan", "--account", "personal"}, runner, &textOutput, &stderr); code != 0 {
		t.Fatalf("text code=%d stderr=%q", code, stderr.String())
	}
	stderr.Reset()
	if code := Execute(t.Context(), []string{"plan", "--account", "personal", "--output=json"}, runner, &jsonOutput, &stderr); code != 0 {
		t.Fatalf("json code=%d stderr=%q", code, stderr.String())
	}
	var document planOutputDocument
	if err := json.Unmarshal(jsonOutput.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	parity := map[string]string{
		"shape": document.Shape, "shape_architecture": document.ShapeArchitecture,
		"ocpus": fmt.Sprint(document.OCPUs), "memory_gb": fmt.Sprint(document.MemoryGB), "boot_volume_gb": fmt.Sprint(document.BootVolumeGB), "public_ip": fmt.Sprint(document.PublicIP),
		"image_id": document.ImageID, "image_name": document.ImageName, "operating_system": document.OperatingSystem, "os_version": document.OSVersion,
	}
	expected := map[string]string{
		"shape": plan.Shape, "shape_architecture": plan.ShapeArchitecture,
		"ocpus": "2", "memory_gb": "24", "boot_volume_gb": "100", "public_ip": "true",
		"image_id": plan.ImageID, "image_name": plan.ImageName, "operating_system": plan.OperatingSystem, "os_version": plan.OSVersion,
	}
	for field, value := range parity {
		if value != expected[field] {
			t.Errorf("JSON %s = %q, want %q", field, value, expected[field])
		}
		if !strings.Contains(textOutput.String(), field+": "+value+"\n") {
			t.Errorf("text/json mismatch for %s: text=%q json=%q", field, textOutput.String(), value)
		}
	}
	if document.Schema != planSchema {
		t.Fatalf("schema=%q", document.Schema)
	}
}

func TestStartJSONExposesSanitizedPolicyRejection(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{run: func(context.Context, app.Request) (app.Result, error) {
		return app.Result{Account: "personal", Region: "region", Policy: config.PolicyDecision{Violations: []string{"ocpus 8 exceeds maximum 2"}}}, &app.Error{Phase: "policy", Err: errors.New("resource policy rejected")}
	}}
	var stdout, stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"start", "--account", "personal", "--output=json"}, runner, &stdout, &stderr); code != 1 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var document commandDocument
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Policy == nil || document.Policy.Allowed || !reflect.DeepEqual(document.Policy.Violations, []string{"ocpus 8 exceeds maximum 2"}) || document.Error.Message != "provisioning failed during policy" || strings.Contains(stdout.String(), "resource policy rejected") {
		t.Fatalf("document=%+v stdout=%q", document, stdout.String())
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
		{name: "images help", args: []string{"images", "--help"}, want: "Inspect current OCI images"},
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
	if got := stdout.String(); got != "account personal provisioning success (region eu-frankfurt-1, instance ocid.instance, state RUNNING, public_ip 203.0.113.1)\n" {
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
	if !strings.Contains(stdout.String(), "provisioning failed (fatal: provisioning failed)") {
		t.Errorf("stdout = %q, want sanitized result", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "Error: provisioning failed") || strings.Contains(got, "service unavailable") {
		t.Errorf("stderr = %q, want useful diagnostic", got)
	}
}

func TestStartExecutionModesAndOutput(t *testing.T) {
	t.Parallel()
	t.Run("once reaches runner", func(t *testing.T) {
		runner := &fakeRunner{result: app.Result{Account: "personal", Capacity: capacity.Unavailable}}
		var stdout, stderr bytes.Buffer
		if code := Execute(t.Context(), []string{"start", "--account", "personal", "--once"}, runner, &stdout, &stderr); code != 3 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		if !runner.request.Once || !strings.Contains(stdout.String(), "no_capacity") || !strings.Contains(stderr.String(), "no capacity available") {
			t.Fatalf("request=%+v stdout=%q stderr=%q", runner.request, stdout.String(), stderr.String())
		}
	})

	t.Run("json result and json logs stay separated", func(t *testing.T) {
		runner := &loggingRunner{result: app.Result{Account: "personal", TargetID: "target", Region: "region", InstanceID: "instance", InstanceState: "RUNNING"}}
		var stdout, stderr bytes.Buffer
		if code := Execute(t.Context(), []string{"start", "--account", "personal", "--output=json", "--log-format=json", "--log-level=debug"}, runner, &stdout, &stderr); code != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		var result commandDocument
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("stdout is not JSON: %v: %q", err, stdout.String())
		}
		if result.Schema != resultSchema || result.TargetID != "target" || result.Outcome != "success" {
			t.Fatalf("result=%+v", result)
		}
		var diagnostic map[string]any
		if err := json.Unmarshal(stderr.Bytes(), &diagnostic); err != nil || diagnostic["msg"] != "diagnostic" {
			t.Fatalf("stderr is not JSON diagnostics: %v: %q", err, stderr.String())
		}
	})

	t.Run("log and result formats are independent", func(t *testing.T) {
		for _, tt := range []struct {
			name, output, logFormat string
		}{
			{name: "text text", output: "text", logFormat: "text"},
			{name: "text json", output: "text", logFormat: "json"},
			{name: "json text", output: "json", logFormat: "text"},
			{name: "json json", output: "json", logFormat: "json"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				runner := &loggingRunner{result: app.Result{Account: "personal", Decision: reconcile.DecisionAlreadySatisfied, InstanceID: "instance-existing"}}
				var stdout, stderr bytes.Buffer
				args := []string{"start", "--account", "personal", "--log-level=debug", "--output=" + tt.output, "--log-format=" + tt.logFormat}
				if code := Execute(t.Context(), args, runner, &stdout, &stderr); code != 0 {
					t.Fatalf("code=%d stderr=%q", code, stderr.String())
				}
				if tt.output == "json" {
					var result commandDocument
					if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Outcome != "already_satisfied" || result.InstanceID != "instance-existing" {
						t.Fatalf("result=%+v err=%v stdout=%q", result, err, stdout.String())
					}
				} else if !strings.Contains(stdout.String(), "already_satisfied") || !strings.Contains(stdout.String(), "instance-existing") {
					t.Fatalf("stdout=%q", stdout.String())
				}
				if tt.logFormat == "json" {
					var diagnostic map[string]any
					if err := json.Unmarshal(stderr.Bytes(), &diagnostic); err != nil {
						t.Fatalf("stderr is not JSON: %v: %q", err, stderr.String())
					}
				} else if !strings.Contains(stderr.String(), "msg=diagnostic") {
					t.Fatalf("stderr=%q", stderr.String())
				}
			})
		}
	})

	t.Run("max runtime returns deadline result", func(t *testing.T) {
		runner := &fakeRunner{run: func(ctx context.Context, _ app.Request) (app.Result, error) {
			<-ctx.Done()
			return app.Result{}, ctx.Err()
		}}
		var stdout, stderr bytes.Buffer
		if code := Execute(t.Context(), []string{"start", "--account", "personal", "--max-runtime=1ns"}, runner, &stdout, &stderr); code != 124 || !strings.Contains(stdout.String(), "deadline_exceeded") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	})
}

type loggingRunner struct {
	logger *slog.Logger
	result app.Result
}

func (r *loggingRunner) SetLogger(logger *slog.Logger) { r.logger = logger }
func (r *loggingRunner) Run(context.Context, app.Request) (app.Result, error) {
	r.logger.Debug("diagnostic")
	return r.result, nil
}
func (*loggingRunner) Plan(context.Context, app.Request) (app.Plan, error) { return app.Plan{}, nil }
func (*loggingRunner) Images(context.Context, app.Request) (app.ImageList, error) {
	return app.ImageList{}, nil
}

func TestStartExitCodesAndSecretRedaction(t *testing.T) {
	t.Parallel()
	secret := "super-secret-token"
	tests := []struct {
		name string
		err  error
		code int
		want string
	}{
		{name: "fatal", err: &app.Error{Phase: "authentication", Err: errors.New(secret)}, code: 1, want: "authentication"},
		{name: "transient", err: &app.Error{Phase: "capacity", Err: &capacity.Error{Kind: capacity.Transient, Err: errors.New(secret)}}, code: 4, want: "transient"},
		{name: "canceled", err: context.Canceled, code: 130, want: "canceled"},
		{name: "deadline", err: context.DeadlineExceeded, code: 124, want: "deadline"},
		{name: "missing image ID", err: &app.Error{Phase: "discovery", Err: &discovery.Error{Kind: discovery.KindInvalid, Stage: "image selection", Err: errors.New("image_id is required; run `ocihood images list` and check the value")}}, code: 2, want: "ocihood images list"},
		{name: "unknown image ID", err: &app.Error{Phase: "discovery", Err: &discovery.Error{Kind: discovery.KindNotFound, Stage: "image selection", Err: errors.New("image_id \"missing\" was not found; run `ocihood images list` and check the value")}}, code: 1, want: "was not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute(t.Context(), []string{"start", "--account", "personal", "--output=json"}, &fakeRunner{err: tt.err}, &stdout, &stderr)
			if code != tt.code || !strings.Contains(stdout.String(), tt.want) || strings.Contains(stdout.String()+stderr.String(), secret) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestInvalidExecutionModesDoNotRunApplication(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"start", "--account", "personal", "--output=yaml"},
		{"start", "--account", "personal", "--log-format=yaml"},
		{"start", "--account", "personal", "--log-level=noisy"},
		{"start", "--account", "personal", "--max-runtime=-1s"},
		{"images", "list", "--account", "personal", "--output=yaml"},
	} {
		runner := &fakeRunner{}
		if code := Execute(t.Context(), args, runner, &bytes.Buffer{}, &bytes.Buffer{}); code == 0 || runner.calls != 0 {
			t.Fatalf("args=%v code=%d calls=%d", args, code, runner.calls)
		}
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
		{name: "removed image name", args: []string{"start", "--account", "personal", "--image-name", "Oracle Linux"}, want: "unknown flag"},
		{name: "removed operating system", args: []string{"plan", "--account", "personal", "--operating-system", "Oracle Linux"}, want: "unknown flag"},
		{name: "removed OS version", args: []string{"plan", "--account", "personal", "--os-version", "9"}, want: "unknown flag"},
		{name: "missing account", args: []string{"start"}, want: "required flag"},
		{name: "missing images account", args: []string{"images", "list"}, want: "required flag"},
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
