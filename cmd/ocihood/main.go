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
	"github.com/MaksimSurmach/OCIHood/internal/provider/oci/auth"
	ocicapacity "github.com/MaksimSurmach/OCIHood/internal/provider/oci/capacity"
	ocidiscovery "github.com/MaksimSurmach/OCIHood/internal/provider/oci/discovery"
	"github.com/MaksimSurmach/OCIHood/internal/provisioner"
	"github.com/MaksimSurmach/OCIHood/internal/state"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := app.NewRunner(logger, func(_ context.Context, path, account string) (config.Effective, error) {
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
	}, func(ctx context.Context, bootstrapper provisioner.Bootstrapper, effective config.Effective, discovered discovery.Result) (capacity.Result, error) {
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
		watcher := capacity.Watcher{Client: ocicapacity.New(clients), Store: store, Sleeper: capacity.TimerSleeper{}, Random: capacity.CryptoRandom{}, Logger: logger, Now: time.Now, Config: capacity.Config{RequestTimeout: effective.RequestTimeout, InitialInterval: effective.RetryMin, MaxInterval: effective.RetryMax, Jitter: .2}}
		return watcher.Watch(ctx, capacity.Input{TargetID: discovered.TargetID, TenancyID: discovered.TenancyID, Shape: effective.Shape, AvailabilityDomains: discovered.AvailabilityDomains, OCPUs: effective.OCPUs, MemoryGB: effective.MemoryGB, Resume: resume})
	})
	exitCode := cli.Execute(ctx, os.Args[1:], runner, os.Stdout, os.Stderr)
	stop()
	os.Exit(exitCode)
}
