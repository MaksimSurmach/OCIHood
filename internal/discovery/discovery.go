// Package discovery deterministically resolves the read-only provider resources needed for provisioning.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
)

// Kind classifies expected discovery failures.
type Kind string

const (
	KindNotFound  Kind = "not_found"
	KindAmbiguous Kind = "ambiguous"
	KindInvalid   Kind = "invalid"
	KindProvider  Kind = "provider"
	KindCanceled  Kind = "canceled"
)

// Error is a stage-specific, classifiable discovery failure.
type Error struct {
	Kind  Kind
	Stage string
	Err   error
}

func (e *Error) Error() string {
	return fmt.Sprintf("discover %s failed (%s): %v", e.Stage, e.Kind, e.Err)
}
func (e *Error) Unwrap() error { return e.Err }

type Page[T any] struct {
	Items []T
	Next  string
}
type Image struct{ ID, Name, CompartmentID, OperatingSystem, OSVersion string }
type VCN struct{ ID, Name, CompartmentID string }
type Subnet struct {
	ID, Name, CompartmentID, VCNID, AvailabilityDomain string
	AllowsPublicIP                                     bool
}
type Instance struct {
	ID        string
	Lifecycle reconcile.Lifecycle
	Tags      map[string]string
}

// Provider is the minimal read-only resource API consumed by discovery.
type Provider interface {
	AvailabilityDomains(context.Context, string) ([]string, error)
	Images(context.Context, Query, string) (Page[Image], error)
	VCNs(context.Context, Query, string) (Page[VCN], error)
	Subnets(context.Context, Query, string) (Page[Subnet], error)
	Instances(context.Context, string, string) (Page[Instance], error)
}

type Query struct{ CompartmentID, Shape, OperatingSystem, OSVersion, VCNID string }
type Input struct {
	Account, TenancyID, CompartmentID, Region, Shape string
	OCPUs, MemoryGB, BootVolumeGB                    int
	ImageID, OperatingSystem, OSVersion              string
	VCNID, VCNName, SubnetID, SubnetName             string
	PublicIP                                         bool
}
type Result struct {
	Account, TenancyID, CompartmentID, Region string
	AvailabilityDomains                       []string
	Image                                     Image
	VCN                                       VCN
	Subnet                                    Subnet
	TargetID                                  string
	Instances                                 []reconcile.Instance
}

// Discover performs bounded read-only discovery and returns no partial result on failure.
func Discover(ctx context.Context, provider Provider, in Input) (Result, error) {
	if err := validate(in); err != nil {
		return Result{}, err
	}
	ads, err := provider.AvailabilityDomains(ctx, in.TenancyID)
	if err != nil {
		return Result{}, wrap("availability domains", err)
	}
	if len(ads) == 0 {
		return Result{}, fail(KindNotFound, "availability domains", "no availability domains found")
	}
	sort.Strings(ads)

	imageQuery := Query{CompartmentID: in.CompartmentID, Shape: in.Shape}
	if in.ImageID == "" {
		imageQuery.OperatingSystem, imageQuery.OSVersion = in.OperatingSystem, in.OSVersion
	}
	images, err := all(ctx, "images", func(page string) (Page[Image], error) {
		return provider.Images(ctx, imageQuery, page)
	})
	if err != nil {
		return Result{}, err
	}
	image, err := selectImage(images, in)
	if err != nil {
		return Result{}, err
	}

	vcns, err := all(ctx, "VCNs", func(page string) (Page[VCN], error) {
		return provider.VCNs(ctx, Query{CompartmentID: in.CompartmentID}, page)
	})
	if err != nil {
		return Result{}, err
	}
	vcn, err := selectVCN(vcns, in)
	if err != nil {
		return Result{}, err
	}

	subnets, err := all(ctx, "subnets", func(page string) (Page[Subnet], error) {
		return provider.Subnets(ctx, Query{CompartmentID: in.CompartmentID, VCNID: vcn.ID}, page)
	})
	if err != nil {
		return Result{}, err
	}
	subnet, err := selectSubnet(subnets, in, vcn.ID)
	if err != nil {
		return Result{}, err
	}

	target := reconcile.Target{Account: in.Account, Region: in.Region, CompartmentID: in.CompartmentID, SubnetID: subnet.ID, ImageID: image.ID, Shape: in.Shape, OCPUs: in.OCPUs, MemoryGB: in.MemoryGB, BootVolumeGB: in.BootVolumeGB, PublicIP: in.PublicIP}
	targetID := target.ID()
	observed, err := all(ctx, "instances", func(page string) (Page[Instance], error) { return provider.Instances(ctx, in.CompartmentID, page) })
	if err != nil {
		return Result{}, err
	}
	instances := make([]reconcile.Instance, 0, len(observed))
	for _, instance := range observed {
		instances = append(instances, reconcile.Instance{ID: instance.ID, Lifecycle: instance.Lifecycle, Tags: instance.Tags})
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].ID < instances[j].ID })
	return Result{Account: in.Account, TenancyID: in.TenancyID, CompartmentID: in.CompartmentID, Region: in.Region, AvailabilityDomains: ads, Image: image, VCN: vcn, Subnet: subnet, TargetID: targetID, Instances: instances}, nil
}

func validate(in Input) error {
	for name, value := range map[string]string{"account": in.Account, "tenancy": in.TenancyID, "compartment": in.CompartmentID, "region": in.Region, "shape": in.Shape} {
		if strings.TrimSpace(value) == "" {
			return fail(KindInvalid, "input", name+" is required")
		}
	}
	if in.OCPUs <= 0 || in.MemoryGB <= 0 || in.BootVolumeGB <= 0 {
		return fail(KindInvalid, "input", "ocpus, memory_gb and boot_volume_gb must be positive")
	}
	if in.VCNID != "" && in.VCNName != "" {
		return fail(KindInvalid, "input", "vcn_id and vcn_name are mutually exclusive")
	}
	if in.SubnetID != "" && in.SubnetName != "" {
		return fail(KindInvalid, "input", "subnet_id and subnet_name are mutually exclusive")
	}
	return nil
}

func all[T any](ctx context.Context, stage string, fetch func(string) (Page[T], error)) ([]T, error) {
	var result []T
	seen := map[string]bool{}
	for page := ""; ; {
		if err := ctx.Err(); err != nil {
			return nil, wrap(stage, err)
		}
		p, err := fetch(page)
		if err != nil {
			return nil, wrap(stage, err)
		}
		result = append(result, p.Items...)
		if p.Next == "" {
			return result, nil
		}
		if seen[p.Next] {
			return nil, fail(KindProvider, stage, "provider repeated a pagination token")
		}
		seen[p.Next] = true
		page = p.Next
	}
}

func selectImage(items []Image, in Input) (Image, error) {
	if in.ImageID != "" {
		return exactImage(items, in.ImageID, in.CompartmentID)
	}
	items = keepImages(items, in)
	if len(items) == 0 {
		return Image{}, fail(KindNotFound, "image selection", "no compatible image found")
	}
	if len(items) > 1 && in.OperatingSystem == "" && in.OSVersion == "" {
		return Image{}, fail(KindAmbiguous, "image selection", fmt.Sprintf("found %d candidates; configure an explicit OCID or OS filters", len(items)))
	}
	// Newest platform images sort lexically by OCI display name; ID is the stable tie-breaker.
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].ID > items[j].ID
		}
		return items[i].Name > items[j].Name
	})
	return items[0], nil
}
func keepImages(items []Image, in Input) []Image {
	out := items[:0]
	for _, x := range items {
		if (in.OperatingSystem == "" || x.OperatingSystem == in.OperatingSystem) && (in.OSVersion == "" || x.OSVersion == in.OSVersion) {
			out = append(out, x)
		}
	}
	return out
}
func exactImage(items []Image, id, compartmentID string) (Image, error) {
	for _, x := range items {
		if x.ID == id && x.CompartmentID == compartmentID {
			return x, nil
		}
	}
	return Image{}, fail(KindNotFound, "image selection", "explicit image is unavailable or incompatible")
}
func selectVCN(items []VCN, in Input) (VCN, error) {
	candidates := make([]VCN, 0)
	for _, x := range items {
		if x.CompartmentID == in.CompartmentID && (in.VCNID == "" || x.ID == in.VCNID) && (in.VCNName == "" || x.Name == in.VCNName) {
			candidates = append(candidates, x)
		}
	}
	return one(candidates, "VCN selection")
}
func selectSubnet(items []Subnet, in Input, vcnID string) (Subnet, error) {
	candidates := make([]Subnet, 0)
	for _, x := range items {
		if x.CompartmentID == in.CompartmentID && x.VCNID == vcnID && (in.SubnetID == "" || x.ID == in.SubnetID) && (in.SubnetName == "" || x.Name == in.SubnetName) && (!in.PublicIP || x.AllowsPublicIP) {
			candidates = append(candidates, x)
		}
	}
	return one(candidates, "subnet selection")
}
func one[T any](items []T, stage string) (T, error) {
	var zero T
	if len(items) == 0 {
		return zero, fail(KindNotFound, stage, "no compatible candidate found")
	}
	if len(items) > 1 {
		return zero, fail(KindAmbiguous, stage, fmt.Sprintf("found %d candidates; configure an explicit OCID or unique name", len(items)))
	}
	return items[0], nil
}
func fail(kind Kind, stage, message string) error {
	return &Error{Kind: kind, Stage: stage, Err: errors.New(message)}
}
func wrap(stage string, err error) error {
	kind := KindProvider
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		kind = KindCanceled
	}
	return &Error{Kind: kind, Stage: stage, Err: err}
}
