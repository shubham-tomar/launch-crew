// Package preflight runs the seven launch checks and the per-draft subset.
package preflight

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/drafting"
)

const (
	maxLead        = 30 * 24 * time.Hour
	rateMargin     = 10
	defaultOverlap = 0.35
	fetchTimeout   = 5 * time.Second
	// limitsHorizon is how far ahead the daily limit still counts: Buffer's
	// "today" follows the channel's timezone, so a launch up to 36 h out
	// can still fall on today's quota somewhere.
	limitsHorizon   = 36 * time.Hour
	checkNameImages = "Card images reachable"
)

// Check is one preflight result. Fix is empty when OK.
type Check struct {
	ID        int
	Name, Fix string
	OK        bool
}

// Draft is a post a preflight inspects.
type Draft struct {
	ChannelID, Service, Network string
	Name                        string // channel display name
	Text                        string
	Thread                      []string
	FirstComment                string
	HasAsset                    bool
}

// Asset is a card or thumbnail URL; Label is the crew member's name or "release card".
type Asset struct{ URL, Label string }

// Launch is the input to Run.
type Launch struct {
	ID            uuid.UUID
	ConnectionID  uuid.UUID
	LaunchAt      time.Time
	Assets        []Asset
	CaptainDrafts []Draft
	ExpectedCalls int
}

// CrewDraft is the input to ForCrewDraft.
type CrewDraft struct {
	Draft
	ConnectionID uuid.UUID
	// AssetURLs are every image the post will carry, card first.
	AssetURLs []string
	LaunchAt  time.Time
	Threshold float64 // overlap threshold, 0.35 when zero
}

// Deps are the outside calls preflight makes; tests pass fakes.
// Every field is required except Now, which defaults to time.Now.
type Deps struct {
	Fetch        func(ctx context.Context, url string) (status int, contentType string, err error)
	Limits       func(ctx context.Context, connID uuid.UUID) (map[string]buffer.Limit, error)
	Config       func(ctx context.Context, connID uuid.UUID) (buffer.Config, error)
	RateHeadroom func(ctx context.Context, connID uuid.UUID) (remaining int, err error)
	Now          func() time.Time
}

// HTTPFetch is a Deps.Fetch using a clean client: HEAD, then GET if HEAD is refused.
func HTTPFetch(ctx context.Context, u string) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	client := &http.Client{}
	do := func(method string) (int, string, error) {
		req, err := http.NewRequestWithContext(ctx, method, u, nil)
		if err != nil {
			return 0, "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Content-Type"), nil
	}
	status, ct, err := do(http.MethodHead)
	if err != nil || status != http.StatusOK {
		if s2, c2, err2 := do(http.MethodGet); err2 == nil {
			return s2, c2, nil
		}
		return status, ct, err
	}
	return status, ct, nil
}

// Run runs all seven checks in order; it never short-circuits.
func Run(ctx context.Context, deps Deps, l Launch) ([]Check, bool) {
	cs := []Check{
		checkURLs(ctx, deps, l.Assets),
		checkLimits(ctx, deps, l),
		checkRules(ctx, deps, l.ConnectionID, l.CaptainDrafts),
		checkLinkedIn(l.CaptainDrafts),
		{ID: 5, Name: "Crew draft overlap", OK: true},
		checkTime(deps, l.LaunchAt),
		checkRate(ctx, deps, l),
	}
	return cs, allOK(cs)
}

// ForCrewDraft runs checks 1, 3, 4 and 5 for one crew member's post.
func ForCrewDraft(ctx context.Context, deps Deps, d CrewDraft, recent []string) ([]Check, bool) {
	urls := make([]Asset, 0, len(d.AssetURLs))
	for _, u := range d.AssetURLs {
		urls = append(urls, Asset{URL: u, Label: lastSegment(u)})
	}
	cs := []Check{
		checkURLs(ctx, deps, urls),
		checkRules(ctx, deps, d.ConnectionID, []Draft{d.Draft}),
		checkLinkedIn([]Draft{d.Draft}),
		checkOverlap(d, recent),
	}
	return cs, allOK(cs)
}

func allOK(cs []Check) bool {
	for _, c := range cs {
		if !c.OK {
			return false
		}
	}
	return true
}

func fail(id int, name, fix string) Check { return Check{ID: id, Name: name, Fix: fix} }

func pass(id int, name string) Check { return Check{ID: id, Name: name, OK: true} }

func lastSegment(u string) string {
	if p, err := url.Parse(u); err == nil && p.Path != "" {
		if s := path.Base(p.Path); s != "/" && s != "." {
			return s
		}
	}
	return u
}

func checkURLs(ctx context.Context, deps Deps, assets []Asset) Check {
	for _, a := range assets {
		label := a.Label
		if label == "" {
			label = lastSegment(a.URL)
		}
		status, ct, err := deps.Fetch(ctx, a.URL)
		var problem string
		switch {
		case err != nil:
			problem = "no answer"
		case status != http.StatusOK:
			problem = fmt.Sprintf("HTTP %d", status)
		case !strings.HasPrefix(strings.ToLower(ct), "image/"):
			problem = "not an image"
		default:
			continue
		}
		// No card re-render action exists yet (TODOS.md), so the fix offers what does.
		return fail(1, checkNameImages, fmt.Sprintf("Card image for %s could not be fetched (%s). Untick %s, or create the launch again.", label, problem, label))
	}
	return pass(1, checkNameImages)
}

// checkLimits applies Buffer's dailyPostingLimits, which describe today in
// each channel's timezone, to a launch within limitsHorizon; a later one passes.
func checkLimits(ctx context.Context, deps Deps, l Launch) Check {
	const name = "Daily posting limits"
	if l.LaunchAt.Sub(clock(deps)) > limitsHorizon {
		return pass(2, name)
	}
	limits, err := deps.Limits(ctx, l.ConnectionID)
	if err != nil {
		return fail(2, name, "Buffer posting limits could not be read. Retry in a moment.")
	}
	for _, d := range l.CaptainDrafts {
		if lim, ok := limits[d.ChannelID]; ok && lim.IsAtLimit {
			label := d.Name
			if label == "" {
				label = d.Service
			}
			return fail(2, name, fmt.Sprintf("%s has hit today's posting limit. Pick a launch time tomorrow or untick it.", label))
		}
	}
	return pass(2, name)
}

func checkRules(ctx context.Context, deps Deps, conn uuid.UUID, drafts []Draft) Check {
	const name = "Network rules"
	cfg, err := deps.Config(ctx, conn)
	if err != nil {
		return fail(3, name, "Buffer network rules could not be read. Retry in a moment.")
	}
	for _, d := range drafts {
		r := drafting.RulesFor(drafting.Network(d.Network), &cfg)
		for _, t := range append([]string{d.Text}, d.Thread...) {
			if n := len([]rune(t)); n > r.MaxChars {
				return fail(3, name, fmt.Sprintf("%s post is %d characters over %d. Shorten it.", d.Network, n-r.MaxChars, r.MaxChars))
			}
		}
		if d.HasAsset && !r.ImageOK {
			return fail(3, name, fmt.Sprintf("%s does not take images. Remove the image from that post.", d.Network))
		}
		if !d.HasAsset && strings.EqualFold(d.Network, "instagram") {
			return fail(3, name, "Instagram post has no image. Turn the card on or add an image.")
		}
	}
	return pass(3, name)
}

var linkRe = regexp.MustCompile(`https?://\S+`)

func checkLinkedIn(drafts []Draft) Check {
	const name = "LinkedIn link placement"
	for _, d := range drafts {
		if strings.EqualFold(d.Network, "linkedin") && d.HasAsset && linkRe.MatchString(d.Text) {
			return fail(4, name, "LinkedIn draft carries a link and an image together. Move the link to the first comment.")
		}
	}
	return pass(4, name)
}

func checkOverlap(d CrewDraft, recent []string) Check {
	const name = "Crew draft overlap"
	th := d.Threshold
	if th == 0 {
		th = defaultOverlap
	}
	for _, r := range recent {
		if drafting.TrigramOverlap(d.Text, r) > th {
			return fail(5, name, "This draft reads like one already queued. Edit a line or two.")
		}
	}
	return pass(5, name)
}

func checkTime(deps Deps, at time.Time) Check {
	const name = "Launch time"
	now := clock(deps)
	switch {
	case !at.UTC().After(now.UTC()):
		return fail(6, name, "Launch time is in the past. Pick a later time.")
	case at.UTC().After(now.UTC().Add(maxLead)):
		return fail(6, name, "Launch time is more than 30 days away. Pick a closer time.")
	}
	return pass(6, name)
}

func checkRate(ctx context.Context, deps Deps, l Launch) Check {
	const name = "Buffer rate headroom"
	left, err := deps.RateHeadroom(ctx, l.ConnectionID)
	if err != nil {
		return fail(7, name, "Buffer rate budget could not be read. Retry in a moment.")
	}
	if left < l.ExpectedCalls+rateMargin {
		return fail(7, name, fmt.Sprintf("Buffer has %d calls left this window; this launch needs about %d. Wait for the window to reset.", left, l.ExpectedCalls))
	}
	return pass(7, name)
}

func clock(deps Deps) time.Time {
	if deps.Now == nil {
		return time.Now()
	}
	return deps.Now()
}
