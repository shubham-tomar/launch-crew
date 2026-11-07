// Package trigger turns a GitHub release or a manual form into a LaunchSpec,
// and runs the ProcessRelease job that saves it as a draft launch.
package trigger

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/shubham-tomar/launchcrew/internal/drafting"
)

// Role is a crew role for manual launches, with the hint used in drafts.
type Role struct {
	Name string `json:"name"`
	Hint string `json:"hint"`
}

// DefaultRoles are used when a manual launch names none.
var DefaultRoles = []Role{
	{Name: "Attendee", Hint: "came along"},
	{Name: "Speaker", Hint: "spoke about"},
	{Name: "Organiser", Hint: "helped organise"},
}

// CrewSuggestion is a person suggested for the crew; Ticked ones are pre-selected.
type CrewSuggestion struct {
	GitHubUserID int64
	Login        string
	AvatarURL    string
	PRNumber     int
	PRTitle      string
	// PRs is every PR the person has in the release, newest first. PRNumber and
	// PRTitle are the first of them, the one the comment goes on.
	PRs    []PRRef
	Ticked bool
}

// PRRef names one PR.
type PRRef struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	// Lines added and removed, for the card's diff. Zero on rows saved before
	// they were kept.
	Added   int `json:"added,omitempty"`
	Deleted int `json:"deleted,omitempty"`
}

// LaunchSpec is what a trigger produces: everything needed to save a draft launch.
// Captain is the release author's login (empty for manual launches).
type LaunchSpec struct {
	Title, Summary, Source, ReleaseTag, ReleaseURL string
	Headlines, Chores                              []drafting.Change
	Crew                                           []CrewSuggestion
	HeroURL                                        string
	Roles                                          []Role
	RepoFullName                                   string
	ReleaseID                                      int64
	Captain                                        string
	// Solo is set when the release author is the only person in it; the
	// release card then leads with them.
	Solo *Solo
}

// Solo is what the solo release card shows about one person's release.
type Solo struct {
	Name, Login, AvatarURL string
	Commits                int
	Added, Deleted         int
	// Subjects are the commit subjects, newest first, for a release made
	// without PRs.
	Subjects []string
	// SubjectCount is every such subject, kept or not.
	SubjectCount int
	PrevTag      string
	PrevAt       time.Time
	Ordinal      int
}

// Project is the repository name for releases, or the title otherwise.
func (s LaunchSpec) Project() string {
	if _, name, ok := strings.Cut(s.RepoFullName, "/"); ok {
		return name
	}
	return s.Title
}

// Trigger builds a LaunchSpec from its input.
type Trigger interface {
	Build(ctx context.Context, in Input) (LaunchSpec, error)
}

// Input carries either a release webhook payload or a manual form.
type Input struct {
	ReleasePayload json.RawMessage
	Manual         *ManualInput
}

// ManualInput is the manual launch form.
type ManualInput struct {
	Title, Summary string
	Roles          []Role
	HeroURL        string
	// QRLogo is plane, none or custom ("" is plane); QRLogoURL is the image
	// for custom. Both are handled by the launch service, not by Build.
	QRLogo, QRLogoURL string
}
