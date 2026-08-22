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

var defaultAllowedShapes = []string{defaultShape}

// File is the project configuration stored in YAML.
type File struct {
	Defaults Settings           `yaml:"defaults"`
	Accounts map[string]Account `yaml:"accounts"`
}

// Settings contains shared runtime values. Pointer fields distinguish omitted values from zero.
type Settings struct {
	RequestTimeout *time.Duration       `yaml:"request_timeout"`
	RetryMin       *time.Duration       `yaml:"retry_min"`
	RetryMax       *time.Duration       `yaml:"retry_max"`
	StateDir       *string              `yaml:"state_dir"`
	LogDir         *string              `yaml:"log_dir"`
	Shape          *string              `yaml:"shape"`
	OCPUs          *int                 `yaml:"ocpus"`
	MemoryGB       *int                 `yaml:"memory_gb"`
	BootVolumeGB   *int                 `yaml:"boot_volume_gb"`
	PublicIP       *bool                `yaml:"public_ip"`
	Policy         PolicySettings       `yaml:"policy,omitempty"`
	Notifications  NotificationSettings `yaml:"notifications,omitempty"`
}

type NotificationSettings struct {
	Enabled          *bool   `yaml:"enabled,omitempty"`
	TelegramChat     *string `yaml:"telegram_chat_id,omitempty"`
	TelegramTokenEnv *string `yaml:"telegram_token_env,omitempty"`
}

type Notifications struct {
	Enabled          bool   `yaml:"enabled"`
	TelegramChat     string `yaml:"telegram_chat_id,omitempty"`
	TelegramTokenEnv string `yaml:"telegram_token_env,omitempty"`
}

// PolicySettings contains configurable provisioning safety limits.
type PolicySettings struct {
	AllowedShapes *[]string `yaml:"allowed_shapes,omitempty"`
	MaxOCPUs      *int      `yaml:"max_ocpus,omitempty"`
	MaxMemoryGB   *int      `yaml:"max_memory_gb,omitempty"`
	MaxBootGB     *int      `yaml:"max_boot_volume_gb,omitempty"`
	AllowExceed   *bool     `yaml:"allow_exceed,omitempty"`
}

// Policy is the resolved provisioning safety policy.
type Policy struct {
	AllowedShapes []string `yaml:"allowed_shapes" json:"allowed_shapes"`
	MaxOCPUs      int      `yaml:"max_ocpus" json:"max_ocpus"`
	MaxMemoryGB   int      `yaml:"max_memory_gb" json:"max_memory_gb"`
	MaxBootGB     int      `yaml:"max_boot_volume_gb" json:"max_boot_volume_gb"`
	AllowExceed   bool     `yaml:"allow_exceed" json:"allow_exceed"`
}

// PolicyDecision is a sanitized decision for resolved resource values.
type PolicyDecision struct {
	Allowed    bool     `json:"allowed"`
	Overridden bool     `json:"overridden"`
	Violations []string `json:"violations,omitempty"`
}

// Overrides contains explicitly set CLI values. Pointer fields preserve explicit zero and false values.
type Overrides struct {
	OCIConfigPath, OCIProfile, Region, SSHPublicKeyPath, SSHPrivateKeyPath *string
	CompartmentID, ImageID, OperatingSystem, OSVersion                     *string
	VCNID, VCNName, SubnetID, SubnetName                                   *string
	Settings                                                               Settings
}

// Account selects OCI credential references and optional runtime overrides.
type Account struct {
	OCIConfigPath     string   `yaml:"oci_config_path"`
	OCIProfile        string   `yaml:"oci_profile"`
	Region            string   `yaml:"region,omitempty"`
	SSHPublicKeyPath  string   `yaml:"ssh_public_key_path,omitempty"`
	SSHPrivateKeyPath string   `yaml:"ssh_private_key_path,omitempty"`
	CompartmentID     string   `yaml:"compartment_id,omitempty"`
	ImageID           string   `yaml:"image_id,omitempty"`
	OperatingSystem   string   `yaml:"operating_system,omitempty"`
	OSVersion         string   `yaml:"os_version,omitempty"`
	VCNID             string   `yaml:"vcn_id,omitempty"`
	VCNName           string   `yaml:"vcn_name,omitempty"`
	SubnetID          string   `yaml:"subnet_id,omitempty"`
	SubnetName        string   `yaml:"subnet_name,omitempty"`
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
	CompartmentID     string        `yaml:"compartment_id,omitempty"`
	ImageID           string        `yaml:"image_id,omitempty"`
	OperatingSystem   string        `yaml:"operating_system,omitempty"`
	OSVersion         string        `yaml:"os_version,omitempty"`
	VCNID             string        `yaml:"vcn_id,omitempty"`
	VCNName           string        `yaml:"vcn_name,omitempty"`
	SubnetID          string        `yaml:"subnet_id,omitempty"`
	SubnetName        string        `yaml:"subnet_name,omitempty"`
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
	Policy            Policy        `yaml:"policy"`
	Notifications     Notifications `yaml:"notifications"`
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
		if account.VCNID != "" && account.VCNName != "" {
			return fmt.Errorf("account %q vcn_id and vcn_name are mutually exclusive", name)
		}
		if account.SubnetID != "" && account.SubnetName != "" {
			return fmt.Errorf("account %q subnet_id and subnet_name are mutually exclusive", name)
		}
		if _, err := f.Resolve(name); err != nil {
			return err
		}
	}
	return nil
}

func validateSettings(scope string, s Settings) error {
	if s.Notifications.TelegramChat != nil && strings.TrimSpace(*s.Notifications.TelegramChat) == "" {
		return fmt.Errorf("%s.notifications.telegram_chat_id must not be blank", scope)
	}
	if s.Notifications.TelegramTokenEnv != nil && strings.TrimSpace(*s.Notifications.TelegramTokenEnv) == "" {
		return fmt.Errorf("%s.notifications.telegram_token_env must not be blank", scope)
	}
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
	if shapes := s.Policy.AllowedShapes; shapes != nil {
		if len(*shapes) == 0 {
			return fmt.Errorf("%s.policy.allowed_shapes must not be empty", scope)
		}
		seen := make(map[string]struct{}, len(*shapes))
		for _, shape := range *shapes {
			if strings.TrimSpace(shape) == "" {
				return fmt.Errorf("%s.policy.allowed_shapes must not contain blank values", scope)
			}
			if _, ok := seen[shape]; ok {
				return fmt.Errorf("%s.policy.allowed_shapes contains duplicate %q", scope, shape)
			}
			seen[shape] = struct{}{}
		}
	}
	for name, value := range map[string]*int{"max_ocpus": s.Policy.MaxOCPUs, "max_memory_gb": s.Policy.MaxMemoryGB, "max_boot_volume_gb": s.Policy.MaxBootGB} {
		if value != nil && *value <= 0 {
			return fmt.Errorf("%s.policy.%s must be greater than zero", scope, name)
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
		Policy: Policy{AllowedShapes: append([]string(nil), defaultAllowedShapes...), MaxOCPUs: defaultOCPUs, MaxMemoryGB: defaultMemoryGB, MaxBootGB: defaultBootGB},
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
	e.CompartmentID = account.CompartmentID
	e.ImageID = account.ImageID
	e.OperatingSystem = account.OperatingSystem
	e.OSVersion = account.OSVersion
	e.VCNID = account.VCNID
	e.VCNName = account.VCNName
	e.SubnetID = account.SubnetID
	e.SubnetName = account.SubnetName
	if e.RetryMin > e.RetryMax {
		return Effective{}, fmt.Errorf("account %q retry_min must not exceed retry_max", name)
	}
	if e.Notifications.Enabled && (e.Notifications.TelegramChat == "" || e.Notifications.TelegramTokenEnv == "") {
		return Effective{}, fmt.Errorf("account %q enabled notifications require telegram_chat_id and telegram_token_env", name)
	}
	return e, nil
}

// Defaults resolves built-in defaults for a configless named account.
func Defaults(name string) (Effective, error) {
	return (File{Accounts: map[string]Account{name: {}}}).Resolve(name)
}

func apply(e *Effective, s Settings) {
	if s.Notifications.Enabled != nil {
		e.Notifications.Enabled = *s.Notifications.Enabled
	}
	if s.Notifications.TelegramChat != nil {
		e.Notifications.TelegramChat = *s.Notifications.TelegramChat
	}
	if s.Notifications.TelegramTokenEnv != nil {
		e.Notifications.TelegramTokenEnv = *s.Notifications.TelegramTokenEnv
	}
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
	if s.Policy.AllowedShapes != nil {
		e.Policy.AllowedShapes = append([]string(nil), (*s.Policy.AllowedShapes)...)
	}
	if s.Policy.MaxOCPUs != nil {
		e.Policy.MaxOCPUs = *s.Policy.MaxOCPUs
	}
	if s.Policy.MaxMemoryGB != nil {
		e.Policy.MaxMemoryGB = *s.Policy.MaxMemoryGB
	}
	if s.Policy.MaxBootGB != nil {
		e.Policy.MaxBootGB = *s.Policy.MaxBootGB
	}
	if s.Policy.AllowExceed != nil {
		e.Policy.AllowExceed = *s.Policy.AllowExceed
	}
}

// EvaluatePolicy checks resolved resources without reading secrets or contacting OCI.
func EvaluatePolicy(e Effective) PolicyDecision {
	p := e.Policy
	requestedShape, ocpus, memory, boot := e.Shape, e.OCPUs, e.MemoryGB, e.BootVolumeGB
	if requestedShape == "" {
		requestedShape = defaultShape
	}
	if ocpus == 0 {
		ocpus = defaultOCPUs
	}
	if memory == 0 {
		memory = defaultMemoryGB
	}
	if boot == 0 {
		boot = defaultBootGB
	}
	if len(p.AllowedShapes) == 0 {
		p.AllowedShapes = defaultAllowedShapes
	}
	if p.MaxOCPUs == 0 {
		p.MaxOCPUs = defaultOCPUs
	}
	if p.MaxMemoryGB == 0 {
		p.MaxMemoryGB = defaultMemoryGB
	}
	if p.MaxBootGB == 0 {
		p.MaxBootGB = defaultBootGB
	}
	allowedShape := false
	for _, allowed := range p.AllowedShapes {
		if requestedShape == allowed {
			allowedShape = true
			break
		}
	}
	var violations []string
	if !allowedShape {
		violations = append(violations, fmt.Sprintf("shape %q is not allowed", requestedShape))
	}
	if ocpus > p.MaxOCPUs {
		violations = append(violations, fmt.Sprintf("ocpus %d exceeds maximum %d", ocpus, p.MaxOCPUs))
	}
	if memory > p.MaxMemoryGB {
		violations = append(violations, fmt.Sprintf("memory_gb %d exceeds maximum %d", memory, p.MaxMemoryGB))
	}
	if boot > p.MaxBootGB {
		violations = append(violations, fmt.Sprintf("boot_volume_gb %d exceeds maximum %d", boot, p.MaxBootGB))
	}
	return PolicyDecision{Allowed: len(violations) == 0 || p.AllowExceed, Overridden: len(violations) > 0 && p.AllowExceed, Violations: violations}
}

// ApplyOverrides applies explicitly set CLI values and validates the resulting configuration.
func ApplyOverrides(e Effective, o Overrides) (Effective, error) {
	apply(&e, o.Settings)
	if o.VCNID != nil && o.VCNName == nil {
		e.VCNName = ""
	}
	if o.VCNName != nil && o.VCNID == nil {
		e.VCNID = ""
	}
	if o.SubnetID != nil && o.SubnetName == nil {
		e.SubnetName = ""
	}
	if o.SubnetName != nil && o.SubnetID == nil {
		e.SubnetID = ""
	}
	for value, target := range map[*string]*string{
		o.OCIConfigPath: &e.OCIConfigPath, o.OCIProfile: &e.OCIProfile, o.Region: &e.Region,
		o.SSHPublicKeyPath: &e.SSHPublicKeyPath, o.SSHPrivateKeyPath: &e.SSHPrivateKeyPath,
		o.CompartmentID: &e.CompartmentID, o.ImageID: &e.ImageID, o.OperatingSystem: &e.OperatingSystem,
		o.OSVersion: &e.OSVersion, o.VCNID: &e.VCNID, o.VCNName: &e.VCNName,
		o.SubnetID: &e.SubnetID, o.SubnetName: &e.SubnetName,
	} {
		if value != nil {
			*target = *value
		}
	}
	if err := validateSettings("CLI overrides", o.Settings); err != nil {
		return Effective{}, err
	}
	if e.VCNID != "" && e.VCNName != "" {
		return Effective{}, errors.New("vcn_id and vcn_name are mutually exclusive")
	}
	if e.SubnetID != "" && e.SubnetName != "" {
		return Effective{}, errors.New("subnet_id and subnet_name are mutually exclusive")
	}
	if e.RetryMin > e.RetryMax {
		return Effective{}, errors.New("retry_min must not exceed retry_max")
	}
	if e.Notifications.Enabled && (e.Notifications.TelegramChat == "" || e.Notifications.TelegramTokenEnv == "") {
		return Effective{}, errors.New("enabled notifications require telegram_chat_id and telegram_token_env")
	}
	return e, nil
}

// MarshalEffective returns deterministic YAML containing references, never referenced file contents.
func MarshalEffective(e Effective) ([]byte, error) {
	b, err := yaml.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("render effective config: %w", err)
	}
	return b, nil
}
