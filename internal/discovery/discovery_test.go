package discovery

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
)

type fakeProvider struct {
	ads        []string
	shapes     map[string]Page[Shape]
	images     map[string]Page[Image]
	vcns       map[string]Page[VCN]
	subnets    map[string]Page[Subnet]
	instances  map[string]Page[Instance]
	fail       string
	calls      []string
	queries    []Query
	shapeQuery Query
}

func (f *fakeProvider) AvailabilityDomains(context.Context, string) ([]string, error) {
	f.calls = append(f.calls, "ads")
	if f.fail == "ads" {
		return nil, errors.New("boom")
	}
	return f.ads, nil
}
func (f *fakeProvider) Shapes(_ context.Context, q Query, p string) (Page[Shape], error) {
	f.calls = append(f.calls, "shapes:"+p)
	f.shapeQuery = q
	if f.fail == "shapes" {
		return Page[Shape]{}, errors.New("boom")
	}
	return f.shapes[p], nil
}
func (f *fakeProvider) Images(_ context.Context, q Query, p string) (Page[Image], error) {
	f.calls = append(f.calls, "images:"+p)
	f.queries = append(f.queries, q)
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
	in := Input{Account: "main", TenancyID: "tenancy", CompartmentID: "compartment", Region: "eu-test-1", Shape: "VM.Standard.A1.Flex", OCPUs: 2, MemoryGB: 12, BootVolumeGB: 50, ImageID: "image-new", VCNName: "main", SubnetName: "public", PublicIP: true}
	f := &fakeProvider{
		ads:       []string{"AD-2", "AD-1"},
		shapes:    map[string]Page[Shape]{"": {Items: []Shape{{Name: "VM.Standard.A1.Flex", Architecture: "aarch64"}}}},
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
	want := Result{Account: "main", TenancyID: "tenancy", CompartmentID: "compartment", Region: "eu-test-1", ShapeArchitecture: "aarch64", AvailabilityDomains: []string{"AD-1", "AD-2"}, Image: Image{ID: "image-new", Name: "Oracle-Linux-9-2026.02", CompartmentID: "compartment", OperatingSystem: "Oracle Linux", OSVersion: "9"}, VCN: VCN{ID: "vcn", Name: "main", CompartmentID: "compartment"}, Subnet: Subnet{ID: "subnet", Name: "public", CompartmentID: "compartment", VCNID: "vcn", AllowsPublicIP: true}, TargetID: target.ID(), Instances: []reconcile.Instance{{ID: "owned", Lifecycle: reconcile.LifecycleActive, Tags: reconcile.OwnershipTags(target.ID(), in.Account)}, {ID: "terminated", Lifecycle: reconcile.LifecycleTerminated, Tags: reconcile.OwnershipTags(target.ID(), in.Account)}, {ID: "unrelated", Lifecycle: reconcile.LifecycleActive, Tags: map[string]string{"shape": "A1"}}}}
	for range 2 {
		got, err := Discover(t.Context(), f, in)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("result mismatch\n got: %#v\nwant: %#v", got, want)
		}
	}
	if !reflect.DeepEqual(f.calls[:7], []string{"ads", "shapes:", "images:", "images:p2", "vcns:", "subnets:", "instances:"}) {
		t.Fatalf("unexpected calls: %v", f.calls)
	}
	if f.shapeQuery.CompartmentID != in.CompartmentID || f.shapeQuery.Shape != in.Shape {
		t.Fatalf("shape query = %#v", f.shapeQuery)
	}
}

func TestDiscoverSelectionFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeProvider, *Input)
		kind   Kind
	}{
		{"zero images", func(f *fakeProvider, _ *Input) { f.images = map[string]Page[Image]{"": {}} }, KindNotFound},
		{"missing shape", func(f *fakeProvider, _ *Input) { f.shapes = map[string]Page[Shape]{"": {}} }, KindNotFound},
		{"missing architecture", func(f *fakeProvider, _ *Input) { f.shapes[""].Items[0].Architecture = "" }, KindProvider},
		{"explicit incompatible image", func(_ *fakeProvider, in *Input) { in.ImageID = "missing" }, KindNotFound},
		{"explicit image in wrong compartment", func(f *fakeProvider, in *Input) {
			in.ImageID = "image-old"
			x := f.images[""].Items[0]
			x.CompartmentID = "other"
			f.images[""] = Page[Image]{Items: []Image{x}}
		}, KindNotFound},
		{"explicit VCN in wrong compartment", func(f *fakeProvider, in *Input) {
			in.VCNID, in.VCNName = "vcn", ""
			x := f.vcns[""].Items[0]
			x.CompartmentID = "other"
			f.vcns[""] = Page[VCN]{Items: []VCN{x}}
		}, KindNotFound},
		{"explicit subnet in wrong VCN", func(f *fakeProvider, in *Input) {
			in.SubnetID, in.SubnetName = "subnet", ""
			x := f.subnets[""].Items[0]
			x.VCNID = "other"
			f.subnets[""] = Page[Subnet]{Items: []Subnet{x}}
		}, KindNotFound},
		{"missing image ID", func(_ *fakeProvider, in *Input) { in.ImageID = "" }, KindInvalid},
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
	for _, stage := range []string{"ads", "shapes", "images", "vcns", "subnets", "instances"} {
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
	if f.queries[0].Shape != in.Shape {
		t.Fatalf("explicit image query = %#v", f.queries[0])
	}
}

func TestImageSelectionUsesPublicOCID(t *testing.T) {
	f, in := fixture()
	in.ImageID = "image-old"
	page := f.images[""]
	page.Items[0].CompartmentID = ""
	f.images[""] = page
	got, err := Discover(t.Context(), f, in)
	if err != nil || got.Image.ID != "image-old" {
		t.Fatalf("public image = %#v, err=%v", got.Image, err)
	}
}

func TestImageIDIsRequired(t *testing.T) {
	f, in := fixture()
	in.ImageID = ""
	_, err := Discover(t.Context(), f, in)
	var discoveryErr *Error
	if !errors.As(err, &discoveryErr) || discoveryErr.Kind != KindInvalid || !strings.Contains(err.Error(), "ocihood images list") {
		t.Fatalf("missing image ID error = %v", err)
	}
}

func TestUnknownImageIDExplainsHowToCheck(t *testing.T) {
	f, in := fixture()
	in.ImageID = "missing-image"
	_, err := Discover(t.Context(), f, in)
	var discoveryErr *Error
	if !errors.As(err, &discoveryErr) || discoveryErr.Kind != KindNotFound || !strings.Contains(err.Error(), `image_id "missing-image" was not found`) || !strings.Contains(err.Error(), "ocihood images list") {
		t.Fatalf("unknown image ID error = %v", err)
	}
}

func TestUbuntuAliasSelectsNewestCompatibleImage(t *testing.T) {
	f, in := fixture()
	in.ImageID = "ubuntu"
	old, latest := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	first := f.images[""]
	first.Items = append(first.Items,
		Image{ID: "ubuntu-22", OperatingSystem: "Canonical Ubuntu", OSVersion: "22.04", CreatedAt: latest},
		Image{ID: "ubuntu-24-minimal", OperatingSystem: "Canonical Ubuntu", OSVersion: "24.04 Minimal aarch64", CreatedAt: latest},
	)
	f.images[""] = first
	second := f.images["p2"]
	second.Items[0] = Image{ID: "ubuntu-24", OperatingSystem: "Canonical Ubuntu", OSVersion: "24.04", CreatedAt: old}
	f.images["p2"] = second
	got, err := Discover(t.Context(), f, in)
	if err != nil || got.Image.ID != "ubuntu-24" {
		t.Fatalf("image=%+v err=%v", got.Image, err)
	}
}

func TestListImagesIsPaginatedAndNewestFirst(t *testing.T) {
	f, _ := fixture()
	old, latest := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	first := f.images[""]
	first.Items[0].CreatedAt = old
	f.images[""] = first
	second := f.images["p2"]
	second.Items[0].CreatedAt = latest
	f.images["p2"] = second
	images, err := ListImages(t.Context(), f, Query{CompartmentID: "compartment", Shape: "VM.Standard.A1.Flex"})
	if err != nil || len(images) != 2 || images[0].ID != "image-new" || images[1].ID != "image-old" {
		t.Fatalf("images=%+v err=%v", images, err)
	}
}
