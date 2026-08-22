package oci_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	capacitydomain "github.com/MaksimSurmach/OCIHood/internal/capacity"
	"github.com/MaksimSurmach/OCIHood/internal/config"
	discoverydomain "github.com/MaksimSurmach/OCIHood/internal/discovery"
	launchdomain "github.com/MaksimSurmach/OCIHood/internal/launch"
	"github.com/MaksimSurmach/OCIHood/internal/provider/oci/auth"
	ocicapacity "github.com/MaksimSurmach/OCIHood/internal/provider/oci/capacity"
	ocidiscovery "github.com/MaksimSurmach/OCIHood/internal/provider/oci/discovery"
	ocilaunch "github.com/MaksimSurmach/OCIHood/internal/provider/oci/launch"
	"github.com/MaksimSurmach/OCIHood/internal/reconcile"
	"github.com/oracle/oci-go-sdk/v65/common"
)

type capturedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

type expectedRequest struct {
	Method  string
	Target  string
	Respond http.HandlerFunc
}

type localOnlyDispatcher struct {
	allowed *url.URL
	next    common.HTTPRequestDispatcher
	mu      sync.Mutex
	blocked int
}

func (d *localOnlyDispatcher) Do(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != d.allowed.Scheme || r.URL.Host != d.allowed.Host {
		d.mu.Lock()
		d.blocked++
		d.mu.Unlock()
		return nil, fmt.Errorf("external endpoint blocked: %s://%s", r.URL.Scheme, r.URL.Host)
	}
	return d.next.Do(r)
}

type fakeOCI struct {
	server     *httptest.Server
	dispatcher *localOnlyDispatcher
	mu         sync.Mutex
	expected   []expectedRequest
	next       int
}

func newFakeOCI(t *testing.T, expected ...expectedRequest) *fakeOCI {
	t.Helper()
	f := &fakeOCI{expected: expected}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		f.mu.Lock()
		if f.next >= len(f.expected) {
			f.mu.Unlock()
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		expect := f.expected[f.next]
		f.next++
		f.mu.Unlock()
		if r.Method != expect.Method || r.URL.RequestURI() != expect.Target {
			t.Errorf("request %d: got %s %s, want %s %s", f.next, r.Method, r.URL.RequestURI(), expect.Method, expect.Target)
			http.Error(w, "wrong request", http.StatusInternalServerError)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		expect.Respond(w, r)
	}))
	serverURL, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	f.dispatcher = &localOnlyDispatcher{allowed: serverURL, next: f.server.Client()}
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.next != len(f.expected) {
			t.Errorf("consumed %d of %d expected requests", f.next, len(f.expected))
		}
	})
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOCI) clients(t *testing.T) *auth.Clients {
	t.Helper()
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "key.pem")
	keyData := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyPath, keyData, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config")
	profile := fmt.Sprintf("[TEST]\ntenancy=ocid1.tenancy.oc1..test\nuser=ocid1.user.oc1..test\nfingerprint=aa:bb\nkey_file=%s\nregion=eu-frankfurt-1\n", keyPath)
	if err := os.WriteFile(configPath, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	clients, err := auth.New(config.Effective{OCIConfigPath: configPath, OCIProfile: "TEST"})
	if err != nil {
		t.Fatal(err)
	}
	clients.Identity.Host = f.server.URL
	clients.Compute.Host = f.server.URL
	clients.VirtualNetwork.Host = f.server.URL
	clients.Identity.HTTPClient = f.dispatcher
	clients.Compute.HTTPClient = f.dispatcher
	clients.VirtualNetwork.HTTPClient = f.dispatcher
	noRetry := common.NoRetryPolicy()
	clients.Identity.Configuration.RetryPolicy = &noRetry
	clients.Compute.Configuration.RetryPolicy = &noRetry
	clients.VirtualNetwork.Configuration.RetryPolicy = &noRetry
	return clients
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func TestSDKContractReadFlowsAndPagination(t *testing.T) {
	fake := newFakeOCI(t,
		expectedRequest{http.MethodGet, "/20160918/tenancies/ocid1.tenancy.oc1..test", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, map[string]any{"id": "ocid1.tenancy.oc1..test", "name": "test"})
		}},
		expectedRequest{http.MethodGet, "/20160918/availabilityDomains?compartmentId=root", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, []map[string]any{{"name": "AD-1", "compartmentId": "root"}})
		}},
		expectedRequest{http.MethodGet, "/20160918/images?compartmentId=compartment&lifecycleState=AVAILABLE&operatingSystem=Oracle+Linux&operatingSystemVersion=9&shape=VM.Standard.A1.Flex", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("opc-next-page", "images-2")
			writeJSON(t, w, []map[string]any{{"id": "image-1", "displayName": "OL 9.1", "compartmentId": "compartment", "operatingSystem": "Oracle Linux", "operatingSystemVersion": "9"}})
		}},
		expectedRequest{http.MethodGet, "/20160918/images?compartmentId=compartment&lifecycleState=AVAILABLE&operatingSystem=Oracle+Linux&operatingSystemVersion=9&page=images-2&shape=VM.Standard.A1.Flex", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, []map[string]any{{"id": "image-2", "displayName": "OL 9.2", "compartmentId": "compartment", "operatingSystem": "Oracle Linux", "operatingSystemVersion": "9"}})
		}},
		expectedRequest{http.MethodGet, "/20160918/vcns?compartmentId=compartment&lifecycleState=AVAILABLE", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, []map[string]any{{"id": "vcn", "displayName": "main", "compartmentId": "compartment", "lifecycleState": "AVAILABLE"}})
		}},
		expectedRequest{http.MethodGet, "/20160918/subnets?compartmentId=compartment&lifecycleState=AVAILABLE&vcnId=vcn", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, []map[string]any{{"id": "subnet", "displayName": "public", "compartmentId": "compartment", "vcnId": "vcn", "availabilityDomain": "AD-1", "lifecycleState": "AVAILABLE", "prohibitPublicIpOnVnic": false}})
		}},
		expectedRequest{http.MethodGet, "/20160918/instances?compartmentId=compartment", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, []map[string]any{{"id": "instance", "lifecycleState": "RUNNING", "freeformTags": map[string]string{"ocihood.target": "target"}}})
		}},
	)
	clients := fake.clients(t)
	if err := clients.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	provider := ocidiscovery.New(clients)
	if ads, err := provider.AvailabilityDomains(t.Context(), "root"); err != nil || !reflect.DeepEqual(ads, []string{"AD-1"}) {
		t.Fatalf("ADs=%v err=%v", ads, err)
	}
	query := discoverydomain.Query{CompartmentID: "compartment", Shape: "VM.Standard.A1.Flex", OperatingSystem: "Oracle Linux", OSVersion: "9"}
	first, err := provider.Images(t.Context(), query, "")
	if err != nil || first.Next != "images-2" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	second, err := provider.Images(t.Context(), query, first.Next)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != "image-2" {
		t.Fatalf("second page=%+v err=%v", second, err)
	}
	if _, err = provider.VCNs(t.Context(), query, ""); err != nil {
		t.Fatal(err)
	}
	query.VCNID = "vcn"
	if _, err = provider.Subnets(t.Context(), query, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Instances(t.Context(), "compartment", ""); err != nil {
		t.Fatal(err)
	}
}

func TestSDKContractCapacityAndServiceErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		retry     string
		body      string
		want      capacitydomain.Kind
		wantRetry time.Duration
	}{
		{name: "available", status: 200, body: `{"shapeAvailabilities":[{"availabilityStatus":"AVAILABLE"}]}`, want: capacitydomain.Available},
		{name: "unsupported", status: 404, body: `{"code":"NotFound","message":"missing"}`, want: capacitydomain.ProbeUnavailable},
		{name: "unauthorized", status: 403, body: `{"code":"NotAuthorizedOrNotFound","message":"denied"}`, want: capacitydomain.ProbeUnavailable},
		{name: "throttled", status: 429, retry: "7", body: `{"code":"TooManyRequests","message":"slow"}`, want: capacitydomain.Throttled, wantRetry: 7 * time.Second},
		{name: "server", status: 503, body: `{"code":"ServiceUnavailable","message":"later"}`, want: capacitydomain.Transient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeOCI(t, expectedRequest{http.MethodPost, "/20160918/computeCapacityReports", func(w http.ResponseWriter, _ *http.Request) {
				if tt.retry != "" {
					w.Header().Set("Retry-After", tt.retry)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}})
			got, _ := ocicapacity.New(fake.clients(t)).Probe(t.Context(), capacitydomain.Request{TenancyID: "root", AvailabilityDomain: "AD-1", Shape: "VM.Standard.A1.Flex", OCPUs: 2, MemoryGB: 12})
			if got.Kind != tt.want || got.RetryAfter != tt.wantRetry {
				t.Fatalf("result=%+v", got)
			}
		})
	}
}

func TestSDKContractLaunchLifecycleAndVNIC(t *testing.T) {
	var launch capturedRequest
	fake := newFakeOCI(t,
		expectedRequest{http.MethodPost, "/20160918/instances", func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			launch = capturedRequest{Header: r.Header.Clone(), Body: body}
			writeJSON(t, w, map[string]any{"id": "instance", "lifecycleState": "STARTING"})
		}},
		expectedRequest{http.MethodGet, "/20160918/instances/instance", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, map[string]any{"id": "instance", "lifecycleState": "RUNNING"})
		}},
		expectedRequest{http.MethodGet, "/20160918/vnicAttachments?compartmentId=compartment&instanceId=instance", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, []map[string]any{{"id": "attachment", "vnicId": "vnic"}})
		}},
		expectedRequest{http.MethodGet, "/20160918/vnics/vnic", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, map[string]any{"id": "vnic", "publicIp": "203.0.113.7"})
		}},
	)
	provider := ocilaunch.New(fake.clients(t))
	in := launchdomain.Request{TargetID: "target", Account: "Prod", CompartmentID: "compartment", AvailabilityDomain: "AD-1", Shape: "VM.Standard.A1.Flex", ImageID: "image", SubnetID: "subnet", SSHPublicKey: "ssh-ed25519 key", OCPUs: 2, MemoryGB: 12, BootVolumeGB: 50, PublicIP: true, Attempt: reconcile.Attempt{RetryToken: "retry-token"}}
	if _, err := provider.Launch(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	got, err := provider.Get(t.Context(), "compartment", "instance")
	if err != nil || got.State != "RUNNING" || got.PublicIP != "203.0.113.7" {
		t.Fatalf("instance=%+v err=%v", got, err)
	}
	if launch.Header.Get("opc-retry-token") != "retry-token" {
		t.Fatalf("retry token=%q", launch.Header.Get("opc-retry-token"))
	}
	var body map[string]any
	if err := json.Unmarshal(launch.Body, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"availabilityDomain": "AD-1", "compartmentId": "compartment", "shape": "VM.Standard.A1.Flex"}
	for key, value := range want {
		if body[key] != value {
			t.Fatalf("%s=%v", key, body[key])
		}
	}
	shape := body["shapeConfig"].(map[string]any)
	source := body["sourceDetails"].(map[string]any)
	vnic := body["createVnicDetails"].(map[string]any)
	if shape["ocpus"] != float64(2) || shape["memoryInGBs"] != float64(12) || source["imageId"] != "image" || source["bootVolumeSizeInGBs"] != float64(50) || vnic["subnetId"] != "subnet" || vnic["assignPublicIp"] != true {
		t.Fatalf("launch body=%s", launch.Body)
	}
	if body["metadata"].(map[string]any)["ssh_authorized_keys"] != "ssh-ed25519 key" {
		t.Fatalf("metadata=%v", body["metadata"])
	}
	tags := body["freeformTags"].(map[string]any)
	for key, value := range reconcile.OwnershipTags("target", "Prod") {
		if tags[key] != value {
			t.Fatalf("tags=%v", tags)
		}
	}
}

func TestSDKContractErrorClassificationMalformedAndCancellation(t *testing.T) {
	statuses := []struct {
		status int
		code   string
		want   launchdomain.Kind
	}{
		{401, "NotAuthenticated", launchdomain.Fatal}, {403, "NotAuthorized", launchdomain.Fatal}, {404, "NotFound", launchdomain.Fatal}, {409, "Conflict", launchdomain.Fatal}, {429, "TooManyRequests", launchdomain.Transient}, {500, "InternalError", launchdomain.Transient},
	}
	for _, tt := range statuses {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			fake := newFakeOCI(t, expectedRequest{http.MethodPost, "/20160918/instances", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				writeJSON(t, w, map[string]string{"code": tt.code, "message": "test"})
			}})
			result, err := ocilaunch.New(fake.clients(t)).Launch(t.Context(), launchdomain.Request{Attempt: reconcile.Attempt{RetryToken: "token"}})
			if err == nil || result.Kind != tt.want {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
	t.Run("malformed JSON", func(t *testing.T) {
		fake := newFakeOCI(t, expectedRequest{http.MethodGet, "/20160918/images?compartmentId=compartment&lifecycleState=AVAILABLE", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "{")
		}})
		_, err := ocidiscovery.New(fake.clients(t)).Images(t.Context(), discoverydomain.Query{CompartmentID: "compartment"}, "")
		if err == nil {
			t.Fatal("expected malformed response error")
		}
	})
	for _, name := range []string{"cancel", "timeout"} {
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			fake := newFakeOCI(t, expectedRequest{http.MethodGet, "/20160918/images?compartmentId=compartment&lifecycleState=AVAILABLE", func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }})
			provider := ocidiscovery.New(fake.clients(t))
			ctx, cancel := context.WithCancel(t.Context())
			if name == "timeout" {
				ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := provider.Images(ctx, discoverydomain.Query{CompartmentID: "compartment"}, "")
				done <- err
			}()
			<-started
			if name == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected context error")
				}
			case <-time.After(time.Second):
				t.Fatal("SDK request did not stop")
			}
		})
	}
}

func TestSDKContractRejectsExternalEndpoint(t *testing.T) {
	fake := newFakeOCI(t)
	clients := fake.clients(t)
	clients.Identity.Host = "https://identity.example.invalid"
	if err := clients.Validate(t.Context()); err == nil {
		t.Fatal("expected external endpoint rejection")
	}
	fake.dispatcher.mu.Lock()
	defer fake.dispatcher.mu.Unlock()
	if fake.dispatcher.blocked != 1 {
		t.Fatalf("blocked requests=%d, want 1", fake.dispatcher.blocked)
	}
}
