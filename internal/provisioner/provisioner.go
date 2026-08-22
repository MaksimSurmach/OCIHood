// Package provisioner coordinates provider-independent provisioning phases.
package provisioner

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/MaksimSurmach/OCIHood/internal/config"
)

// Bootstrapper performs the provider's safe read-only bootstrap operation.
type Bootstrapper interface {
	Validate(context.Context) error
}

// Run contains the non-secret settings and dependencies for one account run.
type Run struct {
	Account      string
	Settings     config.Effective
	Logger       *slog.Logger
	Bootstrapper Bootstrapper
}

// Execute performs the read-only bootstrap phase.
func (r Run) Execute(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.Logger.InfoContext(ctx, "provider bootstrap started", "account", r.Account, "region", r.Settings.Region)
	if err := r.Bootstrapper.Validate(ctx); err != nil {
		return fmt.Errorf("validate provider connectivity: %w", err)
	}
	r.Logger.InfoContext(ctx, "provider bootstrap completed", "account", r.Account, "region", r.Settings.Region)
	return nil
}
