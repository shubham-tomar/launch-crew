// Package mail sends Launch Crew's few emails: the captain's address
// verification and "your launch draft is ready".
package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Message is a plain-text email.
type Message struct {
	To, Subject, Text string
}

// Sender sends one message.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// ErrNotConfigured means the server has no mail provider; callers hide email.
var ErrNotConfigured = errors.New("mail: not configured")

// Available reports whether s can send.
func Available(s Sender) bool {
	if s == nil {
		return false
	}
	_, off := s.(none)
	return !off
}

type none struct{}

func (none) Send(context.Context, Message) error { return ErrNotConfigured }

// DefaultResendURL is Resend's send endpoint.
const DefaultResendURL = "https://api.resend.com/emails"

// Resend sends through Resend's HTTP API. URL defaults to DefaultResendURL;
// tests point it at a fake.
type Resend struct {
	APIKey, From, URL string
	Client            *http.Client
}

// New returns a Resend sender, or one that refuses when key or from is empty.
func New(apiKey, from string) Sender {
	if apiKey == "" || from == "" {
		return none{}
	}
	return &Resend{APIKey: apiKey, From: from}
}

// Send posts the message to Resend.
func (r *Resend) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(map[string]any{"from": r.From, "to": []string{m.To}, "subject": m.Subject, "text": m.Text})
	if err != nil {
		return err
	}
	url := r.URL
	if url == "" {
		url = DefaultResendURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.APIKey)
	req.Header.Set("Content-Type", "application/json")
	cl := r.Client
	if cl == nil {
		cl = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("mail: send: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("mail: send: status %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}
