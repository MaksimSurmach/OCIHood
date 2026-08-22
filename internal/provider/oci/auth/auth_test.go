package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MaksimSurmach/OCIHood/internal/config"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

func writeProfile(t *testing.T, profile, region string) config.Effective {
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
	body := fmt.Sprintf("[%s]\ntenancy=ocid1.tenancy.oc1..test\nuser=ocid1.user.oc1..test\nfingerprint=aa:bb\nkey_file=%s\nregion=%s\n", profile, keyPath, region)
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.Effective{OCIConfigPath: configPath, OCIProfile: profile}
}

func TestNewProfileRegionAndIsolation(t *testing.T) {
	t.Parallel()
	firstConfig := writeProfile(t, "ONE", "eu-frankfurt-1")
	secondConfig := writeProfile(t, "TWO", "us-ashburn-1")
	firstConfig.Region = "uk-london-1"

	first, err := New(firstConfig)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(secondConfig)
	if err != nil {
		t.Fatal(err)
	}
	if first.Region != "uk-london-1" || second.Region != "us-ashburn-1" {
		t.Fatalf("regions leaked or ignored: first=%q second=%q", first.Region, second.Region)
	}
	if first.Identity.Host == second.Identity.Host || first.Compute.Host == second.Compute.Host {
		t.Fatalf("account clients share region endpoints: first=%q second=%q", first.Identity.Host, second.Identity.Host)
	}
}

func TestNewRejectsLocalCredentialFailures(t *testing.T) {
	t.Parallel()
	valid := writeProfile(t, "VALID", "eu-frankfurt-1")
	tests := []struct {
		name   string
		mutate func(*config.Effective)
	}{
		{name: "missing config", mutate: func(c *config.Effective) { c.OCIConfigPath = filepath.Join(t.TempDir(), "missing") }},
		{name: "missing profile", mutate: func(c *config.Effective) { c.OCIProfile = "MISSING" }},
		{name: "missing key", mutate: func(c *config.Effective) {
			data, err := os.ReadFile(c.OCIConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			data = []byte(strings.ReplaceAll(string(data), "key_file=", "key_file=/missing/"))
			if err := os.WriteFile(c.OCIConfigPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "malformed config", mutate: func(c *config.Effective) {
			if err := os.WriteFile(c.OCIConfigPath, []byte("[VALID]\ntenancy"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "invalid region", mutate: func(c *config.Effective) { c.Region = "not a region" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			if tt.name != "missing config" && tt.name != "missing profile" && tt.name != "invalid region" {
				cfg = writeProfile(t, "VALID", "eu-frankfurt-1")
			}
			tt.mutate(&cfg)
			_, err := New(cfg)
			var authErr *Error
			if !errors.As(err, &authErr) || authErr.Kind != KindLocal {
				t.Fatalf("New() error = %v, want local credential Error", err)
			}
		})
	}
}

type fakeTenancyReader struct{ err error }

func (f fakeTenancyReader) GetTenancy(ctx context.Context, _ identity.GetTenancyRequest) (identity.GetTenancyResponse, error) {
	if f.err != nil {
		return identity.GetTenancyResponse{}, f.err
	}
	return identity.GetTenancyResponse{}, ctx.Err()
}

type recordingTenancyReader struct {
	calls   int
	request identity.GetTenancyRequest
}

func (r *recordingTenancyReader) GetTenancy(_ context.Context, request identity.GetTenancyRequest) (identity.GetTenancyResponse, error) {
	r.calls++
	r.request = request
	return identity.GetTenancyResponse{}, nil
}

type serviceError struct {
	status  int
	message string
}

func (e serviceError) Error() string           { return e.message }
func (e serviceError) GetHTTPStatusCode() int  { return e.status }
func (e serviceError) GetMessage() string      { return e.message }
func (e serviceError) GetCode() string         { return "test-code" }
func (e serviceError) GetOpcRequestID() string { return "secret-request-id" }

func TestValidateClassifiesAndSanitizesFailures(t *testing.T) {
	t.Parallel()
	secret := "PRIVATE-KEY-CONTENTS"
	tests := []struct {
		name string
		err  error
		kind Kind
	}{
		{name: "authentication", err: serviceError{status: 401, message: secret}, kind: KindAuthentication},
		{name: "authorization", err: serviceError{status: 403, message: secret}, kind: KindAuthorization},
		{name: "network", err: errors.New(secret), kind: KindNetwork},
		{name: "timeout", err: context.DeadlineExceeded, kind: KindCanceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients := &Clients{TenancyOCID: "ocid1.tenancy.oc1..test", identityReader: fakeTenancyReader{err: tt.err}}
			err := clients.Validate(t.Context())
			var authErr *Error
			if !errors.As(err, &authErr) || authErr.Kind != tt.kind {
				t.Fatalf("Validate() error = %v, want kind %q", err, tt.kind)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "secret-request-id") {
				t.Fatalf("Validate() exposed secret: %v", err)
			}
		})
	}
}

func TestValidateUsesCallerCancellationAndReadOnlyCall(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	clients := &Clients{TenancyOCID: "ocid1.tenancy.oc1..test", identityReader: fakeTenancyReader{}}
	err := clients.Validate(ctx)
	var authErr *Error
	if !errors.As(err, &authErr) || authErr.Kind != KindCanceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("Validate() error = %v", err)
	}

	reader := &recordingTenancyReader{}
	clients.identityReader = reader
	if err := clients.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || reader.request.TenancyId == nil || *reader.request.TenancyId != clients.TenancyOCID {
		t.Fatalf("GetTenancy calls = %d, request = %#v", reader.calls, reader.request)
	}
}
