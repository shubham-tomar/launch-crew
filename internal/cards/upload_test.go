package cards_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/r2fake"
)

func TestPutImageKeyAndType(t *testing.T) {
	r2 := r2fake.New("bucket")
	t.Cleanup(r2.Close)
	u := cards.NewUploader(r2.URL, "bucket", "key", "secret", "https://cdn.example/")

	for _, c := range []struct{ contentType, ext string }{
		{"image/jpeg", "jpg"}, {"image/png", "png"}, {"image/gif", "gif"}, {"image/webp", "webp"},
	} {
		data := []byte("bytes of a " + c.contentType)
		sum := sha256.Sum256(data)
		wantSha := hex.EncodeToString(sum[:])
		url, sha, err := u.PutImage(context.Background(), data, c.contentType)
		if err != nil {
			t.Fatalf("%s: PutImage: %v", c.contentType, err)
		}
		if sha != wantSha {
			t.Errorf("%s: sha = %s, want %s", c.contentType, sha, wantSha)
		}
		key := "uploads/" + wantSha + "." + c.ext
		if want := "https://cdn.example/" + key; url != want {
			t.Errorf("%s: url = %s, want %s", c.contentType, url, want)
		}
		o, ok := r2.Object(key)
		if !ok {
			t.Fatalf("%s: no object at %s", c.contentType, key)
		}
		if o.ContentType != c.contentType {
			t.Errorf("%s: stored Content-Type = %q", c.contentType, o.ContentType)
		}
		if o.CacheControl != "public, max-age=31536000, immutable" {
			t.Errorf("Cache-Control = %q", o.CacheControl)
		}
		if string(o.Body) != string(data) {
			t.Errorf("%s: stored body differs", c.contentType)
		}
	}
	if _, _, err := u.PutImage(context.Background(), []byte("x"), "image/svg+xml"); err == nil {
		t.Error("an unsupported content type was stored")
	}
}
