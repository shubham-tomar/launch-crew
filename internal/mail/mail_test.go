package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResendPostsTheMessage(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	s := New("key", "Launch Crew <hi@lc.test>").(*Resend)
	s.URL = srv.URL
	if err := s.Send(context.Background(), Message{To: "a@b.test", Subject: "S", Text: "T"}); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer key" || got["subject"] != "S" || got["from"] != "Launch Crew <hi@lc.test>" {
		t.Fatalf("auth %q body %v", auth, got)
	}
	if to, _ := got["to"].([]any); len(to) != 1 || to[0] != "a@b.test" {
		t.Fatalf("to %v", got["to"])
	}
}

func TestResendReportsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad from", http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	s := &Resend{APIKey: "k", From: "f", URL: srv.URL}
	if err := s.Send(context.Background(), Message{To: "a@b.test"}); err == nil {
		t.Fatal("want an error")
	}
}

func TestUnconfiguredIsUnavailable(t *testing.T) {
	if Available(New("", "x")) || Available(nil) || !Available(New("k", "f")) {
		t.Fatal("availability wrong")
	}
	if err := New("", "").Send(context.Background(), Message{}); err != ErrNotConfigured {
		t.Fatalf("got %v", err)
	}
}
