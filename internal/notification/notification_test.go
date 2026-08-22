package notification

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, context.DeadlineExceeded
}

func TestTelegram(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	err := (Telegram{Endpoint: server.URL, Token: "secret-token", ChatID: "42", Timeout: time.Second}).Notify(t.Context(), Event{Kind: InstanceRunning, Account: "prod", Region: "eu-test-1", TargetID: "target", InstanceID: "instance", PublicIP: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "instance%3Dinstance") || strings.Contains(body, "secret-token") {
		t.Fatalf("unexpected request body %q", body)
	}
}

func TestTelegramFailuresAndCancellation(t *testing.T) {
	tests := []struct {
		name     string
		handler  http.HandlerFunc
		canceled bool
	}{
		{"4xx", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusBadRequest) }, false},
		{"5xx", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusServiceUnavailable) }, false},
		{"timeout", func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(50 * time.Millisecond)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}, false},
		{"canceled", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := httptest.NewServer(tt.handler)
			defer s.Close()
			ctx := t.Context()
			var cancel context.CancelFunc
			if tt.canceled {
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := (Telegram{Endpoint: s.URL, Token: "token", ChatID: "chat", Timeout: 10 * time.Millisecond}).Notify(ctx, Event{})
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "token") {
				t.Fatalf("secret leaked: %v", err)
			}
		})
	}
}

func TestTelegramNetworkErrorIsRedacted(t *testing.T) {
	err := (Telegram{Endpoint: "https://example.invalid", Token: "secret-token", ChatID: "chat", Client: &http.Client{Transport: failingTransport{}}, Timeout: time.Second}).Notify(t.Context(), Event{})
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error=%v", err)
	}
}

func TestFormatOptionalPublicIPAndSanitizes(t *testing.T) {
	without := Format(Event{Kind: TerminalFailure, Account: "a", Region: "r", TargetID: "t", Detail: "bad\nvalue"})
	if strings.Contains(without, "public_ip=") || strings.Contains(without, "\n") {
		t.Fatalf("unexpected format %q", without)
	}
	with := Format(Event{Kind: InstanceRunning, Account: "a", Region: "r", TargetID: "t", PublicIP: "192.0.2.1"})
	if !strings.Contains(with, "public_ip=192.0.2.1") {
		t.Fatalf("missing IP: %q", with)
	}
}
