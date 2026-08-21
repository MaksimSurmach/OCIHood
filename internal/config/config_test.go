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
	if got.Shape != defaultShape || got.OCPUs != 2 || got.MemoryGB != 12 || got.BootVolumeGB != 50 || got.OCIProfile != "DEFAULT" || got.RequestTimeout != 30*time.Second {
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
