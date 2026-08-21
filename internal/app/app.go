package app

import (
	"fmt"
	"io"
	"log/slog"
)

// Run executes the minimal OCIHood command.
func Run(stdout, stderr io.Writer) error {
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	logger.Info("OCIHood started")

	if _, err := io.WriteString(stdout, "ocihood\n"); err != nil {
		return fmt.Errorf("write command result: %w", err)
	}
	return nil
}
