package main

import (
	"log/slog"
	"os"

	"github.com/MaksimSurmach/OCIHood/internal/app"
)

func main() {
	if err := app.Run(os.Stdout, os.Stderr); err != nil {
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("command failed", "error", err)
		os.Exit(1)
	}
}
