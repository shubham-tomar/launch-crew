package crew

import (
	"context"
	"errors"
	"fmt"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
)

// DocumentStore builds a PDF from stored images, in order, and returns its
// URL; cards.Uploader is one.
type DocumentStore interface {
	PutDocument(ctx context.Context, imageURLs []string) (string, error)
}

// ErrNoDocument means a LinkedIn document was asked for and could not be made.
var ErrNoDocument = errors.New("crew: the LinkedIn document could not be made")

// DocumentPages are the files a LinkedIn document is made of: the card slot
// when includeCard is on, then the images, each as its small copy when it
// has one (LinkedIn shows a page about 1,200 px wide, so the copy is plenty
// and keeps the PDF light).
func DocumentPages(card Image, includeCard bool, images []Image) []string {
	pick := func(i Image) string {
		if i.Small != "" {
			return i.Small
		}
		return i.URL
	}
	var out []string
	if includeCard && card.URL != "" {
		out = append(out, pick(card))
	}
	for _, img := range images {
		if img.URL != "" {
			out = append(out, pick(img))
		}
	}
	return out
}

// ApplyFormat turns in into the chosen format. A story keeps only its images
// (a story shows no text, thread or comment, so those are dropped). A
// document replaces the images with one PDF of pages, titled title, with the first page as its thumbnail. A plain
// post, or a format the channel cannot take, leaves in as it is.
func ApplyFormat(ctx context.Context, docs DocumentStore, in *buffer.CreatePostInput, service, format, title string, pages []string) error {
	if !buffer.FormatAllowed(service, format) {
		return nil
	}
	switch format {
	case buffer.FormatStory:
		in.Story = true
		in.Thread, in.FirstComment = nil, ""
	case buffer.FormatDocument:
		if len(pages) == 0 {
			return nil
		}
		if docs == nil {
			return fmt.Errorf("%w: no document store", ErrNoDocument)
		}
		url, err := docs.PutDocument(ctx, pages)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrNoDocument, err)
		}
		// The first page doubles as the thumbnail Buffer requires.
		in.Document = &buffer.Document{URL: url, Title: title, ThumbnailURL: pages[0]}
		in.AssetURLs = nil
	}
	return nil
}
