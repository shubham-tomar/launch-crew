package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

func stubGitHub(t *testing.T, installation map[string]any, repos []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/42", func(w http.ResponseWriter, r *http.Request) {
		if installation == nil {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(installation)
	})
	mux.HandleFunc("/app/installations/42/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test","expires_at":"2099-01-01T00:00:00Z"}`))
	})
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		var rs []map[string]any
		for i, n := range repos {
			rs = append(rs, map[string]any{"id": i + 1, "full_name": n})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(rs), "repositories": rs})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDescribeInstallation(t *testing.T) {
	srv := stubGitHub(t, map[string]any{
		"id": 42, "app_id": 777, "app_slug": "launch-crew-bot", "repository_selection": "selected",
		"account":     map[string]any{"login": "shubham-tomar"},
		"permissions": map[string]any{"issues": "write", "pull_requests": "read", "metadata": "read"},
	}, []string{"shubham-tomar/launch-crew"})
	c, err := New(777, testKey(t), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	got := c.DescribeInstallation(context.Background(), 42, "shubham-tomar/launch-crew")
	for _, want := range []string{
		"server app_id=777", "installation app_id=777", "slug=launch-crew-bot", "account=shubham-tomar",
		"selection=selected", "issues=write", "pull_requests=read", "contents=none", "suspended=false",
		"repo_included(shubham-tomar/launch-crew)=true of 1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
}

func TestDescribeInstallationReportsAWrongRepoAndAnApp(t *testing.T) {
	srv := stubGitHub(t, map[string]any{
		"id": 42, "app_id": 999, "app_slug": "other-app", "account": map[string]any{"login": "someone"},
		"permissions": map[string]any{"issues": "read"},
	}, []string{"someone/else"})
	c, err := New(777, testKey(t), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	got := c.DescribeInstallation(context.Background(), 42, "shubham-tomar/launch-crew")
	for _, want := range []string{"server app_id=777", "installation app_id=999", "issues=read", "repo_included(shubham-tomar/launch-crew)=false of 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
}

func TestDescribeInstallationNeverFails(t *testing.T) {
	srv := stubGitHub(t, nil, nil)
	c, err := New(777, testKey(t), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	got := c.DescribeInstallation(context.Background(), 42, "a/b")
	if !strings.Contains(got, "lookup failed") || !strings.Contains(got, "server app_id=777") {
		t.Fatalf("got %q", got)
	}
}
