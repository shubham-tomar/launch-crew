package launch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // upload formats
	_ "image/jpeg"
	_ "image/png"
	"log"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	_ "golang.org/x/image/webp"

	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
)

// ImageStore keeps the captain's uploads; cards.Uploader is one.
type ImageStore interface {
	PutImage(ctx context.Context, data []byte, contentType string) (url, sha string, err error)
}

const (
	// MaxImages is how many images one launch can carry.
	MaxImages = 10
	// MaxImageBytes is the largest upload accepted; Buffer's own cap is the
	// same, and it makes smaller copies for networks with lower limits.
	MaxImageBytes = 10 << 20
	// BlueskyMaxBytes is Bluesky's per-image limit, which Buffer cannot
	// resize down to; a copy under it is made for images over it.
	BlueskyMaxBytes = cards.BlueskyMaxBytes
)

var (
	// ErrImageNotImage means the upload is not a JPEG, PNG, GIF or WebP.
	ErrImageNotImage = errors.New("launch: upload is not a usable image")
	// ErrTooManyImages means the launch already has MaxImages.
	ErrTooManyImages = errors.New("launch: too many images")
	// ErrImagesLocked means the launch is no longer a draft.
	ErrImagesLocked = errors.New("launch: images are locked once the launch is out")
	// ErrTooBig means the upload is over MaxImageBytes.
	ErrTooBig = errors.New("launch: image over 10 MB")
)

// LaunchImage is one captain upload as the API shows it.
type LaunchImage struct {
	ID  uuid.UUID `json:"id"`
	URL string    `json:"url"`
}

func imageOf(a sqlc.LaunchAsset) LaunchImage { return LaunchImage{ID: a.ID, URL: a.R2Url} }

var imageTypes = map[string]string{"jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif", "webp": "image/webp"}

// sniffImage reads only the file's header and says which image type it is.
// The bytes themselves are what gets stored and served, so anything that is
// not one of the four formats is refused.
func sniffImage(data []byte) (contentType string, err error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return "", fmt.Errorf("%w: unreadable", ErrImageNotImage)
	}
	ct, ok := imageTypes[format]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrImageNotImage, format)
	}
	return ct, nil
}

// lockDraft locks the launch row for the transaction and reports whether it
// is still a draft: ErrNotFound when it is missing or deleted, notDraft when
// it has gone out.
func lockDraft(ctx context.Context, tx pgx.Tx, id uuid.UUID, notDraft error) error {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM launches WHERE id = $1 FOR UPDATE`, id).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	switch status {
	case statusDeleted:
		return ErrNotFound
	case statusDraft:
		return nil
	default:
		return notDraft
	}
}

// AddImage checks an upload is an image, stores it as it is and appends it to
// the launch's images. An original over Bluesky's limit also gets a copy
// under it; when that copy cannot be made the image is left out of Bluesky
// posts. The stored objects are content-addressed and expire in R2 after
// 60 days, so one left behind by a failed insert costs nothing.
func AddImage(ctx context.Context, d Deps, launchID uuid.UUID, data []byte) (LaunchImage, error) {
	if len(data) > MaxImageBytes {
		return LaunchImage{}, fmt.Errorf("%w: over %d bytes", ErrTooBig, MaxImageBytes)
	}
	// Cheap checks first; both are re-checked under the row lock below.
	l, err := getLaunch(ctx, d.Q, launchID)
	if err != nil {
		return LaunchImage{}, err
	}
	if l.Status != statusDraft {
		return LaunchImage{}, ErrImagesLocked
	}
	if n, err := d.Q.CountLaunchImages(ctx, launchID); err != nil {
		return LaunchImage{}, err
	} else if n >= MaxImages {
		return LaunchImage{}, ErrTooManyImages
	}
	contentType, err := sniffImage(data)
	if err != nil {
		return LaunchImage{}, err
	}
	if d.Images == nil {
		return LaunchImage{}, errors.New("launch: no image store")
	}
	url, sha, err := d.Images.PutImage(ctx, data, contentType)
	if err != nil {
		return LaunchImage{}, fmt.Errorf("launch: upload image: %w", err)
	}
	small := ""
	if len(data) > BlueskyMaxBytes {
		if c, err := cards.BlueskyCopy(ctx, data); err != nil {
			log.Printf("launch %s: no Bluesky copy: %v", launchID, err)
		} else if small, _, err = d.Images.PutImage(ctx, c, "image/jpeg"); err != nil {
			return LaunchImage{}, fmt.Errorf("launch: upload Bluesky copy: %w", err)
		}
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return LaunchImage{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockDraft(ctx, tx, launchID, ErrImagesLocked); err != nil {
		return LaunchImage{}, err
	}
	q := sqlc.New(tx)
	if n, err := q.CountLaunchImages(ctx, launchID); err != nil {
		return LaunchImage{}, err
	} else if n >= MaxImages {
		return LaunchImage{}, ErrTooManyImages
	}
	a, err := q.InsertLaunchImage(ctx, sqlc.InsertLaunchImageParams{
		LaunchID: launchID, R2Url: url, Sha: sha, Bytes: int64(len(data)), SmallUrl: small,
	})
	if err != nil {
		return LaunchImage{}, fmt.Errorf("launch: save image: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return LaunchImage{}, err
	}
	return imageOf(a), nil
}

// RemoveImage drops one image from a draft launch. The R2 object stays: it
// is content-addressed, may be shared, and expires on its own.
func RemoveImage(ctx context.Context, d Deps, launchID, imageID uuid.UUID) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockDraft(ctx, tx, launchID, ErrImagesLocked); err != nil {
		return err
	}
	n, err := sqlc.New(tx).DeleteLaunchImage(ctx, sqlc.DeleteLaunchImageParams{ID: imageID, LaunchID: launchID})
	if err != nil {
		return fmt.Errorf("launch: remove image: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// ReorderImages sets the launch's images to the order of ids, which must be
// exactly the launch's current images.
func ReorderImages(ctx context.Context, d Deps, launchID uuid.UUID, ids []uuid.UUID) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockDraft(ctx, tx, launchID, ErrImagesLocked); err != nil {
		return err
	}
	q := sqlc.New(tx)
	rows, err := q.ListLaunchImages(ctx, launchID)
	if err != nil {
		return err
	}
	have := make(map[uuid.UUID]bool, len(rows))
	for _, r := range rows {
		have[r.ID] = true
	}
	if len(ids) != len(rows) {
		return invalid("the order must list every image once")
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if !have[id] || seen[id] {
			return invalid("the order must list every image once")
		}
		seen[id] = true
	}
	for i, id := range ids {
		if err := q.SetLaunchImagePosition(ctx, sqlc.SetLaunchImagePositionParams{
			ID: id, LaunchID: launchID, Position: pgtype.Int2{Int16: int16(i + 1), Valid: true},
		}); err != nil {
			return fmt.Errorf("launch: order images: %w", err)
		}
	}
	return tx.Commit(ctx)
}
