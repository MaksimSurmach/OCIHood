// Package cli defines OCIHood's command-line interface.
package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/app"
	"github.com/MaksimSurmach/OCIHood/internal/config"
	"github.com/MaksimSurmach/OCIHood/internal/state"
	"github.com/spf13/cobra"
)

// Runner performs one OCIHood provisioning run.
type Runner interface {
	Run(context.Context, app.Request) (app.Result, error)
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
		return 1
	}
	return 0
}

func newRootCommand(runner Runner) *cobra.Command {
	var configPath string
	var account string
	var values startValues
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
			overrides, configless := values.overrides(cmd)
			result, err := runner.Run(cmd.Context(), app.Request{ConfigPath: configPath, Account: account, Overrides: overrides, Configless: configPath == "" && configless})
			if err != nil {
				return fmt.Errorf("start provisioning run: %w", err)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "account %s provisioning complete (region %s, instance %s, state %s, public_ip %s)\n", result.Account, result.Region, result.InstanceID, result.InstanceState, result.PublicIP); err != nil {
				return fmt.Errorf("write command result: %w", err)
			}
			return nil
		},
	}
	start.Flags().StringVar(&account, "account", "", "account name")
	values.bind(start)
	_ = start.MarkFlagRequired("account")
	root.AddCommand(start)
	root.AddCommand(newConfigCommand(&configPath))
	root.AddCommand(newStatusCommand(&configPath))

	return root
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
			var effective config.Effective
			var err error
			if *configPath == "" && configless {
				effective, err = config.Defaults(account)
			} else {
				var path string
				path, err = config.Path(*configPath)
				if err == nil {
					var cfg config.File
					cfg, err = config.Load(path)
					if err == nil {
						effective, err = cfg.Resolve(account)
					}
				}
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
