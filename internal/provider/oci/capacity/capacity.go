// Package capacity adapts OCI compute capacity reports to the capacity watcher.
package capacity

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	domain "github.com/MaksimSurmach/OCIHood/internal/capacity"
	"github.com/MaksimSurmach/OCIHood/internal/provider/oci/auth"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

type computeReader interface {
	CreateComputeCapacityReport(context.Context, core.CreateComputeCapacityReportRequest) (core.CreateComputeCapacityReportResponse, error)
}

type Client struct{ compute computeReader }

func New(clients *auth.Clients) *Client { return &Client{compute: &clients.Compute} }

func (c *Client) Probe(ctx context.Context, request domain.Request) (domain.ProbeResult, error) {
	noRetry := common.NoRetryPolicy()
	response, err := c.compute.CreateComputeCapacityReport(ctx, core.CreateComputeCapacityReportRequest{
		CreateComputeCapacityReportDetails: core.CreateComputeCapacityReportDetails{
			CompartmentId: common.String(request.TenancyID), AvailabilityDomain: common.String(request.AvailabilityDomain),
			ShapeAvailabilities: []core.CreateCapacityReportShapeAvailabilityDetails{{
				InstanceShape:       common.String(request.Shape),
				InstanceShapeConfig: &core.CapacityReportInstanceShapeConfig{Ocpus: common.Float32(float32(request.OCPUs)), MemoryInGBs: common.Float32(float32(request.MemoryGB))},
			}},
		},
		RequestMetadata: common.RequestMetadata{RetryPolicy: &noRetry},
	})
	if err != nil {
		result, classified := classify(err)
		if result.Kind == domain.Throttled && response.RawResponse != nil {
			result.RetryAfter = parseRetryAfter(response.RawResponse.Header.Get("Retry-After"), time.Now())
		}
		return result, classified
	}
	if len(response.ShapeAvailabilities) != 1 {
		return domain.ProbeResult{Kind: domain.ProbeUnavailable}, nil
	}
	switch response.ShapeAvailabilities[0].AvailabilityStatus {
	case core.CapacityReportShapeAvailabilityAvailabilityStatusAvailable:
		return domain.ProbeResult{Kind: domain.Available}, nil
	case core.CapacityReportShapeAvailabilityAvailabilityStatusOutOfHostCapacity:
		return domain.ProbeResult{Kind: domain.Unavailable}, nil
	default:
		return domain.ProbeResult{Kind: domain.ProbeUnavailable}, nil
	}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	seconds, err := strconv.Atoi(value)
	if err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	date, err := http.ParseTime(value)
	if err != nil || !date.After(now) {
		return 0
	}
	return date.Sub(now)
}

func classify(err error) (domain.ProbeResult, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return domain.ProbeResult{Kind: domain.Canceled}, err
	}
	var service common.ServiceError
	if errors.As(err, &service) {
		switch status := service.GetHTTPStatusCode(); {
		case status == 429:
			return domain.ProbeResult{Kind: domain.Throttled, RetryAfter: retryAfter(err)}, err
		case status == 403 || status == 404:
			return domain.ProbeResult{Kind: domain.ProbeUnavailable}, err
		case status >= 500:
			return domain.ProbeResult{Kind: domain.Transient}, err
		default:
			return domain.ProbeResult{Kind: domain.Fatal}, err
		}
	}
	var network net.Error
	if errors.As(err, &network) {
		return domain.ProbeResult{Kind: domain.Transient}, err
	}
	return domain.ProbeResult{Kind: domain.Transient}, err
}

// OCI's ServiceError does not expose response headers; honor a Retry-After value when a wrapper does.
func retryAfter(err error) time.Duration {
	type retryAfterer interface{ RetryAfter() time.Duration }
	var retry retryAfterer
	if errors.As(err, &retry) {
		return retry.RetryAfter()
	}
	return 0
}

var _ domain.Client = (*Client)(nil)
