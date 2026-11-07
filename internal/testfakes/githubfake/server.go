// Package githubfake is an in-memory stand-in for the slice of the GitHub REST
// API Launch Crew uses: installation tokens, compare, commits, pulls, tags,
// releases and issue comments for one repository. It records every request.
package githubfake

import (
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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// User is a GitHub account.
type User struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Type      string `json:"type"`
	AvatarURL string `json:"avatar_url"`
}

// Commit is one commit, as GitHub returns it in lists.
type Commit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
	// Author is the GitHub account linked to the commit, nil when none.
	Author *User `json:"author,omitempty"`
}

// Label is a PR label.
type Label struct {
	Name string `json:"name"`
}

// PR is a pull request. MergeCommitSHA links it to /commits/{sha}/pulls.
type PR struct {
	Number         int     `json:"number"`
	Title          string  `json:"title"`
	Body           string  `json:"body"`
	State          string  `json:"state"`
	Merged         bool    `json:"merged"`
	MergedAt       *string `json:"merged_at"`
	MergeCommitSHA string  `json:"merge_commit_sha"`
	Additions      int     `json:"additions"`
	Deletions      int     `json:"deletions"`
	User           User    `json:"user"`
	Labels         []Label `json:"labels"`
}

// InstallRepo is a repository an installation covers.
type InstallRepo struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

// Tag points a name at a commit.
type Tag struct {
	Name string
	SHA  string
}

// Release is a GitHub release.
type Release struct {
	ID         int64  `json:"id"`
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	HTMLURL    string `json:"html_url"`
	// PublishedAt is RFC 3339, empty for none.
	PublishedAt string `json:"published_at,omitempty"`
}

// Comment is an issue comment.
type Comment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User User   `json:"user"`
}

// Request is one request the fake received.
type Request struct {
	Method, Path, Query string
	Body                []byte
}

// Server is the fake. Set the exported fields before use; guard later
// mutation with Lock/Unlock or the helper methods.
type Server struct {
	*httptest.Server
	Owner, Repo string

	Commits  []Commit  // oldest first
	Tags     []Tag     // newest first
	Releases []Release // newest first
	PRs      []PR
	Comments map[int][]Comment // issue number -> comments
	// InstallRepos is what GET /installation/repositories lists, as full names with ids.
	InstallRepos []InstallRepo
	// UserInstallations is what GET /user/installations lists for the OAuth user.
	UserInstallations []int64
	// FailInstallRepos makes GET /installation/repositories answer 502.
	FailInstallRepos bool
	// OAuthCode is the code the token endpoint accepts; OAuthUser is who /user reports for it.
	OAuthCode string
	OAuthUser User
	// FailReleaseEdit makes PATCH /releases/{id} answer 500.
	FailReleaseEdit bool
	// DenyComments makes every issue comment call answer 403, as when the
	// App lacks write access to the repository.
	DenyComments bool
	// InstallCallback and InstallationID drive the web install page
	// (/apps/{slug}/installations/new): it redirects to InstallCallback
	// with installation_id and the state, as GitHub does after an install.
	InstallCallback string
	InstallationID  int64

	mu       sync.Mutex
	requests []Request
	nextID   int64
}

// New starts a fake for owner/repo.
func New(owner, repo string) *Server {
	s := &Server{Owner: owner, Repo: repo, Comments: map[int][]Comment{}, nextID: 9000}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Lock guards direct field access while the server is running.
func (s *Server) Lock() { s.mu.Lock() }

// Unlock releases Lock.
func (s *Server) Unlock() { s.mu.Unlock() }

// Requests returns the recorded requests matching method (any when "")
// whose path contains sub.
func (s *Server) Requests(method, sub string) []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Request
	for _, r := range s.requests {
		if (method == "" || r.Method == method) && strings.Contains(r.Path, sub) {
			out = append(out, r)
		}
	}
	return out
}

// IssueComments returns the comments on an issue or PR.
func (s *Server) IssueComments(number int) []Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Comment(nil), s.Comments[number]...)
}

// Release returns the release with id.
func (s *Server) Release(id int64) (Release, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.Releases {
		if r.ID == id {
			return r, true
		}
	}
	return Release{}, false
}

// NewCommit builds a Commit.
func NewCommit(sha, message string) Commit {
	var c Commit
	c.SHA, c.Commit.Message = sha, message
	return c
}

var (
	keyOnce sync.Once
	keyPEM  []byte
)

// PrivateKeyPEM returns a throwaway RSA key for constructing App clients.
func PrivateKeyPEM() []byte {
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	})
	return keyPEM
}

var (
	reToken       = regexp.MustCompile(`^/app/installations/(\d+)/access_tokens$`)
	reCompare     = regexp.MustCompile(`^/compare/(.+)\.\.\.(.+)$`)
	reCommitPulls = regexp.MustCompile(`^/commits/([^/]+)/pulls$`)
	rePull        = regexp.MustCompile(`^/pulls/(\d+)$`)
	reIssueCmts   = regexp.MustCompile(`^/issues/(\d+)/comments$`)
	reCmt         = regexp.MustCompile(`^/issues/comments/(\d+)$`)
	reRelease     = regexp.MustCompile(`^/releases/(\d+)$`)
	reInstallNew  = regexp.MustCompile(`^/apps/[^/]+/installations/new$`)
)

// webPage serves the browser pages GitHub would show; both approve at once.
func (s *Server) webPage(w http.ResponseWriter, r *http.Request) bool {
	var dest string
	q := url.Values{"state": {r.URL.Query().Get("state")}}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/login/oauth/authorize":
		dest = r.URL.Query().Get("redirect_uri")
		q.Set("code", s.OAuthCode)
	case r.Method == http.MethodGet && reInstallNew.MatchString(r.URL.Path):
		dest = s.InstallCallback
		q.Set("installation_id", strconv.FormatInt(s.InstallationID, 10))
	default:
		return false
	}
	u, err := url.Parse(dest)
	if err != nil || u.Scheme == "" {
		http.Error(w, "no redirect target", http.StatusBadRequest)
		return true
	}
	for k, v := range u.Query() {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
	return true
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})
	if s.webPage(w, r) {
		return
	}

	if m := reToken.FindStringSubmatch(r.URL.Path); m != nil && r.Method == http.MethodPost {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, `{"message":"no jwt"}`, http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"token": "ghs_fake_" + m[1], "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
		return
	}
	switch {
	case r.URL.Path == "/installation/repositories" && r.Method == http.MethodGet:
		if !strings.HasPrefix(r.Header.Get("Authorization"), "token ghs_fake_") &&
			!strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ghs_fake_") {
			http.Error(w, `{"message":"bad credentials"}`, http.StatusUnauthorized)
			return
		}
		if s.FailInstallRepos {
			http.Error(w, `{"message":"bad gateway"}`, http.StatusBadGateway)
			return
		}
		start, end := page(w, r, len(s.InstallRepos))
		repos := s.InstallRepos[start:end]
		if repos == nil {
			repos = []InstallRepo{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"total_count": len(s.InstallRepos), "repositories": repos})
		return
	case r.URL.Path == "/login/oauth/access_token" && r.Method == http.MethodPost:
		form, _ := url.ParseQuery(string(body))
		if s.OAuthCode == "" || form.Get("code") != s.OAuthCode {
			writeJSON(w, http.StatusOK, map[string]string{"error": "bad_verification_code"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"access_token": "gho_fake", "token_type": "bearer"})
		return
	case r.URL.Path == "/user/installations" && r.Method == http.MethodGet:
		if r.Header.Get("Authorization") != "Bearer gho_fake" {
			http.Error(w, `{"message":"bad credentials"}`, http.StatusUnauthorized)
			return
		}
		start, end := page(w, r, len(s.UserInstallations))
		ins := []map[string]int64{}
		for _, id := range s.UserInstallations[start:end] {
			ins = append(ins, map[string]int64{"id": id})
		}
		writeJSON(w, http.StatusOK, map[string]any{"total_count": len(s.UserInstallations), "installations": ins})
		return
	case r.URL.Path == "/user" && r.Method == http.MethodGet:
		if r.Header.Get("Authorization") != "Bearer gho_fake" {
			http.Error(w, `{"message":"bad credentials"}`, http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, s.OAuthUser)
		return
	}
	prefix := "/repos/" + s.Owner + "/" + s.Repo
	rest, ok := strings.CutPrefix(r.URL.Path, prefix)
	if !ok {
		notFound(w)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "token ghs_fake_") &&
		!strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ghs_fake_") {
		http.Error(w, `{"message":"bad credentials"}`, http.StatusUnauthorized)
		return
	}

	switch {
	case r.Method == http.MethodGet && reCompare.MatchString(rest):
		m := reCompare.FindStringSubmatch(rest)
		s.compare(w, r, m[1], m[2])
	case r.Method == http.MethodGet && rest == "/commits":
		s.listCommits(w, r)
	case r.Method == http.MethodGet && reCommitPulls.MatchString(rest):
		sha := reCommitPulls.FindStringSubmatch(rest)[1]
		out := []PR{}
		for _, p := range s.PRs {
			if p.MergeCommitSHA == sha {
				out = append(out, p)
			}
		}
		writeJSON(w, http.StatusOK, out)
	case r.Method == http.MethodGet && rePull.MatchString(rest):
		n, _ := strconv.Atoi(rePull.FindStringSubmatch(rest)[1])
		for _, p := range s.PRs {
			if p.Number == n {
				writeJSON(w, http.StatusOK, p)
				return
			}
		}
		notFound(w)
	case r.Method == http.MethodGet && rest == "/tags":
		out := make([]map[string]any, len(s.Tags))
		for i, t := range s.Tags {
			out[i] = map[string]any{"name": t.Name, "commit": map[string]string{"sha": t.SHA}}
		}
		start, end := page(w, r, len(out))
		writeJSON(w, http.StatusOK, out[start:end])
	case r.Method == http.MethodGet && rest == "/releases":
		start, end := page(w, r, len(s.Releases))
		writeJSON(w, http.StatusOK, s.Releases[start:end])
	case reRelease.MatchString(rest):
		s.release(w, r, rest, body)
	case s.DenyComments && (reIssueCmts.MatchString(rest) || reCmt.MatchString(rest)):
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by integration"})
	case reIssueCmts.MatchString(rest):
		n, _ := strconv.Atoi(reIssueCmts.FindStringSubmatch(rest)[1])
		s.issueComments(w, r, n, body)
	case r.Method == http.MethodPatch && reCmt.MatchString(rest):
		id, _ := strconv.ParseInt(reCmt.FindStringSubmatch(rest)[1], 10, 64)
		var req struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(body, &req)
		for n, cs := range s.Comments {
			for i := range cs {
				if cs[i].ID == id {
					s.Comments[n][i].Body = req.Body
					writeJSON(w, http.StatusOK, s.Comments[n][i])
					return
				}
			}
		}
		notFound(w)
	default:
		notFound(w)
	}
}

// resolve maps a SHA or tag name to an index in Commits, or -1.
func (s *Server) resolve(ref string) int {
	for _, t := range s.Tags {
		if t.Name == ref {
			ref = t.SHA
			break
		}
	}
	for i, c := range s.Commits {
		if c.SHA == ref {
			return i
		}
	}
	return -1
}

func (s *Server) compare(w http.ResponseWriter, r *http.Request, base, head string) {
	b, h := s.resolve(base), s.resolve(head)
	if b < 0 || h < 0 || b > h {
		notFound(w)
		return
	}
	commits := s.Commits[b+1 : h+1]
	start, end := page(w, r, len(commits))
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ahead", "ahead_by": len(commits), "total_commits": len(commits),
		"base_commit": s.Commits[b], "commits": commits[start:end],
	})
}

// listCommits answers GET /commits?sha=<ref>, newest first.
func (s *Server) listCommits(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("sha")
	h := len(s.Commits) - 1
	if ref != "" {
		h = s.resolve(ref)
	}
	if h < 0 {
		notFound(w)
		return
	}
	out := make([]Commit, 0, h+1)
	for i := h; i >= 0; i-- {
		out = append(out, s.Commits[i])
	}
	start, end := page(w, r, len(out))
	writeJSON(w, http.StatusOK, out[start:end])
}

func (s *Server) release(w http.ResponseWriter, r *http.Request, rest string, body []byte) {
	id, _ := strconv.ParseInt(reRelease.FindStringSubmatch(rest)[1], 10, 64)
	for i := range s.Releases {
		if s.Releases[i].ID != id {
			continue
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, s.Releases[i])
		case http.MethodPatch:
			if s.FailReleaseEdit {
				http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
				return
			}
			var req struct {
				Body *string `json:"body"`
			}
			_ = json.Unmarshal(body, &req)
			if req.Body != nil {
				s.Releases[i].Body = *req.Body
			}
			writeJSON(w, http.StatusOK, s.Releases[i])
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	notFound(w)
}

func (s *Server) issueComments(w http.ResponseWriter, r *http.Request, n int, body []byte) {
	switch r.Method {
	case http.MethodGet:
		cs := s.Comments[n]
		start, end := page(w, r, len(cs))
		out := cs[start:end]
		if out == nil {
			out = []Comment{}
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var req struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(body, &req)
		s.nextID++
		c := Comment{ID: s.nextID, Body: req.Body, User: User{ID: 1, Login: "launchcrew[bot]", Type: "Bot"}}
		s.Comments[n] = append(s.Comments[n], c)
		writeJSON(w, http.StatusCreated, c)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// page applies page/per_page (default 30) and sets a Link header for the next and last pages.
func page(w http.ResponseWriter, r *http.Request, total int) (start, end int) {
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if per <= 0 {
		per = 30
	}
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p <= 0 {
		p = 1
	}
	last := (total + per - 1) / per
	if last == 0 {
		last = 1
	}
	start = min((p-1)*per, total)
	end = min(start+per, total)
	link := func(n int) string {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(n))
		q.Set("per_page", strconv.Itoa(per))
		return fmt.Sprintf("<http://%s%s?%s>", r.Host, r.URL.Path, q.Encode())
	}
	var links []string
	if p < last {
		links = append(links, link(p+1)+`; rel="next"`)
	}
	if last > 1 {
		links = append(links, link(last)+`; rel="last"`)
	}
	if len(links) > 0 {
		w.Header().Set("Link", strings.Join(links, ", "))
	}
	return start, end
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}
