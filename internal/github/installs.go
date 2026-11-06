package github

import (
	"context"
	"errors"
	"net/http"

	gh "github.com/google/go-github/v93/github"
)

// RepoRef is a repository an installation can access.
type RepoRef struct {
	ID       int64
	FullName string
}

// ListInstallationRepos returns every repository the installation covers.
func (c *Client) ListInstallationRepos(ctx context.Context, installID int64) ([]RepoRef, error) {
	cl, err := c.installation(installID)
	if err != nil {
		return nil, err
	}
	var out []RepoRef
	opt := &gh.ListOptions{PerPage: 100}
	for {
		res, resp, err := cl.Apps.ListRepos(ctx, opt)
		if err != nil {
			return nil, err
		}
		for _, r := range res.Repositories {
			out = append(out, RepoRef{ID: r.GetID(), FullName: r.GetFullName()})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// ListUserInstallations returns the ids of the installations the user token can access.
func (c *Client) ListUserInstallations(ctx context.Context, token string) ([]int64, error) {
	base := c.baseURL + "/"
	cl, err := gh.NewClient(
		gh.WithHTTPClient(&http.Client{Transport: bearer{token}, Timeout: c.timeout}),
		gh.WithURLs(&base, &base),
	)
	if err != nil {
		return nil, err
	}
	var out []int64
	opt := &gh.ListOptions{PerPage: 100}
	for {
		list, resp, err := cl.Apps.ListUserInstallations(ctx, opt)
		if err != nil {
			return nil, err
		}
		for _, in := range list {
			out = append(out, in.GetID())
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opt.Page = resp.NextPage
	}
}

// UserByToken returns the id and login of the GitHub account a user token belongs to.
func (c *Client) UserByToken(ctx context.Context, token string) (int64, string, error) {
	base := c.baseURL + "/"
	cl, err := gh.NewClient(
		gh.WithHTTPClient(&http.Client{Transport: bearer{token}, Timeout: c.timeout}),
		gh.WithURLs(&base, &base),
	)
	if err != nil {
		return 0, "", err
	}
	u, _, err := cl.Users.Get(ctx, "")
	if err != nil {
		return 0, "", err
	}
	if u.GetID() == 0 {
		return 0, "", errors.New("github: user has no id")
	}
	return u.GetID(), u.GetLogin(), nil
}
