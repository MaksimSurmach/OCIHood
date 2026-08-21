package app

import (
	"context"
	"log/slog"
)

// Runner performs one OCIHood provisioning run.
type Runner struct {
	logger *slog.Logger
}

// NewRunner creates the application runner.
func NewRunner(logger *slog.Logger) *Runner {
	return &Runner{logger: logger}
}

// Run performs one provisioning run.
func (r *Runner) Run(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.logger.Info("OCIHood started")
	return "ocihood\n", nil
}
