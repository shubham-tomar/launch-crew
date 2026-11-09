package cards_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/r2fake"
)

// noisyPNG is a PNG that compresses badly, so a modest size is over 1 MB.
func noisyPNG(t testing.TB, w, h int) []byte {
	t.Helper()
	r := rand.New(rand.NewPCG(1, 2))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(r.IntN(256))
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pngHeader is a PNG that stops after IHDR: enough for DecodeConfig, nothing to decode.
func pngHeader(w, h int, bitDepth, colorType byte) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9] = bitDepth, colorType
	var b bytes.Buffer
	b.Write([]byte("\x89PNG\r\n\x1a\n"))
	chunk := append([]byte("IHDR"), ihdr...)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func TestBlueskyCopyFitsUnderOneMB(t *testing.T) {
	src := noisyPNG(t, 900, 700)
	if len(src) <= cards.BlueskyMaxBytes {
		t.Fatalf("fixture is only %d bytes", len(src))
	}
	out, err := cards.BlueskyCopy(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > cards.BlueskyMaxBytes {
		t.Errorf("copy is %d bytes, want at most %d", len(out), cards.BlueskyMaxBytes)
	}
	if _, format, err := image.DecodeConfig(bytes.NewReader(out)); err != nil || format != "jpeg" {
		t.Fatalf("copy format %q err %v", format, err)
	}
}

// The decode budget is in bytes, not pixels: a 16-bit PNG costs twice as much
// per pixel, so 14 MP of it is refused where 14 MP of 8-bit would be decoded.
func TestBlueskyCopyBudgetCountsSixteenBitPNGs(t *testing.T) {
	const w, h = 4000, 3500 // 14 MP: 56 MB at 8 bit, 112 MB at 16 bit
	_, err := cards.BlueskyCopy(context.Background(), pngHeader(w, h, 16, 6))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("16-bit 14 MP: err = %v, want too large", err)
	}
	_, err = cards.BlueskyCopy(context.Background(), pngHeader(w, h, 8, 6))
	if err == nil || strings.Contains(err.Error(), "too large") {
		t.Fatalf("8-bit 14 MP: err = %v, want a decode failure, not the budget", err)
	}
	_, err = cards.BlueskyCopy(context.Background(), pngHeader(6000, 4500, 8, 6))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("8-bit 27 MP: err = %v, want too large", err)
	}
}

func TestPutCardMakesACopyOverOneMB(t *testing.T) {
	r2 := r2fake.New("bucket")
	t.Cleanup(r2.Close)
	u := cards.NewUploader(r2.URL, "bucket", "key", "secret", "https://cdn.example/")
	ctx := context.Background()

	small := noisyPNG(t, 40, 40)
	s, err := u.PutCard(ctx, small)
	if err != nil {
		t.Fatal(err)
	}
	if s.Bytes != int64(len(small)) || s.Small != "" || !strings.HasSuffix(s.URL, ".png") {
		t.Errorf("small card stored as %+v", s)
	}

	big := noisyPNG(t, 900, 700)
	b, err := u.PutCard(ctx, big)
	if err != nil {
		t.Fatal(err)
	}
	if b.Bytes != int64(len(big)) || b.Small == "" {
		t.Fatalf("big card stored as %+v, want a small copy", b)
	}
	o, ok := r2.Object(strings.TrimPrefix(b.Small, "https://cdn.example/"))
	if !ok {
		t.Fatalf("no object for %s", b.Small)
	}
	if o.ContentType != "image/jpeg" || len(o.Body) > cards.BlueskyMaxBytes {
		t.Errorf("copy is %s, %d bytes", o.ContentType, len(o.Body))
	}
}

// withOrientation inserts an Exif APP1 segment carrying Orientation o after a JPEG's SOI.
func withOrientation(jpg []byte, o uint16) []byte {
	tiff := []byte("II*\x00")
	tiff = binary.LittleEndian.AppendUint32(tiff, 8) // IFD0 at offset 8
	tiff = binary.LittleEndian.AppendUint16(tiff, 1) // one entry
	tiff = binary.LittleEndian.AppendUint16(tiff, 0x0112)
	tiff = binary.LittleEndian.AppendUint16(tiff, 3) // SHORT
	tiff = binary.LittleEndian.AppendUint32(tiff, 1)
	tiff = binary.LittleEndian.AppendUint16(tiff, o)
	tiff = append(tiff, 0, 0)
	tiff = binary.LittleEndian.AppendUint32(tiff, 0) // no next IFD
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(payload)+2))
	seg = append(seg, payload...)
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

func TestBlueskyCopyKeepsOrientation(t *testing.T) {
	// 20 wide × 30 tall: a red block at the top-left, a blue block at the
	// bottom-left. Blocks, not pixels: JPEG smears colour across 2×2 cells.
	img := image.NewRGBA(image.Rect(0, 0, 20, 30))
	for y := 0; y < 30; y++ {
		for x := 0; x < 20; x++ {
			c := color.RGBA{255, 255, 255, 255}
			if x < 10 && y < 10 {
				c = color.RGBA{255, 0, 0, 255}
			} else if x < 10 && y >= 20 {
				c = color.RGBA{0, 0, 255, 255}
			}
			img.Set(x, y, c)
		}
	}
	var j bytes.Buffer
	if err := jpeg.Encode(&j, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	src := withOrientation(j.Bytes(), 6) // rotate 90° clockwise to display
	out, err := cards.BlueskyCopy(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := got.Bounds(); b.Dx() != 30 || b.Dy() != 20 {
		t.Fatalf("copy is %dx%d, want 30x20 after rotation", b.Dx(), b.Dy())
	}
	r, _, bl, _ := got.At(25, 5).RGBA() // red block moved to the top-right
	if r>>8 < 150 || bl>>8 > 120 {
		t.Errorf("top-right = r%d b%d, want red", r>>8, bl>>8)
	}
	r, _, bl, _ = got.At(5, 5).RGBA() // blue block moved to the top-left
	if bl>>8 < 150 || r>>8 > 120 {
		t.Errorf("top-left = r%d b%d, want blue", r>>8, bl>>8)
	}
	// Without Exif the copy keeps the source's own shape.
	out, err = cards.BlueskyCopy(context.Background(), j.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _, _ := image.DecodeConfig(bytes.NewReader(out)); cfg.Width != 20 || cfg.Height != 30 {
		t.Errorf("unoriented copy is %dx%d, want 20x30", cfg.Width, cfg.Height)
	}
}
