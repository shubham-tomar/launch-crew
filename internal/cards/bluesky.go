package cards

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // the formats a copy may be made from
	"image/jpeg"
	_ "image/png"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	// BlueskyMaxBytes is Bluesky's per-image limit, which Buffer cannot
	// resize down to; a copy under it is made for images over it.
	BlueskyMaxBytes = 1_000_000
	// copyMaxBytes bounds what one copy decodes into memory on the 512 MB
	// server: 24 MP of 8-bit colour, about 100 MB. A 16-bit PNG decodes to
	// twice as much per pixel, so it gets half the pixels. Bigger originals
	// get no copy and are left out of Bluesky posts.
	copyMaxBytes = 24_000_000 * 4
	copyEdge     = 2000
)

// copySlot lets one copy decode at a time: two 100 MB decodes at once, from
// two captains or two tabs, would push the server past its memory.
var copySlot = make(chan struct{}, 1)

// testCopyHook runs while the slot is held; tests only.
var testCopyHook func()

// ErrNotImage means the bytes are not an image Go can read.
var ErrNotImage = errors.New("cards: not a readable image")

// BlueskyCopy re-encodes an image as a JPEG under BlueskyMaxBytes: scaled
// so its long edge is at most copyEdge, turned the way its Exif orientation
// says, then smaller and coarser until it fits. Copies run one at a time;
// a ctx that ends while waiting returns its error.
func BlueskyCopy(ctx context.Context, data []byte) ([]byte, error) {
	select {
	case copySlot <- struct{}{}:
		defer func() { <-copySlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if testCopyHook != nil {
		testCopyHook()
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: unreadable", ErrNotImage)
	}
	if cfg.Width*cfg.Height*bytesPerPixel(cfg.ColorModel) > copyMaxBytes {
		return nil, fmt.Errorf("cards: %dx%d is too large to copy for Bluesky", cfg.Width, cfg.Height)
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: unreadable", ErrNotImage)
	}
	o := orientation(data)
	for edge, quality := copyEdge, 85; ; edge, quality = edge*3/4, max(quality-10, 50) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out, err := encodeJPEG(src, o, edge, quality)
		if err != nil {
			return nil, err
		}
		if len(out) <= BlueskyMaxBytes {
			return out, nil
		}
		if edge < 200 {
			return nil, errors.New("cards: could not fit the image under Bluesky's limit")
		}
	}
}

// bytesPerPixel is what image.Decode allocates per pixel for the model the
// header announces: 16-bit samples decode to 8-byte pixels, everything else
// to at most 4.
func bytesPerPixel(m color.Model) int {
	switch m {
	case color.RGBA64Model, color.NRGBA64Model, color.Gray16Model:
		return 8
	}
	return 4
}

// encodeJPEG draws src onto white at most edge px on its long side (never
// upscaled), applies the Exif orientation o, and encodes at quality.
func encodeJPEG(src image.Image, o, edge, quality int) ([]byte, error) {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	if long := max(w, h); long > edge {
		scale := float64(edge) / float64(long)
		w = max(1, int(float64(w)*scale+0.5))
		h = max(1, int(float64(h)*scale+0.5))
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	if w == src.Bounds().Dx() && h == src.Bounds().Dy() {
		draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Over)
	} else {
		// ApproxBiLinear works per destination pixel with no scratch buffer;
		// the kernel scalers allocate width×height×32 bytes, too much here.
		xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, orient(dst, o), &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// orientation reads the Exif Orientation tag (1 to 8) of a JPEG; 1 when
// there is none or the file is not a JPEG. Only the APP1 segment is walked.
func orientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 { // start of scan, end of image
			return 1
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if n < 2 || i+2+n > len(data) {
			return 1
		}
		if marker == 0xE1 && n >= 16 && bytes.Equal(data[i+4:i+10], []byte("Exif\x00\x00")) {
			return tiffOrientation(data[i+10 : i+2+n])
		}
		i += 2 + n
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(t[2:]) != 42 {
		return 1
	}
	ifd := int(bo.Uint32(t[4:]))
	if ifd+2 > len(t) {
		return 1
	}
	count := int(bo.Uint16(t[ifd:]))
	for k := 0; k < count; k++ {
		e := ifd + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 && bo.Uint16(t[e+2:]) == 3 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orient returns img turned so it displays upright for Exif orientation o.
func orient(img *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return img
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	// Orientations 5 to 8 swap the axes.
	ow, oh := w, h
	if o >= 5 {
		ow, oh = h, w
	}
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			var sx, sy int
			switch o {
			case 2: // mirrored
				sx, sy = w-1-x, y
			case 3: // upside down
				sx, sy = w-1-x, h-1-y
			case 4: // flipped
				sx, sy = x, h-1-y
			case 5: // transposed
				sx, sy = y, x
			case 6: // rotated 90° clockwise to display
				sx, sy = y, h-1-x
			case 7: // transverse
				sx, sy = w-1-y, h-1-x
			case 8: // rotated 90° anticlockwise to display
				sx, sy = w-1-y, x
			}
			copy(out.Pix[out.PixOffset(x, y):out.PixOffset(x, y)+4], img.Pix[img.PixOffset(sx, sy):img.PixOffset(sx, sy)+4])
		}
	}
	return out
}
