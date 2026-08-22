package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/app"
	"github.com/MaksimSurmach/OCIHood/internal/capacity"
	"github.com/MaksimSurmach/OCIHood/internal/cli"
	"github.com/MaksimSurmach/OCIHood/internal/config"
	"github.com/MaksimSurmach/OCIHood/internal/discovery"
	"github.com/MaksimSurmach/OCIHood/internal/launch"
	"github.com/MaksimSurmach/OCIHood/internal/notification"
	"github.com/MaksimSurmach/OCIHood/internal/provider/oci/auth"
	ocicapacity "github.com/MaksimSurmach/OCIHood/internal/provider/oci/capacity"
	ocidiscovery "github.com/MaksimSurmach/OCIHood/internal/provider/oci/discovery"
	ocilaunch "github.com/MaksimSurmach/OCIHood/internal/provider/oci/launch"
	"github.com/MaksimSurmach/OCIHood/internal/provisioner"
	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
	"github.com/MaksimSurmach/OCIHood/internal/state"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	var runner *app.Runner
	runner = app.NewRunner(logger, func(_ context.Context, path, account string) (config.Effective, error) {
		resolvedPath, err := config.Path(path)
		if err != nil {
			return config.Effective{}, err
		}
		cfg, err := config.Load(resolvedPath)
		if err != nil {
			return config.Effective{}, err
		}
		return cfg.Resolve(account)
	}, func(_ context.Context, effective config.Effective) (provisioner.Bootstrapper, error) {
		return auth.New(effective)
	}, func(ctx context.Context, bootstrapper provisioner.Bootstrapper, effective config.Effective) (discovery.Result, error) {
		clients, ok := bootstrapper.(*auth.Clients)
		if !ok {
			return discovery.Result{}, fmt.Errorf("authenticated provider has unsupported type %T", bootstrapper)
		}
		return discovery.Discover(ctx, ocidiscovery.New(clients), discovery.Input{
			Account: effective.Account, TenancyID: clients.TenancyOCID, CompartmentID: effective.CompartmentID,
			Region: clients.Region, Shape: effective.Shape, OCPUs: effective.OCPUs, MemoryGB: effective.MemoryGB,
			BootVolumeGB: effective.BootVolumeGB, ImageID: effective.ImageID, OperatingSystem: effective.OperatingSystem,
			OSVersion: effective.OSVersion, VCNID: effective.VCNID, VCNName: effective.VCNName,
			SubnetID: effective.SubnetID, SubnetName: effective.SubnetName, PublicIP: effective.PublicIP,
		})
	}, func(ctx context.Context, bootstrapper provisioner.Bootstrapper, effective config.Effective, discovered discovery.Result, once bool) (capacity.Result, error) {
		clients, ok := bootstrapper.(*auth.Clients)
		if !ok {
			return capacity.Result{}, fmt.Errorf("authenticated provider has unsupported type %T", bootstrapper)
		}
		store := capacity.StateStore{Store: state.New(effective.StateDir), Account: effective.Account, Now: time.Now}
		resume, err := store.Load(discovered.TargetID)
		if err != nil {
			return capacity.Result{}, err
		}
		for index, ad := range discovered.AvailabilityDomains {
			if ad == resume.LastAD {
				resume.NextAD = (index + 1) % len(discovered.AvailabilityDomains)
			}
		}
		watcher := capacity.Watcher{Client: ocicapacity.New(clients), Store: store, Sleeper: capacity.TimerSleeper{}, Random: capacity.CryptoRandom{}, Logger: runner.Logger(), Now: time.Now, Config: capacity.Config{RequestTimeout: effective.RequestTimeout, InitialInterval: effective.RetryMin, MaxInterval: effective.RetryMax, Jitter: .2}}
		return watcher.Watch(ctx, capacity.Input{TargetID: discovered.TargetID, TenancyID: discovered.TenancyID, Shape: effective.Shape, AvailabilityDomains: discovered.AvailabilityDomains, OCPUs: effective.OCPUs, MemoryGB: effective.MemoryGB, Resume: resume, Once: once})
	})
	runner.SetLaunch(func(ctx context.Context, bootstrapper provisioner.Bootstrapper, effective config.Effective, discovered discovery.Result, decision reconcile.Decision, placement capacity.Result, sshKey string) (launch.Instance, error) {
		clients, ok := bootstrapper.(*auth.Clients)
		if !ok {
			return launch.Instance{}, fmt.Errorf("authenticated provider has unsupported type %T", bootstrapper)
		}
		attempt := decision.Attempt
		if attempt == nil {
			attempt = &reconcile.Attempt{}
		}
		store := launch.StateStore{Store: state.New(effective.StateDir), Account: effective.Account, TargetID: discovered.TargetID, Now: time.Now}
		return (launch.Orchestrator{Provider: ocilaunch.New(clients), Store: store, Sleeper: launch.TimerSleeper{}}).Run(ctx, launch.Input{
			Request:            launch.Request{TargetID: discovered.TargetID, Account: effective.Account, CompartmentID: discovered.CompartmentID, AvailabilityDomain: placement.AvailabilityDomain, Shape: effective.Shape, ImageID: discovered.Image.ID, SubnetID: discovered.Subnet.ID, SSHPublicKey: sshKey, OCPUs: effective.OCPUs, MemoryGB: effective.MemoryGB, BootVolumeGB: effective.BootVolumeGB, PublicIP: effective.PublicIP, Attempt: *attempt},
			ExistingInstanceID: decision.InstanceID, RequestTimeout: effective.RequestTimeout, RetryMin: effective.RetryMin, RetryMax: effective.RetryMax,
		})
	})
	runner.SetNotifierFactory(func(effective config.Effective) notification.Notifier {
		if !effective.Notifications.Enabled {
			return nil
		}
		return notification.Telegram{Token: os.Getenv(effective.Notifications.TelegramTokenEnv), ChatID: effective.Notifications.TelegramChat, Timeout: effective.RequestTimeout}
	})
	exitCode := cli.Execute(ctx, os.Args[1:], runner, os.Stdout, os.Stderr)
	stop()
	os.Exit(exitCode)
}
