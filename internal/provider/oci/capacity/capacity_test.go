package capacity

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "github.com/MaksimSurmach/OCIHood/internal/capacity"
	"github.com/oracle/oci-go-sdk/v65/core"
)

type fakeCompute struct {
	request  core.CreateComputeCapacityReportRequest
	response core.CreateComputeCapacityReportResponse
	err      error
}

func (f *fakeCompute) CreateComputeCapacityReport(_ context.Context, request core.CreateComputeCapacityReportRequest) (core.CreateComputeCapacityReportResponse, error) {
	f.request = request
	return f.response, f.err
}

type serviceError struct {
	status int
	retry  time.Duration
}

func (e serviceError) Error() string             { return "oci error" }
func (e serviceError) GetHTTPStatusCode() int    { return e.status }
func (e serviceError) GetMessage() string        { return "redacted" }
func (e serviceError) GetCode() string           { return "code" }
func (e serviceError) GetOpcRequestID() string   { return "request" }
func (e serviceError) RetryAfter() time.Duration { return e.retry }

func TestClientProbeRequestAndAvailability(t *testing.T) {
	compute := &fakeCompute{response: core.CreateComputeCapacityReportResponse{ComputeCapacityReport: core.ComputeCapacityReport{ShapeAvailabilities: []core.CapacityReportShapeAvailability{{AvailabilityStatus: core.CapacityReportShapeAvailabilityAvailabilityStatusAvailable}}}}}
	client := &Client{compute: compute}
	got, err := client.Probe(t.Context(), domain.Request{TenancyID: "root", AvailabilityDomain: "AD-1", Shape: "shape", OCPUs: 2, MemoryGB: 12})
	if err != nil || got.Kind != domain.Available {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	details := compute.request.CreateComputeCapacityReportDetails
	shape := details.ShapeAvailabilities[0]
	if *details.CompartmentId != "root" || *details.AvailabilityDomain != "AD-1" || *shape.InstanceShape != "shape" || *shape.InstanceShapeConfig.Ocpus != 2 || *shape.InstanceShapeConfig.MemoryInGBs != 12 {
		t.Fatalf("request = %+v", compute.request)
	}
	if compute.request.RetryPolicy() == nil || compute.request.RetryPolicy().MaximumNumberAttempts != 1 {
		t.Fatalf("SDK retry policy = %+v, want exactly one request", compute.request.RetryPolicy())
	}
}

func TestClientProbeClassifiesProviderFailures(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		want  domain.Kind
		retry time.Duration
	}{
		{name: "throttled", err: serviceError{status: 429, retry: 7 * time.Second}, want: domain.Throttled, retry: 7 * time.Second},
		{name: "probe forbidden", err: serviceError{status: 403}, want: domain.ProbeUnavailable},
		{name: "probe unsupported", err: serviceError{status: 404}, want: domain.ProbeUnavailable},
		{name: "transient", err: serviceError{status: 503}, want: domain.Transient},
		{name: "fatal authentication", err: serviceError{status: 401}, want: domain.Fatal},
		{name: "canceled", err: context.Canceled, want: domain.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := (&Client{compute: &fakeCompute{err: tt.err}}).Probe(t.Context(), domain.Request{})
			if got.Kind != tt.want || got.RetryAfter != tt.retry || !errors.Is(err, tt.err) {
				t.Fatalf("result=%+v err=%v", got, err)
			}
		})
	}
}
