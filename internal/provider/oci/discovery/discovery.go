// Package discovery adapts OCI SDK read APIs to the provider-independent discovery contract.
package discovery

import (
	"context"

	domain "github.com/MaksimSurmach/OCIHood/internal/discovery"
	"github.com/MaksimSurmach/OCIHood/internal/provider/oci/auth"
	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

type identityReader interface {
	ListAvailabilityDomains(context.Context, identity.ListAvailabilityDomainsRequest) (identity.ListAvailabilityDomainsResponse, error)
}
type computeReader interface {
	ListImages(context.Context, core.ListImagesRequest) (core.ListImagesResponse, error)
	ListInstances(context.Context, core.ListInstancesRequest) (core.ListInstancesResponse, error)
}
type networkReader interface {
	ListVcns(context.Context, core.ListVcnsRequest) (core.ListVcnsResponse, error)
	ListSubnets(context.Context, core.ListSubnetsRequest) (core.ListSubnetsResponse, error)
}

// Provider exposes only OCI read operations; its interface cannot mutate cloud resources.
type Provider struct {
	identity identityReader
	compute  computeReader
	network  networkReader
}

func New(clients *auth.Clients) *Provider {
	return &Provider{identity: &clients.Identity, compute: &clients.Compute, network: &clients.VirtualNetwork}
}

func (p *Provider) AvailabilityDomains(ctx context.Context, tenancy string) ([]string, error) {
	r, err := p.identity.ListAvailabilityDomains(ctx, identity.ListAvailabilityDomainsRequest{CompartmentId: common.String(tenancy)})
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(r.Items))
	for _, x := range r.Items {
		if x.Name != nil {
			result = append(result, *x.Name)
		}
	}
	return result, nil
}
func (p *Provider) Images(ctx context.Context, q domain.Query, page string) (domain.Page[domain.Image], error) {
	r, err := p.compute.ListImages(ctx, core.ListImagesRequest{CompartmentId: common.String(q.CompartmentID), Shape: optional(q.Shape), OperatingSystem: optional(q.OperatingSystem), OperatingSystemVersion: optional(q.OSVersion), Page: optional(page), LifecycleState: core.ImageLifecycleStateAvailable})
	if err != nil {
		return domain.Page[domain.Image]{}, err
	}
	items := make([]domain.Image, 0, len(r.Items))
	for _, x := range r.Items {
		items = append(items, domain.Image{ID: value(x.Id), Name: value(x.DisplayName), CompartmentID: value(x.CompartmentId), OperatingSystem: value(x.OperatingSystem), OSVersion: value(x.OperatingSystemVersion)})
	}
	return domain.Page[domain.Image]{Items: items, Next: value(r.OpcNextPage)}, nil
}
func (p *Provider) VCNs(ctx context.Context, q domain.Query, page string) (domain.Page[domain.VCN], error) {
	r, err := p.network.ListVcns(ctx, core.ListVcnsRequest{CompartmentId: common.String(q.CompartmentID), Page: optional(page), LifecycleState: core.VcnLifecycleStateAvailable})
	if err != nil {
		return domain.Page[domain.VCN]{}, err
	}
	items := make([]domain.VCN, 0, len(r.Items))
	for _, x := range r.Items {
		items = append(items, domain.VCN{ID: value(x.Id), Name: value(x.DisplayName), CompartmentID: value(x.CompartmentId)})
	}
	return domain.Page[domain.VCN]{Items: items, Next: value(r.OpcNextPage)}, nil
}
func (p *Provider) Subnets(ctx context.Context, q domain.Query, page string) (domain.Page[domain.Subnet], error) {
	r, err := p.network.ListSubnets(ctx, core.ListSubnetsRequest{CompartmentId: common.String(q.CompartmentID), VcnId: common.String(q.VCNID), Page: optional(page), LifecycleState: core.SubnetLifecycleStateAvailable})
	if err != nil {
		return domain.Page[domain.Subnet]{}, err
	}
	items := make([]domain.Subnet, 0, len(r.Items))
	for _, x := range r.Items {
		items = append(items, domain.Subnet{ID: value(x.Id), Name: value(x.DisplayName), CompartmentID: value(x.CompartmentId), VCNID: value(x.VcnId), AvailabilityDomain: value(x.AvailabilityDomain), AllowsPublicIP: x.ProhibitPublicIpOnVnic == nil || !*x.ProhibitPublicIpOnVnic})
	}
	return domain.Page[domain.Subnet]{Items: items, Next: value(r.OpcNextPage)}, nil
}
func (p *Provider) Instances(ctx context.Context, compartment, page string) (domain.Page[domain.Instance], error) {
	r, err := p.compute.ListInstances(ctx, core.ListInstancesRequest{CompartmentId: common.String(compartment), Page: optional(page)})
	if err != nil {
		return domain.Page[domain.Instance]{}, err
	}
	items := make([]domain.Instance, 0, len(r.Items))
	for _, x := range r.Items {
		items = append(items, domain.Instance{ID: value(x.Id), Lifecycle: lifecycle(x.LifecycleState), Tags: x.FreeformTags})
	}
	return domain.Page[domain.Instance]{Items: items, Next: value(r.OpcNextPage)}, nil
}
func lifecycle(state core.InstanceLifecycleStateEnum) reconcile.Lifecycle {
	if state == core.InstanceLifecycleStateTerminated {
		return reconcile.LifecycleTerminated
	}
	if state == core.InstanceLifecycleStateRunning || state == core.InstanceLifecycleStateStarting || state == core.InstanceLifecycleStateStopping || state == core.InstanceLifecycleStateStopped {
		return reconcile.LifecycleActive
	}
	return reconcile.LifecycleUnknown
}
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return common.String(s)
}
func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

var _ domain.Provider = (*Provider)(nil)
