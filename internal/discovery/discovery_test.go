package discovery

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
)

type fakeProvider struct {
	ads       []string
	images    map[string]Page[Image]
	vcns      map[string]Page[VCN]
	subnets   map[string]Page[Subnet]
	instances map[string]Page[Instance]
	fail      string
	calls     []string
}

func (f *fakeProvider) AvailabilityDomains(context.Context, string) ([]string, error) {
	f.calls = append(f.calls, "ads")
	if f.fail == "ads" {
		return nil, errors.New("boom")
	}
	return f.ads, nil
}
func (f *fakeProvider) Images(_ context.Context, _ Query, p string) (Page[Image], error) {
	f.calls = append(f.calls, "images:"+p)
	if f.fail == "images" {
		return Page[Image]{}, errors.New("boom")
	}
	return f.images[p], nil
}
func (f *fakeProvider) VCNs(_ context.Context, _ Query, p string) (Page[VCN], error) {
	f.calls = append(f.calls, "vcns:"+p)
	if f.fail == "vcns" {
		return Page[VCN]{}, errors.New("boom")
	}
	return f.vcns[p], nil
}
func (f *fakeProvider) Subnets(_ context.Context, _ Query, p string) (Page[Subnet], error) {
	f.calls = append(f.calls, "subnets:"+p)
	if f.fail == "subnets" {
		return Page[Subnet]{}, errors.New("boom")
	}
	return f.subnets[p], nil
}
func (f *fakeProvider) Instances(_ context.Context, _ string, p string) (Page[Instance], error) {
	f.calls = append(f.calls, "instances:"+p)
	if f.fail == "instances" {
		return Page[Instance]{}, errors.New("boom")
	}
	return f.instances[p], nil
}

func fixture() (*fakeProvider, Input) {
	in := Input{Account: "main", TenancyID: "tenancy", CompartmentID: "compartment", Region: "eu-test-1", Shape: "VM.Standard.A1.Flex", OCPUs: 2, MemoryGB: 12, BootVolumeGB: 50, OperatingSystem: "Oracle Linux", OSVersion: "9", VCNName: "main", SubnetName: "public", PublicIP: true}
	f := &fakeProvider{
		ads:       []string{"AD-2", "AD-1"},
		images:    map[string]Page[Image]{"": {Items: []Image{{ID: "image-old", Name: "Oracle-Linux-9-2026.01", CompartmentID: "compartment", OperatingSystem: "Oracle Linux", OSVersion: "9"}}, Next: "p2"}, "p2": {Items: []Image{{ID: "image-new", Name: "Oracle-Linux-9-2026.02", CompartmentID: "compartment", OperatingSystem: "Oracle Linux", OSVersion: "9"}}}},
		vcns:      map[string]Page[VCN]{"": {Items: []VCN{{ID: "vcn", Name: "main", CompartmentID: "compartment"}}}},
		subnets:   map[string]Page[Subnet]{"": {Items: []Subnet{{ID: "subnet", Name: "public", CompartmentID: "compartment", VCNID: "vcn", AllowsPublicIP: true}}}},
		instances: map[string]Page[Instance]{"": {Items: []Instance{{ID: "unrelated", Lifecycle: reconcile.LifecycleActive, Tags: map[string]string{"shape": "A1"}}}, Next: "p2"}, "p2": {Items: []Instance{{ID: "owned", Lifecycle: reconcile.LifecycleActive}, {ID: "terminated", Lifecycle: reconcile.LifecycleTerminated}}}},
	}
	return f, in
}

func TestDiscoverDeterministicAndPaginated(t *testing.T) {
	f, in := fixture()
	target := reconcile.Target{Account: in.Account, Region: in.Region, CompartmentID: in.CompartmentID, SubnetID: "subnet", ImageID: "image-new", Shape: in.Shape, OCPUs: 2, MemoryGB: 12, BootVolumeGB: 50, PublicIP: true}
	f.instances["p2"] = Page[Instance]{Items: []Instance{{ID: "owned", Lifecycle: reconcile.LifecycleActive, Tags: reconcile.OwnershipTags(target.ID(), in.Account)}, {ID: "terminated", Lifecycle: reconcile.LifecycleTerminated, Tags: reconcile.OwnershipTags(target.ID(), in.Account)}}}
	want := Result{Account: "main", TenancyID: "tenancy", CompartmentID: "compartment", Region: "eu-test-1", AvailabilityDomains: []string{"AD-1", "AD-2"}, Image: Image{ID: "image-new", Name: "Oracle-Linux-9-2026.02", CompartmentID: "compartment", OperatingSystem: "Oracle Linux", OSVersion: "9"}, VCN: VCN{ID: "vcn", Name: "main", CompartmentID: "compartment"}, Subnet: Subnet{ID: "subnet", Name: "public", CompartmentID: "compartment", VCNID: "vcn", AllowsPublicIP: true}, TargetID: target.ID(), Instances: []reconcile.Instance{{ID: "owned", Lifecycle: reconcile.LifecycleActive, Tags: reconcile.OwnershipTags(target.ID(), in.Account)}, {ID: "terminated", Lifecycle: reconcile.LifecycleTerminated, Tags: reconcile.OwnershipTags(target.ID(), in.Account)}, {ID: "unrelated", Lifecycle: reconcile.LifecycleActive, Tags: map[string]string{"shape": "A1"}}}}
	for range 2 {
		got, err := Discover(t.Context(), f, in)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("result mismatch\n got: %#v\nwant: %#v", got, want)
		}
	}
	if !reflect.DeepEqual(f.calls[:6], []string{"ads", "images:", "images:p2", "vcns:", "subnets:", "instances:"}) {
		t.Fatalf("unexpected calls: %v", f.calls)
	}
}

func TestDiscoverSelectionFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeProvider, *Input)
		kind   Kind
	}{
		{"zero images", func(f *fakeProvider, _ *Input) { f.images = map[string]Page[Image]{"": {}} }, KindNotFound},
		{"explicit incompatible image", func(f *fakeProvider, in *Input) { in.ImageID = "missing" }, KindNotFound},
		{"ambiguous unfiltered image", func(_ *fakeProvider, in *Input) { in.OperatingSystem = ""; in.OSVersion = "" }, KindAmbiguous},
		{"ambiguous VCN", func(f *fakeProvider, in *Input) {
			in.VCNName = ""
			f.vcns[""] = Page[VCN]{Items: append(f.vcns[""].Items, VCN{ID: "vcn2", CompartmentID: "compartment"})}
		}, KindAmbiguous},
		{"zero VCN", func(f *fakeProvider, _ *Input) { f.vcns = map[string]Page[VCN]{"": {}} }, KindNotFound},
		{"ambiguous subnet", func(f *fakeProvider, in *Input) {
			in.SubnetName = ""
			f.subnets[""] = Page[Subnet]{Items: append(f.subnets[""].Items, Subnet{ID: "subnet2", CompartmentID: "compartment", VCNID: "vcn", AllowsPublicIP: true})}
		}, KindAmbiguous},
		{"zero subnet", func(f *fakeProvider, _ *Input) { f.subnets = map[string]Page[Subnet]{"": {}} }, KindNotFound},
		{"private subnet", func(f *fakeProvider, _ *Input) {
			x := f.subnets[""].Items[0]
			x.AllowsPublicIP = false
			f.subnets[""] = Page[Subnet]{Items: []Subnet{x}}
		}, KindNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, in := fixture()
			tt.mutate(f, &in)
			_, err := Discover(t.Context(), f, in)
			var de *Error
			if !errors.As(err, &de) || de.Kind != tt.kind {
				t.Fatalf("got %v, want kind %s", err, tt.kind)
			}
		})
	}
}

func TestDiscoverRejectsPaginationCycle(t *testing.T) {
	f, in := fixture()
	f.images["p2"] = Page[Image]{Next: "p2"}
	_, err := Discover(t.Context(), f, in)
	var de *Error
	if !errors.As(err, &de) || de.Kind != KindProvider {
		t.Fatalf("got %v", err)
	}
}

func TestDiscoverProviderErrorsAndCancellation(t *testing.T) {
	for _, stage := range []string{"ads", "images", "vcns", "subnets", "instances"} {
		t.Run(stage, func(t *testing.T) {
			f, in := fixture()
			f.fail = stage
			_, err := Discover(t.Context(), f, in)
			var de *Error
			if !errors.As(err, &de) || de.Kind != KindProvider {
				t.Fatalf("got %v", err)
			}
		})
	}
	f, in := fixture()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Discover(ctx, f, in)
	var de *Error
	if !errors.As(err, &de) || de.Kind != KindCanceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestExplicitOverrides(t *testing.T) {
	f, in := fixture()
	in.ImageID = "image-old"
	in.VCNID = "vcn"
	in.VCNName = ""
	in.SubnetID = "subnet"
	in.SubnetName = ""
	got, err := Discover(t.Context(), f, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Image.ID != "image-old" || got.VCN.ID != "vcn" || got.Subnet.ID != "subnet" {
		t.Fatalf("overrides ignored: %#v", got)
	}
}
