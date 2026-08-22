// Package auth constructs isolated OCI SDK clients from resolved account configuration.
package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/MaksimSurmach/OCIHood/internal/config"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// Kind classifies authentication failures without exposing credentials.
type Kind string

const (
	KindLocal          Kind = "local_credentials"
	KindAuthentication Kind = "authentication"
	KindAuthorization  Kind = "authorization"
	KindNetwork        Kind = "network"
	KindCanceled       Kind = "canceled"
)

// Error is a sanitized, classifiable authentication error.
type Error struct {
	Kind Kind
	Op   string
	err  error
}

func (e *Error) Error() string { return fmt.Sprintf("oci authentication %s failed (%s)", e.Op, e.Kind) }
func (e *Error) Unwrap() error { return e.err }

// Clients contains the isolated SDK clients used by OCIHood's discovery and provisioning layers.
type Clients struct {
	Identity       identity.IdentityClient
	Compute        core.ComputeClient
	VirtualNetwork core.VirtualNetworkClient
	TenancyOCID    string
	UserOCID       string
	Region         string

	identityReader tenancyReader
}

type tenancyReader interface {
	GetTenancy(context.Context, identity.GetTenancyRequest) (identity.GetTenancyResponse, error)
}

type regionProvider struct {
	common.ConfigurationProvider
	region string
}

func (p regionProvider) Region() (string, error) {
	return common.NewRawConfigurationProvider("x", "x", p.region, "x", "x", nil).Region()
}

// New constructs an independent client set and validates all local credential references.
// It does not call OCI; call Validate before provisioning.
func New(account config.Effective) (*Clients, error) {
	provider, err := common.ConfigurationProviderFromFileWithProfile(account.OCIConfigPath, account.OCIProfile, "")
	if err != nil {
		return nil, localError("load profile", err)
	}
	if account.Region != "" {
		provider = regionProvider{ConfigurationProvider: provider, region: account.Region}
	}

	tenancy, err := provider.TenancyOCID()
	if err != nil {
		return nil, localError("resolve tenancy", err)
	}
	user, err := provider.UserOCID()
	if err != nil {
		return nil, localError("resolve user", err)
	}
	region, err := provider.Region()
	if err != nil {
		return nil, localError("resolve region", err)
	}
	if _, err := provider.KeyFingerprint(); err != nil {
		return nil, localError("resolve fingerprint", err)
	}
	if _, err := provider.PrivateRSAKey(); err != nil {
		return nil, localError("load signing key", err)
	}

	identityClient, err := identity.NewIdentityClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, localError("create identity client", err)
	}
	computeClient, err := core.NewComputeClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, localError("create compute client", err)
	}
	networkClient, err := core.NewVirtualNetworkClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, localError("create network client", err)
	}

	clients := &Clients{
		Identity: identityClient, Compute: computeClient, VirtualNetwork: networkClient,
		TenancyOCID: tenancy, UserOCID: user, Region: region,
	}
	clients.identityReader = &clients.Identity
	return clients, nil
}

func localError(op string, err error) error { return &Error{Kind: KindLocal, Op: op, err: err} }

// Validate performs one read-only Identity call using the caller's context.
func (c *Clients) Validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return &Error{Kind: KindCanceled, Op: "validate connectivity", err: err}
	}
	_, err := c.identityReader.GetTenancy(ctx, identity.GetTenancyRequest{TenancyId: common.String(c.TenancyOCID)})
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: KindCanceled, Op: "validate connectivity", err: err}
	}
	kind := KindNetwork
	var serviceError common.ServiceError
	if errors.As(err, &serviceError) {
		switch serviceError.GetHTTPStatusCode() {
		case 401:
			kind = KindAuthentication
		case 403:
			kind = KindAuthorization
		}
	}
	return &Error{Kind: kind, Op: "validate connectivity", err: err}
}
