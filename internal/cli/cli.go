// Package cli defines OCIHood's command-line interface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/app"
	"github.com/MaksimSurmach/OCIHood/internal/capacity"
	"github.com/MaksimSurmach/OCIHood/internal/config"
	"github.com/MaksimSurmach/OCIHood/internal/launch"
	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
	"github.com/MaksimSurmach/OCIHood/internal/state"
	"github.com/spf13/cobra"
)

// Runner performs one OCIHood provisioning run.
type Runner interface {
	Run(context.Context, app.Request) (app.Result, error)
	Plan(context.Context, app.Request) (app.Plan, error)
}

// Execute runs the CLI and returns its process exit code.
func Execute(ctx context.Context, args []string, runner Runner, stdout, stderr io.Writer) int {
	cmd := newRootCommand(runner)
	cmd.SetContext(ctx)
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	if err := cmd.Execute(); err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		var exit *exitError
		if errors.As(err, &exit) {
			return exit.code
		}
		return 1
	}
	return 0
}

func newRootCommand(runner Runner) *cobra.Command {
	var configPath string
	var account string
	var values startValues
	var execution executionValues
	root := &cobra.Command{
		Use:           "ocihood",
		Short:         "Provision Oracle Cloud Infrastructure resources",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.PersistentFlags().StringVar(&configPath, "config", "", "configuration file (default: OS user config directory/ocihood/config.yaml)")

	start := &cobra.Command{
		Use:   "start",
		Short: "Start one provisioning run",
		Long:  "Start one provisioning run. Explicit flags override account settings, global settings, and built-in defaults. A config file is optional when required inputs are supplied as flags.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger, err := execution.logger(cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if configurable, ok := runner.(interface{ SetLogger(*slog.Logger) }); ok {
				configurable.SetLogger(logger)
			}
			overrides, configless := values.overrides(cmd)
			ctx := cmd.Context()
			if execution.maxRuntime < 0 {
				return errors.New("--max-runtime must not be negative")
			}
			if execution.maxRuntime > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, execution.maxRuntime)
				defer cancel()
			}
			result, runErr := runner.Run(ctx, app.Request{ConfigPath: configPath, Account: account, Overrides: overrides, Configless: configPath == "" && configless, Once: execution.once})
			document, code := commandResult(account, result, runErr)
			if err := execution.write(cmd.OutOrStdout(), document); err != nil {
				return fmt.Errorf("write command result: %w", err)
			}
			if runErr != nil || code != 0 {
				return &exitError{code: code, message: document.Error.Message}
			}
			return nil
		},
	}
	start.Flags().StringVar(&account, "account", "", "account name")
	values.bind(start)
	execution.bind(start)
	_ = start.MarkFlagRequired("account")
	root.AddCommand(start)
	plan := &cobra.Command{
		Use: "plan", Short: "Show resolved provisioning intent without modifying OCI", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			overrides, configless := values.overrides(cmd)
			result, err := runner.Plan(cmd.Context(), app.Request{ConfigPath: configPath, Account: account, Overrides: overrides, Configless: configPath == "" && configless})
			if err != nil {
				return fmt.Errorf("plan provisioning run: %w", err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "account: %s\ntarget_id: %s\nregion: %s\ncompartment_id: %s\nshape: %s\nocpus: %d\nmemory_gb: %d\nimage_id: %s\nvcn_id: %s\nsubnet_id: %s\nboot_volume_gb: %d\npublic_ip: %t\npolicy_decision: %s\npolicy_violations: %s\navailability_domains: %s\nmanaged_instances: %s\naction: %s\nreason: %s\n", result.Account, result.TargetID, result.Region, result.CompartmentID, result.Shape, result.OCPUs, result.MemoryGB, result.ImageID, result.VCNID, result.SubnetID, result.BootVolumeGB, result.PublicIP, renderPolicy(result.Policy), strings.Join(result.Policy.Violations, "; "), strings.Join(result.AvailabilityDomains, ","), renderInstances(result.Instances), renderAction(result.Action), result.Reason)
			return err
		},
	}
	plan.Flags().StringVar(&account, "account", "", "account name")
	values.bind(plan)
	_ = plan.MarkFlagRequired("account")
	root.AddCommand(plan)
	root.AddCommand(newConfigCommand(&configPath))
	root.AddCommand(newStatusCommand(&configPath))

	return root
}

const resultSchema = "ocihood.start/v1"

type commandDocument struct {
	Schema             string                 `json:"schema"`
	Account            string                 `json:"account"`
	TargetID           string                 `json:"target_id,omitempty"`
	Outcome            string                 `json:"outcome"`
	Region             string                 `json:"region,omitempty"`
	InstanceID         string                 `json:"instance_id,omitempty"`
	InstanceState      string                 `json:"instance_state,omitempty"`
	PublicIP           string                 `json:"public_ip,omitempty"`
	Policy             *config.PolicyDecision `json:"policy,omitempty"`
	NotificationErrors []string               `json:"notification_errors,omitempty"`
	Error              struct {
		Category string `json:"category,omitempty"`
		Message  string `json:"message,omitempty"`
	} `json:"error,omitempty"`
}

type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string { return e.message }

type executionValues struct {
	once                            bool
	maxRuntime                      time.Duration
	logLevel, logFormat, outputMode string
}

func (v *executionValues) bind(command *cobra.Command) {
	f := command.Flags()
	f.BoolVar(&v.once, "once", false, "perform one capacity decision cycle")
	f.DurationVar(&v.maxRuntime, "max-runtime", 0, "maximum run duration (zero means unlimited)")
	f.StringVar(&v.logLevel, "log-level", "info", "diagnostic level: debug, info, warn, error")
	f.StringVar(&v.logFormat, "log-format", "text", "diagnostic format: text or json")
	f.StringVar(&v.outputMode, "output", "text", "final result format: text or json")
}

func (v executionValues) logger(stderr io.Writer) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(v.logLevel)); err != nil {
		return nil, fmt.Errorf("invalid --log-level %q", v.logLevel)
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch v.logFormat {
	case "text":
		handler = slog.NewTextHandler(stderr, opts)
	case "json":
		handler = slog.NewJSONHandler(stderr, opts)
	default:
		return nil, fmt.Errorf("invalid --log-format %q", v.logFormat)
	}
	if v.outputMode != "text" && v.outputMode != "json" {
		return nil, fmt.Errorf("invalid --output %q", v.outputMode)
	}
	return slog.New(handler), nil
}

func (v executionValues) write(out io.Writer, result commandDocument) error {
	if v.outputMode == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	if result.Error.Category != "" {
		_, err := fmt.Fprintf(out, "account %s provisioning %s (%s: %s)\n", result.Account, result.Outcome, result.Error.Category, result.Error.Message)
		return err
	}
	_, err := fmt.Fprintf(out, "account %s provisioning %s (region %s, instance %s, state %s, public_ip %s)\n", result.Account, result.Outcome, result.Region, result.InstanceID, result.InstanceState, result.PublicIP)
	return err
}

func commandResult(account string, result app.Result, err error) (commandDocument, int) {
	doc := commandDocument{Schema: resultSchema, Account: account, TargetID: result.TargetID, Outcome: "success", Region: result.Region, InstanceID: result.InstanceID, InstanceState: result.InstanceState, PublicIP: result.PublicIP, NotificationErrors: result.NotificationErrors}
	if result.Policy.Allowed || result.Policy.Overridden || len(result.Policy.Violations) > 0 {
		doc.Policy = &result.Policy
	}
	if err == nil {
		if result.Decision == reconcile.DecisionAlreadySatisfied {
			doc.Outcome = "already_satisfied"
		}
		if result.Capacity == capacity.Unavailable {
			doc.Outcome = "no_capacity"
			return doc, 3
		}
		return doc, 0
	}
	doc.Outcome, doc.Error.Category, doc.Error.Message = "failed", "fatal", "provisioning failed"
	code := 1
	if errors.Is(err, context.DeadlineExceeded) {
		doc.Outcome, doc.Error.Category, doc.Error.Message, code = "deadline_exceeded", "deadline", "maximum runtime exceeded", 124
	} else if errors.Is(err, context.Canceled) {
		doc.Outcome, doc.Error.Category, doc.Error.Message, code = "canceled", "canceled", "provisioning canceled", 130
	} else {
		var capacityErr *capacity.Error
		var launchErr *launch.Error
		if errors.As(err, &capacityErr) && (capacityErr.Kind == capacity.Transient || capacityErr.Kind == capacity.Throttled) || errors.As(err, &launchErr) && (launchErr.Kind == launch.Transient || launchErr.Kind == launch.Ambiguous || launchErr.Kind == launch.OutOfCapacity) {
			doc.Outcome, doc.Error.Category, doc.Error.Message, code = "retryable_failure", "transient", "retryable provider failure", 4
		} else {
			var appErr *app.Error
			if errors.As(err, &appErr) {
				doc.Error.Message = "provisioning failed during " + appErr.Phase
			}
		}
	}
	return doc, code
}

func renderPolicy(decision config.PolicyDecision) string {
	if decision.Overridden {
		return "override-allowed"
	}
	if decision.Allowed {
		return "within-policy"
	}
	return "rejected"
}

func renderAction(kind reconcile.DecisionKind) string {
	switch kind {
	case reconcile.DecisionCreate, reconcile.DecisionNewAttemptSafe, reconcile.DecisionRetrySameAttempt:
		return "create"
	case reconcile.DecisionAlreadySatisfied:
		return "already-satisfied"
	case reconcile.DecisionConflict, reconcile.DecisionResumeReconcile:
		return "blocked/conflict/ambiguous"
	default:
		return "blocked/conflict/ambiguous"
	}
}

func renderInstances(instances []reconcile.Instance) string {
	values := make([]string, len(instances))
	for i, instance := range instances {
		var lifecycle string
		switch instance.Lifecycle {
		case reconcile.LifecycleActive:
			lifecycle = "active"
		case reconcile.LifecycleTerminated:
			lifecycle = "terminated"
		default:
			lifecycle = "unknown"
		}
		values[i] = fmt.Sprintf("%s(%s)", instance.ID, lifecycle)
	}
	return strings.Join(values, ",")
}

type startValues struct {
	ociConfig, ociProfile, region, sshPublicKey, sshPrivateKey string
	compartment, image, operatingSystem, osVersion             string
	vcnID, vcnName, subnetID, subnetName, stateDir, logDir     string
	shape                                                      string
	ocpus, memoryGB, bootVolumeGB                              int
	publicIP                                                   bool
	requestTimeout, retryMin, retryMax                         time.Duration
}

func (v *startValues) bind(command *cobra.Command) {
	f := command.Flags()
	f.StringVar(&v.ociConfig, "oci-config", "", "OCI SDK config path (default ~/.oci/config)")
	f.StringVar(&v.ociProfile, "oci-profile", "", "OCI SDK profile (default DEFAULT)")
	f.StringVar(&v.region, "region", "", "OCI region override")
	f.StringVar(&v.sshPublicKey, "ssh-public-key", "", "SSH public key file reference")
	f.StringVar(&v.sshPrivateKey, "ssh-private-key", "", "SSH private key file reference")
	f.StringVar(&v.compartment, "compartment-id", "", "target compartment OCID")
	f.StringVar(&v.image, "image-id", "", "image OCID")
	f.StringVar(&v.operatingSystem, "operating-system", "", "image operating system selector")
	f.StringVar(&v.osVersion, "os-version", "", "image operating system version selector")
	f.StringVar(&v.vcnID, "vcn-id", "", "VCN OCID")
	f.StringVar(&v.vcnName, "vcn-name", "", "VCN name selector")
	f.StringVar(&v.subnetID, "subnet-id", "", "subnet OCID")
	f.StringVar(&v.subnetName, "subnet-name", "", "subnet name selector")
	f.StringVar(&v.stateDir, "state-dir", "", "durable state directory")
	f.StringVar(&v.logDir, "log-dir", "", "log directory")
	f.StringVar(&v.shape, "shape", "", "compute shape")
	f.IntVar(&v.ocpus, "ocpus", 0, "OCPU count")
	f.IntVar(&v.memoryGB, "memory-gb", 0, "memory in GiB")
	f.IntVar(&v.bootVolumeGB, "boot-volume-gb", 0, "boot volume in GiB")
	f.BoolVar(&v.publicIP, "public-ip", true, "assign a public IP")
	f.DurationVar(&v.requestTimeout, "request-timeout", 0, "provider request timeout")
	f.DurationVar(&v.retryMin, "retry-min", 0, "minimum retry interval")
	f.DurationVar(&v.retryMax, "retry-max", 0, "maximum retry interval")
	command.Example = "  ocihood start --account personal --oci-profile DEFAULT --ssh-public-key ~/.ssh/id_ed25519.pub"
}

func (v startValues) overrides(command *cobra.Command) (config.Overrides, bool) {
	f, any := command.Flags(), false
	changed := func(name string) bool { set := f.Changed(name); any = any || set; return set }
	var o config.Overrides
	setString := func(name string, value string, target **string) {
		if changed(name) {
			*target = &value
		}
	}
	setInt := func(name string, value int, target **int) {
		if changed(name) {
			*target = &value
		}
	}
	setDuration := func(name string, value time.Duration, target **time.Duration) {
		if changed(name) {
			*target = &value
		}
	}
	setString("oci-config", v.ociConfig, &o.OCIConfigPath)
	setString("oci-profile", v.ociProfile, &o.OCIProfile)
	setString("region", v.region, &o.Region)
	setString("ssh-public-key", v.sshPublicKey, &o.SSHPublicKeyPath)
	setString("ssh-private-key", v.sshPrivateKey, &o.SSHPrivateKeyPath)
	setString("compartment-id", v.compartment, &o.CompartmentID)
	setString("image-id", v.image, &o.ImageID)
	setString("operating-system", v.operatingSystem, &o.OperatingSystem)
	setString("os-version", v.osVersion, &o.OSVersion)
	setString("vcn-id", v.vcnID, &o.VCNID)
	setString("vcn-name", v.vcnName, &o.VCNName)
	setString("subnet-id", v.subnetID, &o.SubnetID)
	setString("subnet-name", v.subnetName, &o.SubnetName)
	setString("state-dir", v.stateDir, &o.Settings.StateDir)
	setString("log-dir", v.logDir, &o.Settings.LogDir)
	setString("shape", v.shape, &o.Settings.Shape)
	setInt("ocpus", v.ocpus, &o.Settings.OCPUs)
	setInt("memory-gb", v.memoryGB, &o.Settings.MemoryGB)
	setInt("boot-volume-gb", v.bootVolumeGB, &o.Settings.BootVolumeGB)
	if changed("public-ip") {
		o.Settings.PublicIP = &v.publicIP
	}
	setDuration("request-timeout", v.requestTimeout, &o.Settings.RequestTimeout)
	setDuration("retry-min", v.retryMin, &o.Settings.RetryMin)
	setDuration("retry-max", v.retryMax, &o.Settings.RetryMax)
	return o, any
}

func newStatusCommand(configPath *string) *cobra.Command {
	var account string
	command := &cobra.Command{
		Use: "status", Short: "Read persisted provisioning status without contacting OCI", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.Path(*configPath)
			if err != nil {
				return err
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			effective, err := cfg.Resolve(account)
			if err != nil {
				return err
			}
			value, err := state.New(effective.StateDir).LoadAccount(account)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "account: %s\ntarget_id: %s\nstatus: %s\ninstance_id: %s\npublic_ip: %s\nlast_result: %s\nlast_error: %s\nupdated_at: %s\n", value.Account, value.TargetID, value.Lifecycle, value.InstanceID, value.PublicIP, value.LastResult, value.LastError, value.UpdatedAt.Format(time.RFC3339)); err != nil {
				return fmt.Errorf("write status: %w", err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&account, "account", "", "account name")
	_ = command.MarkFlagRequired("account")
	return command
}

func newConfigCommand(configPath *string) *cobra.Command {
	command := &cobra.Command{Use: "config", Short: "Inspect and validate configuration", Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{
		Use: "validate", Short: "Validate configuration without contacting OCI", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			path, err := config.Path(*configPath)
			if err != nil {
				return err
			}
			if _, err := config.Load(path); err != nil {
				return err
			}
			return nil
		},
	})
	var account string
	var values startValues
	show := &cobra.Command{
		Use: "show", Short: "Show effective non-secret account configuration", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			overrides, configless := values.overrides(cmd)
			path, err := config.Path(*configPath)
			if err != nil {
				return err
			}
			cfg, err := config.Load(path)
			var effective config.Effective
			if err == nil {
				effective, err = cfg.Resolve(account)
			} else if *configPath == "" && configless && errors.Is(err, os.ErrNotExist) {
				effective, err = config.Defaults(account)
			}
			if err != nil {
				return err
			}
			effective, err = config.ApplyOverrides(effective, overrides)
			if err != nil {
				return err
			}
			out, err := config.MarshalEffective(effective)
			if err != nil {
				return err
			}
			if _, err := cmd.OutOrStdout().Write(out); err != nil {
				return fmt.Errorf("write effective config: %w", err)
			}
			return nil
		},
	}
	show.Flags().StringVar(&account, "account", "", "account name")
	values.bind(show)
	_ = show.MarkFlagRequired("account")
	command.AddCommand(show)
	return command
}
