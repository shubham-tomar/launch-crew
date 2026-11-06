package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	gh "github.com/google/go-github/v93/github"
)

// DescribeInstallation asks GitHub, as the App, what installation installID is
// and returns it as one line for a log: the App and account it belongs to, the
// permissions it holds, and whether repo (owner/name) is among its repositories.
// It is for explaining a refused write, so it never fails: a lookup that does
// not work is described in the line instead.
func (c *Client) DescribeInstallation(ctx context.Context, installID int64, repo string) string {
	base := c.baseURL + "/"
	cl, err := gh.NewClient(
		gh.WithHTTPClient(&http.Client{Transport: c.apps, Timeout: c.timeout}),
		gh.WithURLs(&base, &base),
	)
	if err != nil {
		return fmt.Sprintf("installation %d: client: %v", installID, err)
	}
	in, _, err := cl.Apps.GetInstallation(ctx, installID)
	if err != nil {
		return fmt.Sprintf("installation %d (server app_id=%d): lookup failed: %v", installID, c.appID, err)
	}
	p := in.GetPermissions()
	var b strings.Builder
	fmt.Fprintf(&b, "installation %d: server app_id=%d, installation app_id=%d slug=%s account=%s selection=%s issues=%s pull_requests=%s contents=%s suspended=%t",
		installID, c.appID, in.GetAppID(), in.GetAppSlug(), in.GetAccount().GetLogin(), in.GetRepositorySelection(),
		orNone(p.GetIssues()), orNone(p.GetPullRequests()), orNone(p.GetContents()), in.SuspendedAt != nil)
	if repo != "" {
		repos, err := c.ListInstallationRepos(ctx, installID)
		if err != nil {
			fmt.Fprintf(&b, " repo %s: list failed: %v", repo, err)
		} else {
			found := false
			for _, r := range repos {
				if strings.EqualFold(r.FullName, repo) {
					found = true
				}
			}
			fmt.Fprintf(&b, " repo_included(%s)=%t of %d", repo, found, len(repos))
		}
	}
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
