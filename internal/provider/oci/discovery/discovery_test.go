package discovery

import (
	"context"
	"testing"

	domain "github.com/MaksimSurmach/OCIHood/internal/discovery"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

type fakeIdentity struct {
	request identity.ListAvailabilityDomainsRequest
}

func (f *fakeIdentity) ListAvailabilityDomains(_ context.Context, r identity.ListAvailabilityDomainsRequest) (identity.ListAvailabilityDomainsResponse, error) {
	f.request = r
	return identity.ListAvailabilityDomainsResponse{Items: []identity.AvailabilityDomain{{Name: common.String("AD-1")}}}, nil
}

type fakeCompute struct {
	shape    core.ListShapesRequest
	image    core.ListImagesRequest
	instance core.ListInstancesRequest
}

func (f *fakeCompute) ListShapes(_ context.Context, r core.ListShapesRequest) (core.ListShapesResponse, error) {
	f.shape = r
	return core.ListShapesResponse{Items: []core.Shape{{Shape: common.String("VM.Standard.A1.Flex"), ProcessorDescription: common.String("Ampere Altra")}}, OpcNextPage: common.String("next")}, nil
}

func (f *fakeCompute) ListImages(_ context.Context, r core.ListImagesRequest) (core.ListImagesResponse, error) {
	f.image = r
	return core.ListImagesResponse{Items: []core.Image{{Id: common.String("image"), DisplayName: common.String("Oracle-Linux-9"), CompartmentId: common.String("images"), OperatingSystem: common.String("Oracle Linux"), OperatingSystemVersion: common.String("9")}}, OpcNextPage: common.String("next")}, nil
}
func (f *fakeCompute) ListInstances(_ context.Context, r core.ListInstancesRequest) (core.ListInstancesResponse, error) {
	f.instance = r
	return core.ListInstancesResponse{Items: []core.Instance{{Id: common.String("instance"), LifecycleState: core.InstanceLifecycleStateRunning, FreeformTags: map[string]string{"owned": "true"}}}}, nil
}

type fakeNetwork struct {
	vcn    core.ListVcnsRequest
	subnet core.ListSubnetsRequest
}

func (f *fakeNetwork) ListVcns(_ context.Context, r core.ListVcnsRequest) (core.ListVcnsResponse, error) {
	f.vcn = r
	return core.ListVcnsResponse{Items: []core.Vcn{{Id: common.String("vcn"), DisplayName: common.String("main"), CompartmentId: common.String("compartment")}}}, nil
}
func (f *fakeNetwork) ListSubnets(_ context.Context, r core.ListSubnetsRequest) (core.ListSubnetsResponse, error) {
	f.subnet = r
	return core.ListSubnetsResponse{Items: []core.Subnet{{Id: common.String("subnet"), DisplayName: common.String("public"), CompartmentId: common.String("compartment"), VcnId: common.String("vcn"), ProhibitPublicIpOnVnic: common.Bool(false)}}}, nil
}

func TestProviderMapsReadOnlyRequests(t *testing.T) {
	i, c, n := &fakeIdentity{}, &fakeCompute{}, &fakeNetwork{}
	p := &Provider{identity: i, compute: c, network: n}
	ctx := t.Context()
	ads, err := p.AvailabilityDomains(ctx, "tenancy")
	if err != nil || len(ads) != 1 || ads[0] != "AD-1" {
		t.Fatalf("ADs: %v %v", ads, err)
	}
	shapes, err := p.Shapes(ctx, domain.Query{CompartmentID: "compartment", Shape: "VM.Standard.A1.Flex"}, "page")
	if err != nil || len(shapes.Items) != 1 || shapes.Items[0].Architecture != "aarch64" || shapes.Next != "next" {
		t.Fatalf("shapes: %#v %v", shapes, err)
	}
	images, err := p.Images(ctx, domain.Query{CompartmentID: "compartment", Shape: "shape", OperatingSystem: "Oracle Linux", OSVersion: "9"}, "page")
	if err != nil || len(images.Items) != 1 || images.Next != "next" {
		t.Fatalf("images: %#v %v", images, err)
	}
	vcns, _ := p.VCNs(ctx, domain.Query{CompartmentID: "compartment"}, "page")
	subnets, _ := p.Subnets(ctx, domain.Query{CompartmentID: "compartment", VCNID: "vcn"}, "page")
	instances, _ := p.Instances(ctx, "compartment", "page")
	if *i.request.CompartmentId != "tenancy" || *c.shape.Shape != "VM.Standard.A1.Flex" || *c.shape.Page != "page" || *c.image.CompartmentId != "compartment" || *c.image.Shape != "shape" || *c.image.Page != "page" {
		t.Fatalf("wrong OCI request: %#v", c.image)
	}
	if len(vcns.Items) != 1 || len(subnets.Items) != 1 || !subnets.Items[0].AllowsPublicIP || len(instances.Items) != 1 {
		t.Fatalf("mapping failed: %#v %#v %#v", vcns, subnets, instances)
	}
	if c.instance.Page == nil || n.vcn.Page == nil || n.subnet.Page == nil {
		t.Fatal("pagination token not forwarded")
	}
}

func TestArchitecture(t *testing.T) {
	for processor, want := range map[string]string{"Ampere Altra": "aarch64", "ARM Neoverse": "aarch64", "AMD EPYC": "x86_64", "Intel Xeon": "x86_64", "unknown": ""} {
		if got := architecture(processor); got != want {
			t.Errorf("architecture(%q) = %q, want %q", processor, got, want)
		}
	}
}
