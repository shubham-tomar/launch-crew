package cards_test

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"image/png"
	"regexp"
	"strings"
	"testing"

	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/r2fake"
)

func solid(t *testing.T, w, h int, enc string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 200, 80, 40, 255
	}
	var buf bytes.Buffer
	var err error
	if enc == "png" {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Each image is a page the shape of the image, in order, PNGs included.
func TestBuildPDFOnePagePerImage(t *testing.T) {
	out, err := cards.BuildPDF(context.Background(), [][]byte{
		solid(t, 1200, 630, "png"), solid(t, 600, 800, "jpeg"), solid(t, 4000, 1000, "jpeg"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Fatalf("not a PDF: %q", out[:8])
	}
	boxes := regexp.MustCompile(`/MediaBox \[0 0 ([0-9.]+) ([0-9.]+)\]`).FindAllSubmatch(out, -1)
	// The last box is the document's default, written after the pages.
	boxes = boxes[:len(boxes)-1]
	want := [][2]string{{"1200.00", "630.00"}, {"600.00", "800.00"}, {"2000.00", "500.00"}}
	if len(boxes) != len(want) {
		t.Fatalf("pages = %d, want %d", len(boxes), len(want))
	}
	for i, b := range boxes {
		if string(b[1]) != want[i][0] || string(b[2]) != want[i][1] {
			t.Errorf("page %d is %sx%s, want %sx%s", i+1, b[1], b[2], want[i][0], want[i][1])
		}
	}
}

func TestBuildPDFRefusesNothingAndNonImages(t *testing.T) {
	if _, err := cards.BuildPDF(context.Background(), nil); err == nil {
		t.Fatal("an empty document was built")
	}
	if _, err := cards.BuildPDF(context.Background(), [][]byte{[]byte("<svg/>")}); err == nil {
		t.Fatal("a non-image page was accepted")
	}
}

// A document is built from our own stored images, in order, and stored once
// as a PDF; a URL outside the bucket is refused.
func TestPutDocumentStoresAPDFOfOurImages(t *testing.T) {
	r2 := r2fake.New("bucket")
	t.Cleanup(r2.Close)
	u := cards.NewUploader(r2.URL, "bucket", "k", "s", "https://cdn.example")
	ctx := context.Background()
	a, _, err := u.PutImage(ctx, solid(t, 800, 600, "jpeg"), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := u.PutImage(ctx, solid(t, 300, 400, "png"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	url, err := u.PutDocument(ctx, []string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	key, ok := strings.CutPrefix(url, "https://cdn.example/")
	if !ok || !strings.HasPrefix(key, "docs/") || !strings.HasSuffix(key, ".pdf") {
		t.Fatalf("url = %q", url)
	}
	obj, ok := r2.Object(key)
	if !ok || obj.ContentType != "application/pdf" || !bytes.HasPrefix(obj.Body, []byte("%PDF-")) {
		t.Fatalf("stored %q: ok=%v type=%q", key, ok, obj.ContentType)
	}
	again, err := u.PutDocument(ctx, []string{a, b})
	if err != nil || again != url {
		t.Fatalf("second build = %q, %v; want the same %q", again, err, url)
	}

	if _, err := u.PutDocument(ctx, []string{"https://elsewhere.example/x.jpg"}); err == nil {
		t.Fatal("a URL outside the bucket was read")
	}
}
