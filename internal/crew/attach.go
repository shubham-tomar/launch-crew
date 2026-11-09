package crew

import "github.com/shubham-tomar/launchcrew/internal/db/sqlc"

// Image is one launch upload as the posting code sees it: the original, its
// size, and the copy under Bluesky's limit when the original is over it.
type Image struct {
	URL   string
	Bytes int64
	Small string
}

// forBytes is the URL to send a network whose per-image limit is maxBytes
// (0 for none): the original when it fits, else the small copy, else "".
func (i Image) forBytes(maxBytes int) string {
	if maxBytes <= 0 || i.Bytes <= int64(maxBytes) {
		return i.URL
	}
	return i.Small
}

// Attachments is what a post carries, in order: the card slot (the hero or
// a card) when includeCard is on and card is set, then the launch's images,
// cut to max, the network's images-per-post limit. maxBytes is the network's
// per-image size limit, 0 when Buffer resizes for it; the card or an image
// over it goes as its small copy or not at all. A max of zero or less means
// the network takes no images.
func Attachments(card Image, includeCard bool, images []Image, max, maxBytes int) []string {
	if max <= 0 {
		return nil
	}
	var out []string
	if includeCard && card.URL != "" {
		if u := card.forBytes(maxBytes); u != "" {
			out = append(out, u)
		}
	}
	for _, img := range images {
		if len(out) >= max {
			break
		}
		if u := img.forBytes(maxBytes); u != "" {
			out = append(out, u)
		}
	}
	if len(out) > max {
		out = out[:max]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ImageOf is one stored asset row as an Image.
func ImageOf(a sqlc.LaunchAsset) Image { return Image{URL: a.R2Url, Bytes: a.Bytes, Small: a.SmallUrl} }

// ImagesOf maps stored image rows to Images.
func ImagesOf(rows []sqlc.LaunchAsset) []Image {
	out := make([]Image, 0, len(rows))
	for _, a := range rows {
		out = append(out, ImageOf(a))
	}
	return out
}
