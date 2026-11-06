package launch

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	gh "github.com/google/go-github/v93/github"
)

func TestRefusalDetailQuotesWhatGitHubSaid(t *testing.T) {
	h := http.Header{}
	h.Set("X-Accepted-GitHub-Permissions", "issues=write; pull_requests=write")
	h.Set("X-GitHub-Request-Id", "ABCD:1234")
	err := fmt.Errorf("comment on #6: %w", &gh.ErrorResponse{
		Response:         &http.Response{StatusCode: http.StatusForbidden, Header: h},
		Message:          "Resource not accessible by integration",
		DocumentationURL: "https://docs.github.com/rest/issues/comments",
	})
	if !denied(err) {
		t.Fatal("a 403 must count as refused")
	}
	got := refusalDetail(err)
	for _, want := range []string{"Resource not accessible by integration", "issues=write; pull_requests=write", "ABCD:1234", "docs.github.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if refusalDetail(fmt.Errorf("plain")) != "" {
		t.Error("a plain error has no GitHub detail")
	}
}
