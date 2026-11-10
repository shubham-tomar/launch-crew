package crew_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/crew"
)

type docStore struct {
	got []string
	err error
}

func (d *docStore) PutDocument(_ context.Context, urls []string) (string, error) {
	d.got = urls
	return "https://cdn.example/docs/x.pdf", d.err
}

func TestDocumentPagesPreferSmallCopies(t *testing.T) {
	card := crew.Image{URL: "card.png", Small: "card-small.jpg"}
	imgs := []crew.Image{{URL: "a.jpg"}, {URL: "b.png", Small: "b-small.jpg"}}
	if got := crew.DocumentPages(card, true, imgs); !reflect.DeepEqual(got, []string{"card-small.jpg", "a.jpg", "b-small.jpg"}) {
		t.Fatalf("pages = %v", got)
	}
	if got := crew.DocumentPages(card, false, imgs); !reflect.DeepEqual(got, []string{"a.jpg", "b-small.jpg"}) {
		t.Fatalf("pages without the card = %v", got)
	}
}

func TestApplyFormat(t *testing.T) {
	ctx := context.Background()
	base := func() buffer.CreatePostInput {
		return buffer.CreatePostInput{Text: "hi", AssetURLs: []string{"a.jpg"}, Thread: []string{"hi", "more"}, FirstComment: "link"}
	}

	in := base()
	if err := crew.ApplyFormat(ctx, nil, &in, "instagram", "story", "T", nil); err != nil || !in.Story || in.Thread != nil || in.FirstComment != "" || len(in.AssetURLs) != 1 {
		t.Fatalf("story = %+v, %v", in, err)
	}

	docs := &docStore{}
	in = base()
	if err := crew.ApplyFormat(ctx, docs, &in, "linkedin", "document", "Rocket 1.2", []string{"p1", "p2"}); err != nil {
		t.Fatal(err)
	}
	if in.Document == nil || in.Document.Title != "Rocket 1.2" || in.Document.ThumbnailURL != "p1" || in.AssetURLs != nil || !reflect.DeepEqual(docs.got, []string{"p1", "p2"}) {
		t.Fatalf("document = %+v (pages %v)", in, docs.got)
	}

	// A format the channel cannot take, or a document with nothing in it, changes nothing.
	in = base()
	if err := crew.ApplyFormat(ctx, docs, &in, "twitter", "story", "T", nil); err != nil || in.Story || !reflect.DeepEqual(in, base()) {
		t.Fatalf("twitter story = %+v", in)
	}
	in = base()
	if err := crew.ApplyFormat(ctx, docs, &in, "linkedin", "document", "T", nil); err != nil || in.Document != nil {
		t.Fatalf("empty document = %+v", in)
	}

	in = base()
	if err := crew.ApplyFormat(ctx, &docStore{err: errors.New("r2 down")}, &in, "linkedin", "document", "T", []string{"p1"}); !errors.Is(err, crew.ErrNoDocument) {
		t.Fatalf("err = %v, want ErrNoDocument", err)
	}
}
