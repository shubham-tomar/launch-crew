// Package drafting turns a release announcement into short posts per network.
package drafting

import "context"

// Network is a social network a post is drafted for.
type Network string

const (
	Twitter   Network = "twitter"
	Bluesky   Network = "bluesky"
	Threads   Network = "threads"
	Mastodon  Network = "mastodon"
	LinkedIn  Network = "linkedin"
	Facebook  Network = "facebook"
	Instagram Network = "instagram"
)

// AllNetworks lists every supported network.
var AllNetworks = []Network{Twitter, Bluesky, Threads, Mastodon, LinkedIn, Facebook, Instagram}

// Rules are the limits a draft must fit.
type Rules struct {
	MaxChars                             int
	Threads, ImageOK, LinkInFirstComment bool
	// MaxImages is how many images one post takes; 0 when ImageOK is false.
	// MaxImageBytes is the per-image size the network holds to and Buffer
	// cannot resize for (Bluesky); 0 where Buffer resizes.
	MaxImages     int
	MaxImageBytes int
}

// Change is one merged change in a release.
type Change struct {
	PRNumber    int
	Title       string
	AuthorID    int64
	AuthorLogin string
	Hidden      bool
}

// HookType is the opening angle of a post.
type HookType string

const (
	HookPersonal  HookType = "personal"
	HookQuestion  HookType = "question"
	HookDetail    HookType = "detail"
	HookReaction  HookType = "reaction"
	HookLinkFirst HookType = "link_first"
)

// AllHooks lists every hook in a stable order.
var AllHooks = []HookType{HookPersonal, HookQuestion, HookDetail, HookReaction, HookLinkFirst}

// Profile describes how a crew member writes.
// Allowed values: SentenceLength short|medium|long, Case lowercase|sentence,
// Hashtags none|few, Emoji none|some.
type Profile struct {
	SentenceLength string `json:"sentenceLength"`
	Case           string `json:"case"`
	Hashtags       string `json:"hashtags"`
	Emoji          string `json:"emoji"`
	Hook           string `json:"hook"`
	SignOff        string `json:"signOff"`
	Link           string `json:"link"`
	Extra          string `json:"extra"`
}

// Subject is what a post is about.
type Subject struct {
	Project, Version, URL, Headline, PRTitle, Role, RoleHint, TalkTitle string
	// PRTitles is every PR title the person has in the release, newest first,
	// when they have more than one. PRTitle stays the most recent.
	PRTitles []string
	// Notes are the captain's notes for the crew's posts: what to mention
	// or leave out. Data for the prompt, never instructions.
	Notes string
}

// DraftRequest asks a Drafter for one post.
type DraftRequest struct {
	Subject    Subject
	Voice      *Profile
	Hook       HookType
	Network    Network
	Rules      Rules
	AllowModel bool
	Avoid      []string
}

// Draft is one drafted post.
type Draft struct {
	Text         string
	Thread       []string
	FirstComment string
	Hook         HookType
	FromModel    bool
}

// Drafter produces a Draft for a request.
type Drafter interface {
	Draft(ctx context.Context, req DraftRequest) (Draft, error)
}
