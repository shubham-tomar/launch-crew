package launch_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/launch"
	"github.com/shubham-tomar/launchcrew/internal/trigger"
)

func encodePNG(t testing.TB, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// solidPNG is a w×h opaque image in one colour; different colours give different bytes.
func solidPNG(t testing.TB, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return encodePNG(t, img)
}

// noisyPNG is random pixels, so it hardly compresses: 900×700 is about 2.5 MB.
func noisyPNG(t testing.TB, w, h int) []byte {
	t.Helper()
	r := rand.New(rand.NewPCG(1, 2))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(r.UintN(256))
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	return encodePNG(t, img)
}

// A 1×1 lossy WebP, the smallest valid file; x/image/webp reads its header.
const tinyWebP = "UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA"

func TestSniffAcceptsTheFourFormats(t *testing.T) {
	var g bytes.Buffer
	if err := gif.Encode(&g, image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	var j bytes.Buffer
	if err := jpeg.Encode(&j, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	webp, err := base64.StdEncoding.DecodeString(tinyWebP)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, want string
		data       []byte
	}{
		{"png", "image/png", solidPNG(t, 4, 4, color.White)},
		{"jpeg", "image/jpeg", j.Bytes()},
		{"gif", "image/gif", g.Bytes()},
		{"webp", "image/webp", webp},
	} {
		got, err := launch.SniffImage(c.data)
		if err != nil || got != c.want {
			t.Errorf("%s: sniff = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

func TestSniffRefusesOtherBytes(t *testing.T) {
	for name, data := range map[string][]byte{
		"text": []byte("hello, not an image"),
		"svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"heic": []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"),
		"html": []byte("<!doctype html><img src=x onerror=alert(1)>"),
	} {
		if _, err := launch.SniffImage(data); !errors.Is(err, launch.ErrImageNotImage) {
			t.Errorf("%s: err = %v, want ErrImageNotImage", name, err)
		}
	}
}

func (e *env) images(t *testing.T) []sqlc.LaunchAsset {
	t.Helper()
	rows, err := e.q.ListLaunchImages(e.ctx, e.launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestUploadRejectsUndecodable(t *testing.T) {
	e := setup(t)
	_, err := launch.AddImage(e.ctx, e.deps, e.launch.ID, []byte("not an image at all"))
	if !errors.Is(err, launch.ErrImageNotImage) {
		t.Fatalf("err = %v, want ErrImageNotImage", err)
	}
	if n := len(e.images(t)); n != 0 {
		t.Errorf("%d image rows after a refused upload", n)
	}
	if e.r2.Len() != 0 {
		t.Errorf("%d objects uploaded for a refused image", e.r2.Len())
	}
}

// The original is stored as uploaded, under its own type; a small file gets no Bluesky copy.
func TestAddImageStoresTheOriginal(t *testing.T) {
	e := setup(t)
	data := solidPNG(t, 20, 10, color.RGBA{255, 0, 0, 255})
	a, err := launch.AddImage(e.ctx, e.deps, e.launch.ID, data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.URL, "https://cdn.example/uploads/") || !strings.HasSuffix(a.URL, ".png") {
		t.Errorf("url = %s, want uploads/<sha>.png", a.URL)
	}
	rows := e.images(t)
	if len(rows) != 1 || rows[0].ID != a.ID {
		t.Fatalf("rows = %+v", rows)
	}
	o, ok := e.r2.Object(strings.TrimPrefix(a.URL, "https://cdn.example/"))
	if !ok {
		t.Fatal("no stored object")
	}
	if !bytes.Equal(o.Body, data) || o.ContentType != "image/png" {
		t.Errorf("stored %d bytes as %q, want the original %d bytes as image/png", len(o.Body), o.ContentType, len(data))
	}
	if rows[0].Bytes != int64(len(data)) || rows[0].SmallUrl != "" {
		t.Errorf("bytes = %d small = %q, want %d and no copy", rows[0].Bytes, rows[0].SmallUrl, len(data))
	}
	if e.r2.Len() != 1 {
		t.Errorf("%d objects stored, want 1", e.r2.Len())
	}
}

func TestAddImageMakesABlueskyCopyOverOneMB(t *testing.T) {
	e := setup(t)
	data := noisyPNG(t, 900, 700)
	a, err := launch.AddImage(e.ctx, e.deps, e.launch.ID, data)
	if err != nil {
		t.Fatal(err)
	}
	rows := e.images(t)
	if len(rows) != 1 || rows[0].Bytes != int64(len(data)) {
		t.Fatalf("rows = %+v", rows)
	}
	small := rows[0].SmallUrl
	if small == "" || !strings.HasSuffix(small, ".jpg") || small == a.URL {
		t.Fatalf("small = %q, want a separate uploads/<sha>.jpg", small)
	}
	o, ok := e.r2.Object(strings.TrimPrefix(small, "https://cdn.example/"))
	if !ok {
		t.Fatal("no stored copy")
	}
	if len(o.Body) > launch.BlueskyMaxBytes || o.ContentType != "image/jpeg" {
		t.Errorf("copy is %d bytes as %q", len(o.Body), o.ContentType)
	}
	if e.r2.Len() != 2 {
		t.Errorf("%d objects stored, want the original and the copy", e.r2.Len())
	}
}

func TestAddImageKeepsOrder(t *testing.T) {
	e := setup(t)
	a, err := launch.AddImage(e.ctx, e.deps, e.launch.ID, solidPNG(t, 20, 10, color.RGBA{255, 0, 0, 255}))
	if err != nil {
		t.Fatal(err)
	}
	b, err := launch.AddImage(e.ctx, e.deps, e.launch.ID, solidPNG(t, 20, 10, color.RGBA{0, 255, 0, 255}))
	if err != nil {
		t.Fatal(err)
	}
	rows := e.images(t)
	if len(rows) != 2 || rows[0].ID != a.ID || rows[1].ID != b.ID {
		t.Fatalf("rows = %+v, want a then b", rows)
	}
	if rows[0].Position.Int16 != 1 || rows[1].Position.Int16 != 2 {
		t.Errorf("positions = %d,%d, want 1,2", rows[0].Position.Int16, rows[1].Position.Int16)
	}
}

func TestSameBytesTwice(t *testing.T) {
	e := setup(t)
	data := solidPNG(t, 8, 8, color.RGBA{9, 9, 9, 255})
	a, err := launch.AddImage(e.ctx, e.deps, e.launch.ID, data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := launch.AddImage(e.ctx, e.deps, e.launch.ID, data)
	if err != nil {
		t.Fatalf("second identical upload: %v", err)
	}
	if a.ID == b.ID {
		t.Fatal("identical bytes collapsed into one row")
	}
	if a.URL != b.URL {
		t.Errorf("identical bytes got different keys: %s vs %s", a.URL, b.URL)
	}
	if err := launch.RemoveImage(e.ctx, e.deps, e.launch.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	rows := e.images(t)
	if len(rows) != 1 || rows[0].ID != b.ID {
		t.Errorf("after removing a, rows = %+v", rows)
	}
}

func TestAddImageCapsAtTen(t *testing.T) {
	e := setup(t)
	for i := 0; i < launch.MaxImages; i++ {
		if _, err := launch.AddImage(e.ctx, e.deps, e.launch.ID, solidPNG(t, 4, 4, color.RGBA{uint8(i), 0, 0, 255})); err != nil {
			t.Fatalf("image %d: %v", i+1, err)
		}
	}
	_, err := launch.AddImage(e.ctx, e.deps, e.launch.ID, solidPNG(t, 4, 4, color.RGBA{99, 0, 0, 255}))
	if !errors.Is(err, launch.ErrTooManyImages) {
		t.Fatalf("11th image: err = %v, want ErrTooManyImages", err)
	}
	if n := len(e.images(t)); n != launch.MaxImages {
		t.Errorf("%d rows, want %d", n, launch.MaxImages)
	}
}

func TestImagesLockedAfterLaunch(t *testing.T) {
	e := setup(t)
	a, err := launch.AddImage(e.ctx, e.deps, e.launch.ID, solidPNG(t, 4, 4, color.RGBA{1, 2, 3, 255}))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.q.UpdateLaunchStatus(e.ctx, sqlc.UpdateLaunchStatusParams{ID: e.launch.ID, Status: "launched"}); err != nil {
		t.Fatal(err)
	}
	if _, err := launch.AddImage(e.ctx, e.deps, e.launch.ID, solidPNG(t, 4, 4, color.RGBA{4, 5, 6, 255})); !errors.Is(err, launch.ErrImagesLocked) {
		t.Errorf("add: err = %v, want ErrImagesLocked", err)
	}
	if err := launch.RemoveImage(e.ctx, e.deps, e.launch.ID, a.ID); !errors.Is(err, launch.ErrImagesLocked) {
		t.Errorf("remove: err = %v, want ErrImagesLocked", err)
	}
	if err := launch.ReorderImages(e.ctx, e.deps, e.launch.ID, []uuid.UUID{a.ID}); !errors.Is(err, launch.ErrImagesLocked) {
		t.Errorf("reorder: err = %v, want ErrImagesLocked", err)
	}
	if n := len(e.images(t)); n != 1 {
		t.Errorf("%d rows after locked edits, want 1", n)
	}
}

func TestRemoveUnknownImage(t *testing.T) {
	e := setup(t)
	if err := launch.RemoveImage(e.ctx, e.deps, e.launch.ID, uuid.New()); !errors.Is(err, launch.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestReorderNeedsExactSet(t *testing.T) {
	e := setup(t)
	a, err := launch.AddImage(e.ctx, e.deps, e.launch.ID, solidPNG(t, 4, 4, color.RGBA{10, 0, 0, 255}))
	if err != nil {
		t.Fatal(err)
	}
	b, err := launch.AddImage(e.ctx, e.deps, e.launch.ID, solidPNG(t, 4, 4, color.RGBA{20, 0, 0, 255}))
	if err != nil {
		t.Fatal(err)
	}
	for name, ids := range map[string][]uuid.UUID{
		"missing one": {a.ID},
		"foreign id":  {a.ID, uuid.New()},
		"duplicate":   {a.ID, a.ID},
		"extra":       {a.ID, b.ID, uuid.New()},
	} {
		if err := launch.ReorderImages(e.ctx, e.deps, e.launch.ID, ids); !errors.Is(err, launch.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if err := launch.ReorderImages(e.ctx, e.deps, e.launch.ID, []uuid.UUID{b.ID, a.ID}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	rows := e.images(t)
	if len(rows) != 2 || rows[0].ID != b.ID || rows[1].ID != a.ID {
		t.Errorf("order after reorder = %v, want b then a", []uuid.UUID{rows[0].ID, rows[1].ID})
	}
}

// A release card over Bluesky's limit is stored with its size and a JPEG
// copy under the limit, so the hero or card still reaches Bluesky.
func TestCreateManualStoresABlueskyCopyOfABigCard(t *testing.T) {
	e := setup(t)
	big := noisyPNG(t, 900, 700)
	e.deps.Render = func(c any, p cards.Preset) ([]byte, error) { return big, nil }
	l, _, err := launch.CreateManual(e.ctx, e.deps, e.owner.ID, trigger.ManualInput{Title: "Big card"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.q.GetLatestLaunchAssetByKind(e.ctx, sqlc.GetLatestLaunchAssetByKindParams{LaunchID: l.ID, Kind: "release"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Bytes != int64(len(big)) {
		t.Errorf("bytes = %d, want %d", a.Bytes, len(big))
	}
	if !strings.HasSuffix(a.SmallUrl, ".jpg") {
		t.Fatalf("small copy = %q, want a stored JPEG", a.SmallUrl)
	}
	o, ok := e.r2.Object(strings.TrimPrefix(a.SmallUrl, "https://cdn.example/"))
	if !ok || len(o.Body) > launch.BlueskyMaxBytes {
		t.Errorf("copy object ok=%v %d bytes", ok, len(o.Body))
	}
}
