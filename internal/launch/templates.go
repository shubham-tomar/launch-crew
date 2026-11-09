package launch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/github/comments"
)

const (
	// maxDraftNotes bounds the captain's notes to the model, so they stay notes.
	maxDraftNotes = 600
	// maxTemplateName and maxTemplates keep the picker short.
	maxTemplateName = 40
	maxTemplates    = 20
)

// Template is one of the captain's named templates: the PR comment wording
// and the notes the model reads while drafting the crew's posts. Empty
// strings mean the built-in comment and no notes.
type Template struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	CommentTemplate string    `json:"commentTemplate"`
	DraftNotes      string    `json:"draftNotes"`
	// Preview is the comment as it would read for a sample launch.
	Preview string `json:"preview"`
}

// TemplatesView is the templates page: the captain's templates and what the
// editor needs to check and preview them.
type TemplatesView struct {
	Templates []Template `json:"templates"`
	// DefaultComment is the built-in comment template, used when a launch has
	// no template; DefaultPreview is it rendered for the sample launch.
	DefaultComment string `json:"defaultComment"`
	DefaultPreview string `json:"defaultPreview"`
	// Placeholders are the fields a comment template may use.
	Placeholders  []string `json:"placeholders"`
	MaxCommentLen int      `json:"maxCommentLen"`
	MaxNotesLen   int      `json:"maxNotesLen"`
	MaxNameLen    int      `json:"maxNameLen"`
	MaxTemplates  int      `json:"maxTemplates"`
}

// TemplateInput is a create or a full save of one template.
type TemplateInput struct {
	Name            string `json:"name"`
	CommentTemplate string `json:"commentTemplate"`
	DraftNotes      string `json:"draftNotes"`
}

// sampleVars is the launch the preview is rendered for.
var sampleVars = comments.Vars{
	Captain: "you", Project: "rocket", Version: "v2.4.0",
	Credited: comments.Credited([]comments.PR{{Number: 42, Title: "Split-pane resize with keyboard"}}),
	Link:     "https://launchcrew.example/s/abc", Settings: "https://launchcrew.example/settings",
}

func templateOf(t sqlc.CaptainTemplate) Template {
	return Template{
		ID: t.ID, Name: t.Name, CommentTemplate: t.CommentTemplate, DraftNotes: t.DraftNotes,
		Preview: comments.Render(t.CommentTemplate, sampleVars),
	}
}

// Templates lists the captain's templates by name.
func Templates(ctx context.Context, d Deps, userID uuid.UUID) (TemplatesView, error) {
	ts, err := d.Q.ListCaptainTemplates(ctx, userID)
	if err != nil {
		return TemplatesView{}, fmt.Errorf("launch: list templates: %w", err)
	}
	v := TemplatesView{
		Templates:      make([]Template, 0, len(ts)),
		DefaultComment: comments.DefaultTemplate, DefaultPreview: comments.Render("", sampleVars),
		Placeholders: comments.Placeholders, MaxCommentLen: comments.MaxTemplateLen,
		MaxNotesLen: maxDraftNotes, MaxNameLen: maxTemplateName, MaxTemplates: maxTemplates,
	}
	for _, t := range ts {
		v.Templates = append(v.Templates, templateOf(t))
	}
	return v, nil
}

// clean trims and checks a template's fields.
func (in TemplateInput) clean() (TemplateInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.CommentTemplate = strings.TrimSpace(in.CommentTemplate)
	in.DraftNotes = strings.TrimSpace(in.DraftNotes)
	switch {
	case in.Name == "":
		return in, invalid("give the template a name")
	case len([]rune(in.Name)) > maxTemplateName:
		return in, invalid("keep the template name under %d characters", maxTemplateName)
	case len([]rune(in.DraftNotes)) > maxDraftNotes:
		return in, invalid("keep the drafting notes under %d characters", maxDraftNotes)
	}
	if err := comments.ValidateTemplate(in.CommentTemplate); err != nil {
		return in, invalid("%s", err.Error())
	}
	return in, nil
}

// nameTaken turns the unique-name violation into the captain's message.
func nameTaken(err error, name string) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return invalid("you already have a template called %q", name)
	}
	return err
}

// CreateTemplate saves a new template for the captain.
func CreateTemplate(ctx context.Context, d Deps, userID uuid.UUID, in TemplateInput) (Template, error) {
	in, err := in.clean()
	if err != nil {
		return Template{}, err
	}
	n, err := d.Q.CountCaptainTemplates(ctx, userID)
	if err != nil {
		return Template{}, fmt.Errorf("launch: count templates: %w", err)
	}
	if n >= maxTemplates {
		return Template{}, invalid("you can keep up to %d templates; delete one first", maxTemplates)
	}
	t, err := d.Q.CreateCaptainTemplate(ctx, sqlc.CreateCaptainTemplateParams{
		UserID: userID, Name: in.Name, CommentTemplate: in.CommentTemplate, DraftNotes: in.DraftNotes,
	})
	if err != nil {
		return Template{}, nameTaken(err, in.Name)
	}
	return templateOf(t), nil
}

// UpdateTemplate replaces one of the captain's templates. Anyone else's is ErrNotFound.
func UpdateTemplate(ctx context.Context, d Deps, userID, id uuid.UUID, in TemplateInput) (Template, error) {
	in, err := in.clean()
	if err != nil {
		return Template{}, err
	}
	t, err := d.Q.UpdateCaptainTemplate(ctx, sqlc.UpdateCaptainTemplateParams{
		ID: id, UserID: userID, Name: in.Name, CommentTemplate: in.CommentTemplate, DraftNotes: in.DraftNotes,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, ErrNotFound
	}
	if err != nil {
		return Template{}, nameTaken(err, in.Name)
	}
	return templateOf(t), nil
}

// DeleteTemplate removes one of the captain's templates; repos and launches
// that used it fall back to the built-in default.
func DeleteTemplate(ctx context.Context, d Deps, userID, id uuid.UUID) error {
	n, err := d.Q.DeleteCaptainTemplate(ctx, sqlc.DeleteCaptainTemplateParams{ID: id, UserID: userID})
	if err != nil {
		return fmt.Errorf("launch: delete template: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// templateRef resolves a template id from a form: "" is the built-in default,
// anything else must be one of the captain's own templates.
func templateRef(ctx context.Context, d Deps, userID uuid.UUID, raw string) (uuid.NullUUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return uuid.NullUUID{}, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.NullUUID{}, invalid("pick one of your templates")
	}
	t, err := d.Q.GetCaptainTemplate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && t.UserID != userID) {
		return uuid.NullUUID{}, invalid("pick one of your templates")
	}
	if err != nil {
		return uuid.NullUUID{}, fmt.Errorf("launch: load template: %w", err)
	}
	return uuid.NullUUID{UUID: id, Valid: true}, nil
}

// LaunchTemplate is the template a launch was created with; the zero value
// (built-in comment, no notes) when it has none or it was deleted.
func LaunchTemplate(ctx context.Context, q *sqlc.Queries, l sqlc.Launch) sqlc.CaptainTemplate {
	if !l.TemplateID.Valid {
		return sqlc.CaptainTemplate{}
	}
	t, err := q.GetCaptainTemplate(ctx, l.TemplateID.UUID)
	if err != nil {
		return sqlc.CaptainTemplate{}
	}
	return t
}

// commentTemplate is the launch's PR comment template, "" for the default.
func commentTemplate(ctx context.Context, d Deps, s *state) string {
	return LaunchTemplate(ctx, d.Q, s.l).CommentTemplate
}
