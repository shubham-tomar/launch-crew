package launch_test

import (
	"image/color"
	"reflect"
	"strings"
	"testing"

	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/launch"
)

func postAssets(in map[string]any) []string {
	var out []string
	assets, _ := in["assets"].([]any)
	for _, a := range assets {
		img, _ := a.(map[string]any)["image"].(map[string]any)
		out = append(out, img["url"].(string))
	}
	return out
}

func (e *env) addImages(t *testing.T, n int) []string {
	t.Helper()
	var urls []string
	for i := 0; i < n; i++ {
		img, err := launch.AddImage(e.ctx, e.deps, e.launch.ID, solidPNG(t, 6, 6, color.RGBA{uint8(i * 20), 0, 0, 255}))
		if err != nil {
			t.Fatalf("image %d: %v", i+1, err)
		}
		urls = append(urls, img.URL)
	}
	return urls
}

func (e *env) captainPostsByChannel(t *testing.T) map[string][]string {
	t.Helper()
	got := map[string][]string{}
	for _, in := range e.createInputs() {
		got[in["channelId"].(string)] = postAssets(in)
	}
	return got
}

// Captain posts carry the card slot then the images, cut to each network:
// X takes 4, LinkedIn takes them all.
func TestLaunchJobAttachesImages(t *testing.T) {
	e := setup(t)
	imgs := e.addImages(t, 4)
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}
	got := e.captainPostsByChannel(t)
	const card = "https://cdn.example/cards/release.png"
	if want := append([]string{card}, imgs[:3]...); !reflect.DeepEqual(got["ch-x"], want) {
		t.Errorf("X assets = %v, want %v", got["ch-x"], want)
	}
	if want := append([]string{card}, imgs...); !reflect.DeepEqual(got["ch-li"], want) {
		t.Errorf("LinkedIn assets = %v, want %v", got["ch-li"], want)
	}
}

func TestLaunchJobCardSwitchOff(t *testing.T) {
	e := setup(t)
	imgs := e.addImages(t, 2)
	if err := e.q.SetLaunchIncludeCard(e.ctx, sqlc.SetLaunchIncludeCardParams{ID: e.launch.ID, IncludeCard: false}); err != nil {
		t.Fatal(err)
	}
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}
	got := e.captainPostsByChannel(t)
	for _, ch := range []string{"ch-x", "ch-li"} {
		if !reflect.DeepEqual(got[ch], imgs) {
			t.Errorf("%s assets = %v, want just the images %v", ch, got[ch], imgs)
		}
	}
}

func TestLaunchJobNoCardNoImagesSendsNone(t *testing.T) {
	e := setup(t)
	if err := e.q.SetLaunchIncludeCard(e.ctx, sqlc.SetLaunchIncludeCardParams{ID: e.launch.ID, IncludeCard: false}); err != nil {
		t.Fatal(err)
	}
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}
	ins := e.createInputs()
	if len(ins) != 2 {
		t.Fatalf("%d captain posts, want 2", len(ins))
	}
	for _, in := range ins {
		if _, has := in["assets"]; has {
			t.Errorf("%s sent assets %v with the card off and no images", in["channelId"], in["assets"])
		}
	}
}

// The captain's Instagram channel only gets a draft when a post would carry
// something; and a draft made earlier turns preflight red once nothing does.
func TestInstagramDraftNeedsSomethingToAttach(t *testing.T) {
	e := setup(t)
	e.buf.Handle("GetChannels", map[string]any{"channels": []any{
		map[string]any{"id": "ch-x", "name": "Acme X", "service": "twitter"},
		map[string]any{"id": "ch-ig", "name": "Acme Instagram", "service": "instagram"},
	}})
	if err := e.q.SetLaunchIncludeCard(e.ctx, sqlc.SetLaunchIncludeCardParams{ID: e.launch.ID, IncludeCard: false}); err != nil {
		t.Fatal(err)
	}
	v, err := launch.Detail(e.ctx, e.deps, e.launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, cd := range v.CaptainDrafts {
		if cd.ChannelID == "ch-ig" {
			t.Fatalf("an Instagram draft was made with nothing to attach: %+v", cd)
		}
	}
	for _, c := range v.Checks {
		if !c.OK {
			t.Errorf("check %q red with no Instagram draft: %s", c.Name, c.Fix)
		}
	}

	// Card back on: the Instagram draft appears and preflight is green.
	if err := e.q.SetLaunchIncludeCard(e.ctx, sqlc.SetLaunchIncludeCardParams{ID: e.launch.ID, IncludeCard: true}); err != nil {
		t.Fatal(err)
	}
	if v, err = launch.Detail(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	var ig bool
	for _, cd := range v.CaptainDrafts {
		ig = ig || cd.ChannelID == "ch-ig"
	}
	if !ig {
		t.Fatal("no Instagram draft with the card on")
	}

	// Card off again with the draft in place: check 3 says why Launch must wait.
	if err := e.q.SetLaunchIncludeCard(e.ctx, sqlc.SetLaunchIncludeCardParams{ID: e.launch.ID, IncludeCard: false}); err != nil {
		t.Fatal(err)
	}
	if v, err = launch.Detail(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	var rules launch.CheckView
	for _, c := range v.Checks {
		if c.ID == 3 {
			rules = c
		}
	}
	if rules.OK || !strings.Contains(rules.Fix, "Instagram") {
		t.Errorf("network rules check = %+v, want red naming Instagram", rules)
	}
}

// A release card over Bluesky's limit reaches Bluesky as its small copy.
func TestLaunchJobBigCardGoesSmallToBluesky(t *testing.T) {
	e := setup(t)
	e.buf.Handle("GetChannels", map[string]any{"channels": []any{
		map[string]any{"id": "ch-x", "name": "Acme X", "service": "twitter"},
		map[string]any{"id": "ch-bs", "name": "Acme Bluesky", "service": "bluesky"},
	}})
	// The release card the drafts already point at grows past the limit.
	if _, err := e.pool.Exec(e.ctx, `UPDATE launch_assets SET r2_url = $2, bytes = 2000000, small_url = $3 WHERE launch_id = $1 AND kind = 'release'`,
		e.launch.ID, "https://cdn.example/cards/big.png", "https://cdn.example/uploads/big-small.jpg"); err != nil {
		t.Fatal(err)
	}
	imgs := e.addImages(t, 1)
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}
	got := e.captainPostsByChannel(t)
	if want := append([]string{"https://cdn.example/uploads/big-small.jpg"}, imgs...); !reflect.DeepEqual(got["ch-bs"], want) {
		t.Errorf("Bluesky assets = %v, want %v", got["ch-bs"], want)
	}
	if want := append([]string{"https://cdn.example/cards/big.png"}, imgs...); !reflect.DeepEqual(got["ch-x"], want) {
		t.Errorf("X assets = %v, want %v", got["ch-x"], want)
	}
}
