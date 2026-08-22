package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAndResolve(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `
defaults:
  request_timeout: 45s
  retry_min: 1m
  retry_max: 10m
  memory_gb: 10
  public_ip: false
accounts:
  first:
    oci_config_path: /oci/first
    oci_profile: ONE
    ssh_public_key_path: /ssh/first.pub
    overrides:
      memory_gb: 8
  second:
    oci_config_path: /oci/second
    oci_profile: TWO
    ssh_private_key_path: /ssh/second
    overrides:
      retry_max: 20m
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := cfg.Resolve("first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := cfg.Resolve("second")
	if err != nil {
		t.Fatal(err)
	}
	if first.Account != "first" || first.OCIProfile != "ONE" || first.MemoryGB != 8 || first.RequestTimeout != 45*time.Second || first.PublicIP {
		t.Fatalf("first effective config = %#v", first)
	}
	if second.Account != "second" || second.OCIProfile != "TWO" || second.MemoryGB != 10 || second.RetryMax != 20*time.Minute {
		t.Fatalf("second effective config = %#v", second)
	}
	if first.OCIConfigPath == second.OCIConfigPath || first.SSHPrivateKeyPath != "" || second.SSHPrivateKeyPath != "/ssh/second" {
		t.Fatalf("account state leaked: first=%#v second=%#v", first, second)
	}
}

func TestBuiltInDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := Load(writeConfig(t, "accounts:\n  minimal: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := cfg.Resolve("minimal")
	if err != nil {
		t.Fatal(err)
	}
	if got.Shape != defaultShape || got.OCPUs != 2 || got.MemoryGB != 12 || got.BootVolumeGB != 50 || got.OCIProfile != "DEFAULT" || got.RequestTimeout != 30*time.Second || !reflect.DeepEqual(got.Policy.AllowedShapes, []string{defaultShape}) || got.Policy.MaxOCPUs != 2 || got.Policy.MaxMemoryGB != 12 || got.Policy.MaxBootGB != 50 {
		t.Fatalf("defaults = %#v", got)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "malformed YAML", body: "accounts: [", want: "parse config"},
		{name: "unknown field", body: "unknown: true", want: "field unknown not found"},
		{name: "duplicate account", body: "accounts:\n  one: {}\n  one: {}\n", want: "already defined"},
		{name: "blank account", body: "accounts:\n  ' ': {}\n", want: "account name must not be blank"},
		{name: "zero timeout", body: "defaults:\n  request_timeout: 0s\n", want: "request_timeout must be greater than zero"},
		{name: "negative resources", body: "defaults:\n  ocpus: -1\n", want: "ocpus must be greater than zero"},
		{name: "retry bounds", body: "defaults:\n  retry_min: 2m\n  retry_max: 1m\n", want: "defaults.retry_min must not exceed defaults.retry_max"},
		{name: "cross layer retry bounds", body: "defaults:\n  retry_max: 1m\naccounts:\n  one:\n    overrides:\n      retry_min: 2m\n", want: "retry_min must not exceed retry_max"},
		{name: "empty allowed shapes", body: "defaults:\n  policy:\n    allowed_shapes: []\n", want: "allowed_shapes must not be empty"},
		{name: "invalid policy maximum", body: "defaults:\n  policy:\n    max_ocpus: 0\n", want: "max_ocpus must be greater than zero"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(writeConfig(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestPolicyResolutionAndEvaluation(t *testing.T) {
	t.Parallel()
	cfg, err := Load(writeConfig(t, `
defaults:
  policy:
    allowed_shapes: [global-shape]
    max_ocpus: 4
    max_memory_gb: 16
    max_boot_volume_gb: 100
accounts:
  test:
    overrides:
      shape: account-shape
      ocpus: 5
      memory_gb: 17
      boot_volume_gb: 101
      policy:
        allowed_shapes: [account-shape]
        max_ocpus: 5
        allow_exceed: true
`))
	if err != nil {
		t.Fatal(err)
	}
	effective, err := cfg.Resolve("test")
	if err != nil {
		t.Fatal(err)
	}
	decision := EvaluatePolicy(effective)
	if !decision.Allowed || !decision.Overridden || !reflect.DeepEqual(decision.Violations, []string{"memory_gb 17 exceeds maximum 16", "boot_volume_gb 101 exceeds maximum 100"}) {
		t.Fatalf("effective=%+v decision=%+v", effective, decision)
	}
}

func TestEvaluatePolicyBoundaries(t *testing.T) {
	t.Parallel()
	base := Effective{Shape: "shape", OCPUs: 2, MemoryGB: 8, BootVolumeGB: 50, Policy: Policy{AllowedShapes: []string{"shape"}, MaxOCPUs: 2, MaxMemoryGB: 8, MaxBootGB: 50}}
	tests := []struct {
		name string
		edit func(*Effective)
		want string
	}{
		{name: "within policy", edit: func(*Effective) {}},
		{name: "shape", edit: func(e *Effective) { e.Shape = "other" }, want: `shape "other" is not allowed`},
		{name: "ocpus", edit: func(e *Effective) { e.OCPUs++ }, want: "ocpus 3 exceeds maximum 2"},
		{name: "memory", edit: func(e *Effective) { e.MemoryGB++ }, want: "memory_gb 9 exceeds maximum 8"},
		{name: "boot volume", edit: func(e *Effective) { e.BootVolumeGB++ }, want: "boot_volume_gb 51 exceeds maximum 50"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effective := base
			tt.edit(&effective)
			got := EvaluatePolicy(effective)
			if (tt.want == "") != got.Allowed || tt.want != "" && !reflect.DeepEqual(got.Violations, []string{tt.want}) {
				t.Fatalf("decision=%+v", got)
			}
		})
	}
}

func TestMissingFileAndAccountAreActionable(t *testing.T) {
	t.Parallel()
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil || !strings.Contains(err.Error(), "open config") {
		t.Fatalf("missing file error = %v", err)
	}
	cfg, err := Load(writeConfig(t, "accounts:\n  known: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Resolve("missing"); err == nil || !strings.Contains(err.Error(), `account "missing" is not defined`) {
		t.Fatalf("missing account error = %v", err)
	}
}

func TestPathResolution(t *testing.T) {
	t.Parallel()
	if got, err := Path("/explicit/config.yaml"); err != nil || got != "/explicit/config.yaml" {
		t.Fatalf("Path(explicit) = %q, %v", got, err)
	}
	got, err := Path("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, filepath.Join("ocihood", "config.yaml")) {
		t.Fatalf("Path(default) = %q", got)
	}
}

func TestMarshalEffectiveIsDeterministicAndDoesNotReadReferences(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	secret := "PRIVATE-KEY-CONTENTS"
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(writeConfig(t, "accounts:\n  safe:\n    ssh_private_key_path: "+key+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	effective, err := cfg.Resolve("safe")
	if err != nil {
		t.Fatal(err)
	}
	one, err := MarshalEffective(effective)
	if err != nil {
		t.Fatal(err)
	}
	two, err := MarshalEffective(effective)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one, two) || strings.Contains(string(one), secret) || !strings.Contains(string(one), key) {
		t.Fatalf("unsafe or nondeterministic output: %q / %q", one, two)
	}
}

func TestApplyOverridesPrecedenceAndExplicitValues(t *testing.T) {
	t.Parallel()
	stringValue := func(value string) *string { return &value }
	intValue := func(value int) *int { return &value }
	boolValue := func(value bool) *bool { return &value }
	durationValue := func(value time.Duration) *time.Duration { return &value }

	base := Effective{Account: "test", VCNName: "file-vcn", Shape: "file-shape", OCPUs: 4, MemoryGB: 8, BootVolumeGB: 50, PublicIP: true, RetryMin: time.Minute, RetryMax: 2 * time.Minute, Policy: Policy{AllowedShapes: []string{"file-shape", "cli-shape"}, MaxOCPUs: 4, MaxMemoryGB: 8, MaxBootGB: 50}}
	tests := []struct {
		name      string
		overrides Overrides
		check     func(Effective) bool
		wantErr   string
	}{
		{name: "unset preserves file", check: func(got Effective) bool { return reflect.DeepEqual(got, base) }},
		{name: "all explicit values win", overrides: Overrides{Region: stringValue("eu-zurich-1"), Settings: Settings{Shape: stringValue("cli-shape"), OCPUs: intValue(2), PublicIP: boolValue(false), RetryMin: durationValue(30 * time.Second)}}, check: func(got Effective) bool {
			return got.Region == "eu-zurich-1" && got.Shape == "cli-shape" && got.OCPUs == 2 && !got.PublicIP && got.RetryMin == 30*time.Second
		}},
		{name: "resource override is checked by policy", overrides: Overrides{Settings: Settings{OCPUs: intValue(5)}}, check: func(got Effective) bool { return got.OCPUs == 5 && !EvaluatePolicy(got).Allowed }},
		{name: "explicit zero is validated", overrides: Overrides{Settings: Settings{OCPUs: intValue(0)}}, wantErr: "ocpus must be greater than zero"},
		{name: "exclusive selector replaces lower layer", overrides: Overrides{VCNID: stringValue("id")}, check: func(got Effective) bool { return got.VCNID == "id" && got.VCNName == "" }},
		{name: "cross-field validation", overrides: Overrides{VCNID: stringValue("id"), VCNName: stringValue("name")}, wantErr: "mutually exclusive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ApplyOverrides(base, tt.overrides)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || !tt.check(got) {
				t.Fatalf("ApplyOverrides() = %#v, %v", got, err)
			}
		})
	}
}
