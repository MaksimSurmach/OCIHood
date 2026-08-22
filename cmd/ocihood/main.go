package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/MaksimSurmach/OCIHood/internal/app"
	"github.com/MaksimSurmach/OCIHood/internal/cli"
	"github.com/MaksimSurmach/OCIHood/internal/config"
	"github.com/MaksimSurmach/OCIHood/internal/provider/oci/auth"
	"github.com/MaksimSurmach/OCIHood/internal/provisioner"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := app.NewRunner(logger, func(path, account string) (config.Effective, error) {
		resolvedPath, err := config.Path(path)
		if err != nil {
			return config.Effective{}, err
		}
		cfg, err := config.Load(resolvedPath)
		if err != nil {
			return config.Effective{}, err
		}
		return cfg.Resolve(account)
	}, func(effective config.Effective) (provisioner.Bootstrapper, error) {
		return auth.New(effective)
	})
	exitCode := cli.Execute(ctx, os.Args[1:], runner, os.Stdout, os.Stderr)
	stop()
	os.Exit(exitCode)
}
