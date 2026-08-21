// Package config loads and resolves OCIHood configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultProfile  = "DEFAULT"
	defaultShape    = "VM.Standard.A1.Flex"
	defaultOCPUs    = 2
	defaultMemoryGB = 12
	defaultBootGB   = 50
	defaultRequest  = 30 * time.Second
	defaultRetryMin = 30 * time.Second
	defaultRetryMax = 15 * time.Minute
)

// File is the project configuration stored in YAML.
type File struct {
	Defaults Settings           `yaml:"defaults"`
	Accounts map[string]Account `yaml:"accounts"`
}

// Settings contains shared runtime values. Pointer fields distinguish omitted values from zero.
type Settings struct {
	RequestTimeout *time.Duration `yaml:"request_timeout"`
	RetryMin       *time.Duration `yaml:"retry_min"`
	RetryMax       *time.Duration `yaml:"retry_max"`
	StateDir       *string        `yaml:"state_dir"`
	LogDir         *string        `yaml:"log_dir"`
	Shape          *string        `yaml:"shape"`
	OCPUs          *int           `yaml:"ocpus"`
	MemoryGB       *int           `yaml:"memory_gb"`
	BootVolumeGB   *int           `yaml:"boot_volume_gb"`
	PublicIP       *bool          `yaml:"public_ip"`
}

// Account selects OCI credential references and optional runtime overrides.
type Account struct {
	OCIConfigPath     string   `yaml:"oci_config_path"`
	OCIProfile        string   `yaml:"oci_profile"`
	Region            string   `yaml:"region,omitempty"`
	SSHPublicKeyPath  string   `yaml:"ssh_public_key_path,omitempty"`
	SSHPrivateKeyPath string   `yaml:"ssh_private_key_path,omitempty"`
	Overrides         Settings `yaml:"overrides"`
}

// Effective is the fully resolved, printable configuration for one account.
type Effective struct {
	Account           string        `yaml:"account"`
	OCIConfigPath     string        `yaml:"oci_config_path"`
	OCIProfile        string        `yaml:"oci_profile"`
	Region            string        `yaml:"region,omitempty"`
	SSHPublicKeyPath  string        `yaml:"ssh_public_key_path,omitempty"`
	SSHPrivateKeyPath string        `yaml:"ssh_private_key_path,omitempty"`
	RequestTimeout    time.Duration `yaml:"request_timeout"`
	RetryMin          time.Duration `yaml:"retry_min"`
	RetryMax          time.Duration `yaml:"retry_max"`
	StateDir          string        `yaml:"state_dir"`
	LogDir            string        `yaml:"log_dir"`
	Shape             string        `yaml:"shape"`
	OCPUs             int           `yaml:"ocpus"`
	MemoryGB          int           `yaml:"memory_gb"`
	BootVolumeGB      int           `yaml:"boot_volume_gb"`
	PublicIP          bool          `yaml:"public_ip"`
}

// DefaultPath returns the OS-specific default project configuration path.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(dir, "ocihood", "config.yaml"), nil
}

// Path returns explicit when set, otherwise the default configuration path.
func Path(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return DefaultPath()
}

// Load reads and strictly validates a configuration file without writing files or contacting OCI.
func Load(path string) (File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("open config %q: %w", path, err)
	}

	var cfg File
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return File{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return File{}, fmt.Errorf("parse config %q: multiple YAML documents are not supported", path)
		}
		return File{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return File{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	return cfg, nil
}

// Validate checks ranges and cross-field constraints.
func (f File) Validate() error {
	if err := validateSettings("defaults", f.Defaults); err != nil {
		return err
	}
	if f.Defaults.RetryMin != nil && f.Defaults.RetryMax != nil && *f.Defaults.RetryMin > *f.Defaults.RetryMax {
		return errors.New("defaults.retry_min must not exceed defaults.retry_max")
	}
	for name, account := range f.Accounts {
		if strings.TrimSpace(name) == "" {
			return errors.New("account name must not be blank")
		}
		if err := validateSettings("account "+name+" overrides", account.Overrides); err != nil {
			return err
		}
		if _, err := f.Resolve(name); err != nil {
			return err
		}
	}
	return nil
}

func validateSettings(scope string, s Settings) error {
	for name, value := range map[string]*time.Duration{"request_timeout": s.RequestTimeout, "retry_min": s.RetryMin, "retry_max": s.RetryMax} {
		if value != nil && *value <= 0 {
			return fmt.Errorf("%s.%s must be greater than zero", scope, name)
		}
	}
	for name, value := range map[string]*int{"ocpus": s.OCPUs, "memory_gb": s.MemoryGB, "boot_volume_gb": s.BootVolumeGB} {
		if value != nil && *value <= 0 {
			return fmt.Errorf("%s.%s must be greater than zero", scope, name)
		}
	}
	if s.Shape != nil && *s.Shape == "" {
		return fmt.Errorf("%s.shape must not be empty", scope)
	}
	for name, value := range map[string]*string{"state_dir": s.StateDir, "log_dir": s.LogDir} {
		if value != nil && *value == "" {
			return fmt.Errorf("%s.%s must not be empty", scope, name)
		}
	}
	return nil
}

// Resolve applies built-in defaults, global settings, then account overrides.
func (f File) Resolve(name string) (Effective, error) {
	account, ok := f.Accounts[name]
	if !ok {
		return Effective{}, fmt.Errorf("account %q is not defined", name)
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return Effective{}, fmt.Errorf("resolve user config directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Effective{}, fmt.Errorf("resolve user home directory: %w", err)
	}
	e := Effective{
		Account: name, OCIConfigPath: filepath.Join(home, ".oci", "config"), OCIProfile: defaultProfile,
		RequestTimeout: defaultRequest, RetryMin: defaultRetryMin, RetryMax: defaultRetryMax,
		StateDir: filepath.Join(configDir, "ocihood", "state"), LogDir: filepath.Join(configDir, "ocihood", "log"),
		Shape: defaultShape, OCPUs: defaultOCPUs, MemoryGB: defaultMemoryGB, BootVolumeGB: defaultBootGB, PublicIP: true,
	}
	apply(&e, f.Defaults)
	apply(&e, account.Overrides)
	if account.OCIConfigPath != "" {
		e.OCIConfigPath = account.OCIConfigPath
	}
	if account.OCIProfile != "" {
		e.OCIProfile = account.OCIProfile
	}
	e.Region = account.Region
	e.SSHPublicKeyPath = account.SSHPublicKeyPath
	e.SSHPrivateKeyPath = account.SSHPrivateKeyPath
	if e.RetryMin > e.RetryMax {
		return Effective{}, fmt.Errorf("account %q retry_min must not exceed retry_max", name)
	}
	return e, nil
}

func apply(e *Effective, s Settings) {
	if s.RequestTimeout != nil {
		e.RequestTimeout = *s.RequestTimeout
	}
	if s.RetryMin != nil {
		e.RetryMin = *s.RetryMin
	}
	if s.RetryMax != nil {
		e.RetryMax = *s.RetryMax
	}
	if s.StateDir != nil {
		e.StateDir = *s.StateDir
	}
	if s.LogDir != nil {
		e.LogDir = *s.LogDir
	}
	if s.Shape != nil {
		e.Shape = *s.Shape
	}
	if s.OCPUs != nil {
		e.OCPUs = *s.OCPUs
	}
	if s.MemoryGB != nil {
		e.MemoryGB = *s.MemoryGB
	}
	if s.BootVolumeGB != nil {
		e.BootVolumeGB = *s.BootVolumeGB
	}
	if s.PublicIP != nil {
		e.PublicIP = *s.PublicIP
	}
}

// MarshalEffective returns deterministic YAML containing references, never referenced file contents.
func MarshalEffective(e Effective) ([]byte, error) {
	b, err := yaml.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("render effective config: %w", err)
	}
	return b, nil
}
