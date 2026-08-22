package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Kind string

const (
	RunStarted      Kind = "run_started"
	Waiting         Kind = "waiting"
	Degraded        Kind = "degraded"
	CapacityFound   Kind = "capacity_found"
	InstanceRunning Kind = "instance_running"
	TerminalFailure Kind = "terminal_failure"
	Paused          Kind = "paused"
	Resumed         Kind = "resumed"
)

type Event struct {
	Kind                                                    Kind
	Account, Region, TargetID, InstanceID, PublicIP, Detail string
}

type Notifier interface {
	Notify(context.Context, Event) error
}

type Telegram struct {
	Endpoint, Token, ChatID string
	Client                  *http.Client
	Timeout                 time.Duration
}

func (t Telegram) Notify(ctx context.Context, event Event) error {
	if t.Token == "" || t.ChatID == "" {
		return errors.New("telegram notifier is not configured")
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	endpoint := t.Endpoint
	if endpoint == "" {
		endpoint = "https://api.telegram.org"
	}
	form := url.Values{"chat_id": {t.ChatID}, "text": {Format(event)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/bot"+t.Token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("send telegram notification: %w", ctx.Err())
		}
		return errors.New("send telegram notification: network request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode telegram response: %w", err)
	}
	if !result.OK {
		return errors.New("telegram rejected notification")
	}
	return nil
}

func Format(e Event) string {
	parts := []string{string(e.Kind), "account=" + e.Account, "region=" + e.Region, "target=" + e.TargetID}
	if e.InstanceID != "" {
		parts = append(parts, "instance="+e.InstanceID)
	}
	if e.PublicIP != "" {
		parts = append(parts, "public_ip="+e.PublicIP)
	}
	if e.Detail != "" {
		parts = append(parts, "detail="+sanitize(e.Detail))
	}
	return strings.Join(parts, " ")
}

func sanitize(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
