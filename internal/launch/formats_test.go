package launch_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/shubham-tomar/launchcrew/internal/launch"
)

type fakeDocs struct{ pages []string }

func (f *fakeDocs) PutDocument(_ context.Context, urls []string) (string, error) {
	f.pages = urls
	return "https://cdn.example/docs/launch.pdf", nil
}

func (e *env) draftFor(t *testing.T, channelID string) launch.CaptainDraftView {
	t.Helper()
	v, err := launch.Detail(e.ctx, e.deps, e.launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, cd := range v.CaptainDrafts {
		if cd.ChannelID == channelID {
			return cd
		}
	}
	t.Fatalf("no captain draft for %s in %+v", channelID, v.CaptainDrafts)
	return launch.CaptainDraftView{}
}

// The captain makes the LinkedIn post swipeable: it goes out as one PDF of
// the card and the images, titled with the launch, while X is unchanged.
func TestLaunchJobLinkedInDocument(t *testing.T) {
	e := setup(t)
	docs := &fakeDocs{}
	e.deps.Docs = docs
	imgs := e.addImages(t, 2)

	li := e.draftFor(t, "ch-li")
	if !reflect.DeepEqual(li.Formats, []string{"document"}) || li.Format != "" {
		t.Fatalf("LinkedIn draft offers %v (now %q), want document", li.Formats, li.Format)
	}
	doc := "document"
	if err := launch.PatchCaptainDraft(e.ctx, e.deps, e.launch.ID, li.ID, launch.DraftPatch{Format: &doc}); err != nil {
		t.Fatal(err)
	}
	if got := e.draftFor(t, "ch-li").Format; got != "document" {
		t.Fatalf("format after patch = %q", got)
	}
	story := "story"
	if err := launch.PatchCaptainDraft(e.ctx, e.deps, e.launch.ID, e.draftFor(t, "ch-x").ID, launch.DraftPatch{Format: &story}); err == nil {
		t.Fatal("X accepted a story")
	}

	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}
	const card = "https://cdn.example/cards/release.png"
	if want := append([]string{card}, imgs...); !reflect.DeepEqual(docs.pages, want) {
		t.Fatalf("document pages = %v, want %v", docs.pages, want)
	}
	for _, in := range e.createInputs() {
		assets, _ := in["assets"].([]any)
		switch in["channelId"] {
		case "ch-li":
			if len(assets) != 1 {
				t.Fatalf("LinkedIn assets = %v, want one document", assets)
			}
			d, _ := assets[0].(map[string]any)["document"].(map[string]any)
			if d["url"] != "https://cdn.example/docs/launch.pdf" || d["title"] != e.launch.Title || d["thumbnailUrl"] == "" {
				t.Fatalf("LinkedIn document = %v", assets[0])
			}
		case "ch-x":
			if got := postAssets(in); len(got) != 3 {
				t.Fatalf("X assets = %v, want the card and both images", got)
			}
		}
	}
}
