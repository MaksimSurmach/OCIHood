// Package launch adapts OCI instance APIs to the launch contract.
package launch

import (
	"context"
	"errors"
	"net"
	"strings"

	domain "github.com/MaksimSurmach/OCIHood/internal/launch"
	"github.com/MaksimSurmach/OCIHood/internal/provider/oci/auth"
	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

type computeClient interface {
	LaunchInstance(context.Context, core.LaunchInstanceRequest) (core.LaunchInstanceResponse, error)
	GetInstance(context.Context, core.GetInstanceRequest) (core.GetInstanceResponse, error)
	ListVnicAttachments(context.Context, core.ListVnicAttachmentsRequest) (core.ListVnicAttachmentsResponse, error)
}
type networkClient interface {
	GetVnic(context.Context, core.GetVnicRequest) (core.GetVnicResponse, error)
}

type Provider struct {
	compute computeClient
	network networkClient
}

func New(clients *auth.Clients) *Provider {
	return &Provider{compute: &clients.Compute, network: &clients.VirtualNetwork}
}

func (p *Provider) Launch(ctx context.Context, in domain.Request) (domain.Result, error) {
	ocpus, memory := float32(in.OCPUs), float32(in.MemoryGB)
	boot := int64(in.BootVolumeGB)
	noRetry := common.NoRetryPolicy()
	request := core.LaunchInstanceRequest{
		OpcRetryToken:   common.String(in.Attempt.RetryToken),
		RequestMetadata: common.RequestMetadata{RetryPolicy: &noRetry},
		LaunchInstanceDetails: core.LaunchInstanceDetails{
			AvailabilityDomain: common.String(in.AvailabilityDomain), CompartmentId: common.String(in.CompartmentID), Shape: common.String(in.Shape),
			ShapeConfig:       &core.LaunchInstanceShapeConfigDetails{Ocpus: &ocpus, MemoryInGBs: &memory},
			SourceDetails:     core.InstanceSourceViaImageDetails{ImageId: common.String(in.ImageID), BootVolumeSizeInGBs: &boot},
			CreateVnicDetails: &core.CreateVnicDetails{SubnetId: common.String(in.SubnetID), AssignPublicIp: common.Bool(in.PublicIP)},
			Metadata:          map[string]string{"ssh_authorized_keys": in.SSHPublicKey}, FreeformTags: reconcile.OwnershipTags(in.TargetID, in.Account),
		},
	}
	response, err := p.compute.LaunchInstance(ctx, request)
	if err != nil {
		kind := classify(err)
		return domain.Result{Kind: kind}, err
	}
	return domain.Result{Kind: domain.Accepted, Instance: domain.Instance{ID: value(response.Id), State: string(response.LifecycleState)}}, nil
}

func (p *Provider) Get(ctx context.Context, compartmentID, instanceID string) (domain.Instance, error) {
	response, err := p.compute.GetInstance(ctx, core.GetInstanceRequest{InstanceId: common.String(instanceID)})
	if err != nil {
		return domain.Instance{}, &domain.Error{Kind: classify(err), Err: err}
	}
	result := domain.Instance{ID: value(response.Id), State: string(response.LifecycleState)}
	attachments, err := p.compute.ListVnicAttachments(ctx, core.ListVnicAttachmentsRequest{CompartmentId: common.String(compartmentID), InstanceId: common.String(instanceID)})
	if err != nil {
		return result, nil
	}
	for _, attachment := range attachments.Items {
		if attachment.VnicId == nil {
			continue
		}
		vnic, getErr := p.network.GetVnic(ctx, core.GetVnicRequest{VnicId: attachment.VnicId})
		if getErr == nil && vnic.PublicIp != nil {
			result.PublicIP = *vnic.PublicIp
			break
		}
	}
	return result, nil
}

func classify(err error) domain.Kind {
	if errors.Is(err, context.Canceled) {
		return domain.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.Ambiguous
	}
	var service common.ServiceError
	if errors.As(err, &service) {
		code := strings.ToLower(service.GetCode())
		switch {
		case strings.Contains(code, "outofhostcapacity"), strings.Contains(code, "capacity"):
			return domain.OutOfCapacity
		case strings.Contains(code, "limitexceeded"):
			return domain.LimitExceeded
		case service.GetHTTPStatusCode() == 429 || service.GetHTTPStatusCode() >= 500:
			return domain.Transient
		default:
			return domain.Fatal
		}
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			return domain.Ambiguous
		}
		return domain.Transient
	}
	return domain.Transient
}

func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

var _ domain.Provider = (*Provider)(nil)
