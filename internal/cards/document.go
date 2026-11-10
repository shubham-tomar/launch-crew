package cards

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"strconv"
	"time"

	"github.com/go-pdf/fpdf"
)

// docEpoch stamps every document, so a document's bytes depend only on its pages.
var docEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// docMaxEdge is the long side of a page, in points. LinkedIn shows a document
// about 1,200 px wide, so larger pages only make the file heavier.
const docMaxEdge = 2000

// BuildPDF lays the images out one per page, each page the shape of its
// image, in order. LinkedIn shows a PDF as a swipeable document, which is
// the only carousel it has. A JPEG goes in as it is unless its Exif says it
// must be turned; anything else is drawn onto white and re-encoded as a
// JPEG, one at a time like the Bluesky copies.
func BuildPDF(ctx context.Context, images [][]byte) ([]byte, error) {
	if len(images) == 0 {
		return nil, errors.New("cards: a document needs at least one image")
	}
	pdf := fpdf.NewCustom(&fpdf.InitType{UnitStr: "pt", Size: fpdf.SizeType{Wd: docMaxEdge, Ht: docMaxEdge}})
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0)
	pdf.SetCompression(true)
	// The same images make the same bytes, so the stored file is reused.
	pdf.SetCatalogSort(true)
	pdf.SetCreationDate(docEpoch)
	pdf.SetModificationDate(docEpoch)
	for i, data := range images {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		jpg, err := pageJPEG(ctx, data)
		if err != nil {
			return nil, fmt.Errorf("cards: page %d: %w", i+1, err)
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(jpg))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
			return nil, fmt.Errorf("cards: page %d: %w", i+1, ErrNotImage)
		}
		w, h := float64(cfg.Width), float64(cfg.Height)
		if long := max(w, h); long > docMaxEdge {
			w, h = w*docMaxEdge/long, h*docMaxEdge/long
		}
		name := "p" + strconv.Itoa(i)
		opts := fpdf.ImageOptions{ImageType: "JPG"}
		pdf.RegisterImageOptionsReader(name, opts, bytes.NewReader(jpg))
		pdf.AddPageFormat("P", fpdf.SizeType{Wd: w, Ht: h})
		pdf.ImageOptions(name, 0, 0, w, h, false, opts, 0, "")
	}
	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, fmt.Errorf("cards: write document: %w", err)
	}
	return out.Bytes(), nil
}

// pageJPEG is the JPEG a page embeds: the file itself when it is an upright
// JPEG, else a re-encoded copy no larger than a page.
func pageJPEG(ctx context.Context, data []byte) ([]byte, error) {
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: unreadable", ErrNotImage)
	}
	o := orientation(data)
	if format == "jpeg" && o == 1 {
		return data, nil
	}
	select {
	case copySlot <- struct{}{}:
		defer func() { <-copySlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
	if cfg.Width*cfg.Height*bytesPerPixel(cfg.ColorModel) > copyMaxBytes {
		return nil, fmt.Errorf("cards: %dx%d is too large for a document page", cfg.Width, cfg.Height)
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: unreadable", ErrNotImage)
	}
	return encodeJPEG(src, o, docMaxEdge, 90)
}
