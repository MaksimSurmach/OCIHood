// Package discovery deterministically resolves the read-only provider resources needed for provisioning.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

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
type Image struct {
	ID, Name, CompartmentID, OperatingSystem, OSVersion string
	CreatedAt                                           time.Time
}
type Shape struct{ Name, Architecture string }
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
	Shapes(context.Context, Query, string) (Page[Shape], error)
	Images(context.Context, Query, string) (Page[Image], error)
	VCNs(context.Context, Query, string) (Page[VCN], error)
	Subnets(context.Context, Query, string) (Page[Subnet], error)
	Instances(context.Context, string, string) (Page[Instance], error)
}

type Query struct{ CompartmentID, Shape, VCNID string }
type Input struct {
	Account, TenancyID, CompartmentID, Region, Shape string
	OCPUs, MemoryGB, BootVolumeGB                    int
	ImageID                                          string
	VCNID, VCNName, SubnetID, SubnetName             string
	PublicIP                                         bool
}
type Result struct {
	Account, TenancyID, CompartmentID, Region string
	ShapeArchitecture                         string
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
	shapes, err := all(ctx, "shapes", func(page string) (Page[Shape], error) {
		return provider.Shapes(ctx, Query{CompartmentID: in.CompartmentID, Shape: in.Shape}, page)
	})
	if err != nil {
		return Result{}, err
	}
	shape, err := selectShape(shapes, in.Shape)
	if err != nil {
		return Result{}, err
	}

	images, err := ListImages(ctx, provider, Query{CompartmentID: in.CompartmentID, Shape: in.Shape})
	if err != nil {
		return Result{}, err
	}
	image, err := selectImage(images, in.ImageID, in.CompartmentID)
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
	return Result{Account: in.Account, TenancyID: in.TenancyID, CompartmentID: in.CompartmentID, Region: in.Region, ShapeArchitecture: shape.Architecture, AvailabilityDomains: ads, Image: image, VCN: vcn, Subnet: subnet, TargetID: targetID, Instances: instances}, nil
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
	if strings.TrimSpace(in.ImageID) == "" {
		return fail(KindInvalid, "image selection", "image_id is required; run `ocihood images list` and check the value")
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

// ListImages returns all current shape-compatible OCI images, newest first.
func ListImages(ctx context.Context, provider Provider, query Query) ([]Image, error) {
	images, err := all(ctx, "images", func(page string) (Page[Image], error) {
		return provider.Images(ctx, query, page)
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(images, func(i, j int) bool {
		if images[i].CreatedAt.Equal(images[j].CreatedAt) {
			return images[i].ID < images[j].ID
		}
		return images[i].CreatedAt.After(images[j].CreatedAt)
	})
	return images, nil
}

func selectShape(items []Shape, name string) (Shape, error) {
	candidates := make([]Shape, 0, len(items))
	for _, shape := range items {
		if shape.Name == name {
			candidates = append(candidates, shape)
		}
	}
	shape, err := one(candidates, "shape selection")
	if err != nil {
		return Shape{}, err
	}
	if shape.Architecture == "" {
		return Shape{}, fail(KindProvider, "shape selection", "provider did not report shape architecture")
	}
	return shape, nil
}

func selectImage(items []Image, id, compartmentID string) (Image, error) {
	if strings.EqualFold(strings.TrimSpace(id), "ubuntu") {
		var selected Image
		bestMajor, bestMinor := -1, -1
		for _, image := range items {
			major, minor, ok := ubuntuVersion(image.OSVersion)
			if image.OperatingSystem == "Canonical Ubuntu" && ok && (major > bestMajor || major == bestMajor && minor > bestMinor) {
				selected, bestMajor, bestMinor = image, major, minor
			}
		}
		if selected.ID != "" {
			return selected, nil
		}
		return Image{}, fail(KindNotFound, "image selection", "no compatible Ubuntu image was found; run `ocihood images list` and check the available images")
	}
	for _, x := range items {
		// Public platform images have no owning compartment in OCI responses.
		if x.ID == id && (x.CompartmentID == "" || x.CompartmentID == compartmentID) {
			return x, nil
		}
	}
	return Image{}, fail(KindNotFound, "image selection", fmt.Sprintf("image_id %q was not found for the configured compartment and shape; run `ocihood images list` and check the value", id))
}

func ubuntuVersion(value string) (int, int, bool) {
	majorText, minorText, ok := strings.Cut(value, ".")
	if !ok || strings.ContainsAny(value, " \t") {
		return 0, 0, false
	}
	major, majorErr := strconv.Atoi(majorText)
	minor, minorErr := strconv.Atoi(minorText)
	return major, minor, majorErr == nil && minorErr == nil
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
