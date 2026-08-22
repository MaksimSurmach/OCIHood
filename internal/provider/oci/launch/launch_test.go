package launch

import (
	"context"
	"testing"

	domain "github.com/MaksimSurmach/OCIHood/internal/launch"
	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

type fakeCompute struct {
	request   core.LaunchInstanceRequest
	instances []core.Instance
}

func (f *fakeCompute) LaunchInstance(_ context.Context, r core.LaunchInstanceRequest) (core.LaunchInstanceResponse, error) {
	f.request = r
	return core.LaunchInstanceResponse{Instance: core.Instance{Id: common.String("instance"), LifecycleState: core.InstanceLifecycleStateStarting}}, nil
}
func (*fakeCompute) GetInstance(_ context.Context, request core.GetInstanceRequest) (core.GetInstanceResponse, error) {
	return core.GetInstanceResponse{Instance: core.Instance{Id: request.InstanceId, LifecycleState: core.InstanceLifecycleStateRunning}}, nil
}
func (f *fakeCompute) ListInstances(context.Context, core.ListInstancesRequest) (core.ListInstancesResponse, error) {
	return core.ListInstancesResponse{Items: f.instances}, nil
}

func TestReconcileFindsOnlyIntendedManagedInstance(t *testing.T) {
	tags := reconcile.OwnershipTags("target", "Prod")
	tags["extra"] = "preserved"
	compute := &fakeCompute{instances: []core.Instance{
		{Id: common.String("intended"), LifecycleState: core.InstanceLifecycleStateRunning, FreeformTags: tags},
		{Id: common.String("unrelated"), LifecycleState: core.InstanceLifecycleStateRunning, FreeformTags: reconcile.OwnershipTags("other", "Prod")},
	}}
	provider := &Provider{compute: compute, network: &fakeNetwork{}}
	got, found, err := provider.Reconcile(t.Context(), domain.Request{TargetID: "target", Account: "Prod", CompartmentID: "compartment"})
	if err != nil || !found || got.ID != "intended" || got.State != "RUNNING" {
		t.Fatalf("got=%+v found=%v err=%v", got, found, err)
	}
}
func (*fakeCompute) ListVnicAttachments(context.Context, core.ListVnicAttachmentsRequest) (core.ListVnicAttachmentsResponse, error) {
	return core.ListVnicAttachmentsResponse{}, nil
}

type fakeNetwork struct{}

func (*fakeNetwork) GetVnic(context.Context, core.GetVnicRequest) (core.GetVnicResponse, error) {
	return core.GetVnicResponse{}, nil
}

func TestLaunchRequestExactResolvedTarget(t *testing.T) {
	compute := &fakeCompute{}
	provider := &Provider{compute: compute, network: &fakeNetwork{}}
	in := domain.Request{TargetID: "target", Account: "Prod", CompartmentID: "compartment", AvailabilityDomain: "AD-2", Shape: "VM.Standard.A1.Flex", ImageID: "image", SubnetID: "subnet", SSHPublicKey: "ssh-ed25519 key", OCPUs: 2, MemoryGB: 12, BootVolumeGB: 50, PublicIP: true, Attempt: reconcile.Attempt{ID: "attempt", RetryToken: "token"}}
	got, err := provider.Launch(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Instance.ID != "instance" {
		t.Fatalf("result=%+v", got)
	}
	r := compute.request
	if value(r.OpcRetryToken) != "token" || value(r.AvailabilityDomain) != "AD-2" || value(r.CompartmentId) != "compartment" || value(r.Shape) != "VM.Standard.A1.Flex" || r.ShapeConfig == nil || *r.ShapeConfig.Ocpus != 2 || *r.ShapeConfig.MemoryInGBs != 12 || r.CreateVnicDetails == nil || value(r.CreateVnicDetails.SubnetId) != "subnet" || !*r.CreateVnicDetails.AssignPublicIp || r.Metadata["ssh_authorized_keys"] != "ssh-ed25519 key" {
		t.Fatalf("request=%s", r.String())
	}
	source, ok := r.SourceDetails.(core.InstanceSourceViaImageDetails)
	if !ok || value(source.ImageId) != "image" || *source.BootVolumeSizeInGBs != 50 {
		t.Fatalf("source=%#v", r.SourceDetails)
	}
	want := reconcile.OwnershipTags("target", "Prod")
	for k, v := range want {
		if r.FreeformTags[k] != v {
			t.Fatalf("tags=%v", r.FreeformTags)
		}
	}
}
