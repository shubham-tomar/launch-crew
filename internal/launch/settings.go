package launch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/github/comments"
)

// maxDraftNotes bounds the captain's notes to the model, so they stay notes.
const maxDraftNotes = 600

// SettingsView is the captain's templates page: their PR comment wording
// and the notes the model reads while drafting the crew's posts. Empty
// strings mean the defaults, which the view carries too.
type SettingsView struct {
	CommentTemplate string `json:"commentTemplate"`
	DraftNotes      string `json:"draftNotes"`
	// DefaultComment is the built-in comment template, for the editor's reset.
	DefaultComment string `json:"defaultComment"`
	// Placeholders are the fields a comment template may use.
	Placeholders []string `json:"placeholders"`
	// Preview is the comment as it would read for a sample launch.
	Preview string `json:"preview"`
	// MaxCommentLen and MaxNotesLen are the editor's limits.
	MaxCommentLen int `json:"maxCommentLen"`
	MaxNotesLen   int `json:"maxNotesLen"`
}

// SettingsPatch is a save of the templates page. Nil fields are left alone.
type SettingsPatch struct {
	CommentTemplate *string `json:"commentTemplate"`
	DraftNotes      *string `json:"draftNotes"`
}

// sampleVars is the launch the preview is rendered for.
var sampleVars = comments.Vars{
	Captain: "you", Project: "rocket", Version: "v2.4.0",
	Credited: comments.Credited([]comments.PR{{Number: 42, Title: "Split-pane resize with keyboard"}}),
	Link:     "https://launchcrew.example/s/abc", Settings: "https://launchcrew.example/settings",
}

func settingsView(cs sqlc.CaptainSetting) SettingsView {
	return SettingsView{
		CommentTemplate: cs.CommentTemplate, DraftNotes: cs.DraftNotes,
		DefaultComment: comments.DefaultTemplate, Placeholders: comments.Placeholders,
		Preview:       comments.Render(cs.CommentTemplate, sampleVars),
		MaxCommentLen: comments.MaxTemplateLen, MaxNotesLen: maxDraftNotes,
	}
}

// GetSettings reads the captain's templates; a captain who never saved any
// gets the defaults.
func GetSettings(ctx context.Context, d Deps, userID uuid.UUID) (SettingsView, error) {
	cs, err := d.Q.GetCaptainSettings(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return settingsView(sqlc.CaptainSetting{UserID: userID}), nil
	}
	if err != nil {
		return SettingsView{}, fmt.Errorf("launch: load settings: %w", err)
	}
	return settingsView(cs), nil
}

// SaveSettings stores the captain's templates and returns the page.
func SaveSettings(ctx context.Context, d Deps, userID uuid.UUID, p SettingsPatch) (SettingsView, error) {
	cur, err := d.Q.GetCaptainSettings(ctx, userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return SettingsView{}, fmt.Errorf("launch: load settings: %w", err)
	}
	if p.CommentTemplate != nil {
		cur.CommentTemplate = strings.TrimSpace(*p.CommentTemplate)
		if err := comments.ValidateTemplate(cur.CommentTemplate); err != nil {
			return SettingsView{}, invalid("%s", err.Error())
		}
	}
	if p.DraftNotes != nil {
		cur.DraftNotes = strings.TrimSpace(*p.DraftNotes)
		if len([]rune(cur.DraftNotes)) > maxDraftNotes {
			return SettingsView{}, invalid("keep the drafting notes under %d characters", maxDraftNotes)
		}
	}
	cs, err := d.Q.UpsertCaptainSettings(ctx, sqlc.UpsertCaptainSettingsParams{
		UserID: userID, CommentTemplate: cur.CommentTemplate, DraftNotes: cur.DraftNotes,
	})
	if err != nil {
		return SettingsView{}, fmt.Errorf("launch: save settings: %w", err)
	}
	return settingsView(cs), nil
}

// commentTemplate is the launch owner's PR comment template, "" for the default.
func commentTemplate(ctx context.Context, d Deps, s *state) string {
	if !s.l.OwnerUserID.Valid {
		return ""
	}
	cs, err := d.Q.GetCaptainSettings(ctx, s.l.OwnerUserID.UUID)
	if err != nil {
		return ""
	}
	return cs.CommentTemplate
}
