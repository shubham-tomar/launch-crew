package cards

import (
	"bytes"
	"context"
	"flag"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"

	"github.com/shubham-tomar/launchcrew/internal/testfakes/r2fake"
)

var update = flag.Bool("update", false, "rewrite golden PNGs")

func decodePNG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	return img
}

func TestEveryPresetMeetsSevenToOne(t *testing.T) {
	for _, p := range Presets {
		if got := Contrast(p.Type, p.Field); got < 7.0 {
			t.Errorf("%s: contrast %.2f < 7", p.Name, got)
		}
	}
	if got := Contrast("000000", "#FFFFFF"); math.Abs(got-21) > 0.01 {
		t.Errorf("black on white = %.2f, want 21", got)
	}
}

func hue(hex string) float64 {
	r, g, b := parseHex(hex)
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	d := mx - mn
	if d == 0 {
		return 0
	}
	var h float64
	switch mx {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h
}

func TestNoPresetIsPurple(t *testing.T) {
	for _, p := range Presets {
		if h := hue(p.Field); h >= 255 && h <= 300 {
			t.Errorf("%s hue %.0f is purple", p.Name, h)
		}
	}
}

func TestPresetForIsTheInkPass(t *testing.T) {
	// Every launch gets the ink boarding pass; the other presets only exist so
	// stored indexes of earlier launches still resolve.
	for i := range 50 {
		got := PresetFor(uuid.NewSHA1(uuid.NameSpaceOID, []byte{byte(i)}))
		if got.Name != "ink" || got != Presets[inkPreset] {
			t.Fatalf("PresetFor = %v, want the ink preset", got)
		}
	}
	if got := Presets[inkPreset]; got.Field != "#15181F" || got.Eyebrow != "#ADA9FF" {
		t.Errorf("ink preset = %+v", got)
	}
}

func TestInkPassQuietTextIsReadable(t *testing.T) {
	p := Presets[inkPreset]
	for name, fg := range map[string]string{"eyebrow": p.eyebrow(), "muted": p.muted()} {
		if got := Contrast(fg, p.Field); got < 7.0 {
			t.Errorf("%s %s on %s = %.2f, want >= 7", name, fg, p.Field, got)
		}
	}
	// Derived tints on every other preset stay legible too.
	for _, q := range Presets {
		if got := Contrast(q.muted(), q.Field); got < 4.5 {
			t.Errorf("%s muted text = %.2f, want >= 4.5", q.Name, got)
		}
	}
}

func TestUploadTrimsPublicBaseSlash(t *testing.T) {
	srv := r2fake.New("b")
	defer srv.Close()
	u := NewUploader(srv.URL, "b", "ak", "sk", "https://cdn.example/")
	url, sha, err := u.Put(context.Background(), []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://cdn.example/cards/" + sha + ".png"; url != want {
		t.Errorf("url = %s, want %s", url, want)
	}
	u.PublicBase = "https://cdn.example///"
	if url, _, _ = u.Put(context.Background(), []byte("x")); strings.Contains(strings.TrimPrefix(url, "https://"), "//") {
		t.Errorf("double slash in %s", url)
	}
}

func TestAvatarOverSizeRefused(t *testing.T) {
	big := image.NewGray(image.Rect(0, 0, 4097, 2))
	var buf bytes.Buffer
	_ = png.Encode(&buf, big)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(buf.Bytes()) }))
	defer srv.Close()
	old := HTTPClient
	HTTPClient = srv.Client()
	defer func() { HTTPClient = old }()
	if _, err := fetchAvatar(srv.URL); err == nil {
		t.Error("want error for 4097px-wide avatar")
	}
}

var goldenPreset = Presets[inkPreset]

func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wantB, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	g, w := decodePNG(t, got), decodePNG(t, wantB)
	if g.Bounds() != w.Bounds() {
		t.Fatalf("bounds %v != golden %v", g.Bounds(), w.Bounds())
	}
	diff, total := 0, g.Bounds().Dx()*g.Bounds().Dy()
	for y := g.Bounds().Min.Y; y < g.Bounds().Max.Y; y++ {
		for x := g.Bounds().Min.X; x < g.Bounds().Max.X; x++ {
			if color.NRGBAModel.Convert(g.At(x, y)) != color.NRGBAModel.Convert(w.At(x, y)) {
				diff++
			}
		}
	}
	if float64(diff)/float64(total) > 0.01 {
		t.Errorf("%s: %d of %d pixels differ (>1%%)", name, diff, total)
	}
}

func TestRenderGoldenSize(t *testing.T) {
	cases := map[string]any{
		"contributor.png": ContributorCard{Name: "Ada Lovelace", Project: "analytical-engine", Version: "v2.4.0", PRTitle: "Add Bernoulli number routine"},
		"release.png":     ReleaseCard{Project: "analytical-engine", Version: "v2.4.0", Headline: "Faster loops, fewer punch cards"},
	}
	for name, c := range cases {
		b, err := Render(c, goldenPreset)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if bd := decodePNG(t, b).Bounds(); bd.Dx() != 1200 || bd.Dy() != 630 {
			t.Errorf("%s size = %v", name, bd)
		}
		compareGolden(t, name, b)
	}
	if _, err := Render(42, goldenPreset); err == nil {
		t.Error("want error for unsupported card type")
	}
}

func TestMultiPRContributorCard(t *testing.T) {
	long := strings.Repeat("Rework the retry queue so nothing blocks ", 6)
	for _, title := range []string{"Add Bernoulli number routine", long} {
		b, err := Render(ContributorCard{Name: "Ada Lovelace", Project: "analytical-engine", Version: "v2.4.0", PRTitle: title, PRCount: 3}, goldenPreset)
		if err != nil {
			t.Fatal(err)
		}
		if bd := decodePNG(t, b).Bounds(); bd.Dx() != 1200 || bd.Dy() != 630 {
			t.Errorf("size = %v", bd)
		}
		if title != long {
			compareGolden(t, "contributor_multi.png", b)
		}
	}
	single, _ := Render(ContributorCard{Name: "Ada Lovelace", Project: "analytical-engine", Version: "v2.4.0", PRTitle: "Add Bernoulli number routine"}, goldenPreset)
	multi, _ := Render(ContributorCard{Name: "Ada Lovelace", Project: "analytical-engine", Version: "v2.4.0", PRTitle: "Add Bernoulli number routine", PRCount: 3}, goldenPreset)
	if bytes.Equal(single, multi) {
		t.Error("multi-PR card renders like a single-PR card")
	}
}

func TestLongAndCJKNamesWrap(t *testing.T) {
	fs, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("abcdefghi ", 6)[:60]
	for _, s := range []string{long, strings.Repeat("W", 60)} {
		f := fitFace(fs.display, s, 644, 56, 44)
		if got := ellipsize(f, s, 644); textWidth(f, got) > 644 || got == "" {
			t.Errorf("%q: does not fit: %q", s[:10], got)
		}
	}
	if got := keepGlyphs(fs.display, "山田太郎"); got != "" {
		t.Errorf("CJK kept: %q", got)
	}
	if got := titleText(fs.display, "山田太郎", "Contributor"); got != "Contributor" {
		t.Errorf("CJK-only title = %q, want Contributor", got)
	}
	if got := titleText(fs.display, "Ada 山田", "Contributor"); got != "Ada" {
		t.Errorf("mixed title = %q, want Ada", got)
	}
	if got := keepGlyphs(fs.display, "Sam 🚀 Lee"); got != "Sam Lee" {
		t.Errorf("emoji kept: %q", got)
	}
	for _, name := range []string{long, "山田太郎", ""} {
		b, err := Render(ContributorCard{Name: name, Project: "p", Version: "v1", PRTitle: "t"}, goldenPreset)
		if err != nil {
			t.Fatalf("render %q: %v", name, err)
		}
		decodePNG(t, b)
	}
}

func solid(c color.Color) []byte {
	m := image.NewRGBA(image.Rect(0, 0, 200, 120))
	draw.Draw(m, m.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	var buf bytes.Buffer
	_ = png.Encode(&buf, m)
	return buf.Bytes()
}

// A point inside the avatar circle, clear of the initials.
const avatarCx, avatarCy = padL + avatarSize/2, padT + 66 - 40

func TestMissingAvatarUsesPlaceholder(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok.png" {
			w.Write(solid(red))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	old := HTTPClient
	HTTPClient = srv.Client()
	defer func() { HTTPClient = old }()

	centre := func(url string) color.Color {
		b, err := Render(ContributorCard{Name: "Ada", AvatarURL: url, Project: "p", Version: "v1"}, goldenPreset)
		if err != nil {
			t.Fatal(err)
		}
		return decodePNG(t, b).At(avatarCx, avatarCy)
	}
	pl := paletteOf(goldenPreset, "")
	want := hexColor(blend(pl.accent, pl.Field, 0.22)) // the accent's quiet tint
	near := func(a, b color.Color) bool {
		ar, ag, ab, _ := a.RGBA()
		br, bg, bb, _ := b.RGBA()
		d := func(x, y uint32) bool { return math.Abs(float64(x)-float64(y)) < 3*257 }
		return d(ar, br) && d(ag, bg) && d(ab, bb)
	}
	for _, u := range []string{srv.URL + "/missing.png", "", "http://127.0.0.1:1/x.png"} {
		if got := centre(u); !near(got, want) {
			t.Errorf("%q: centre %v, want placeholder %v", u, got, want)
		}
	}
	if got := centre(srv.URL + "/ok.png"); !near(got, red) {
		t.Errorf("fetched avatar centre %v, want red", got)
	}
}

func TestHeroSitsBehindTheCardAndTextStaysReadable(t *testing.T) {
	white := image.NewRGBA(image.Rect(0, 0, 400, 300))
	draw.Draw(white, white.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	b, err := Render(ContributorCard{Name: "Ada", Project: "p", Version: "v1", Hero: white}, goldenPreset)
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, b)
	if bd := img.Bounds(); bd.Dx() != 1200 || bd.Dy() != 630 {
		t.Fatalf("size %v", bd)
	}
	// A pure white hero under the wash must come out dark: text on it is white.
	r, g, bl, _ := img.At(600, 560).RGBA()
	if lum := (float64(r) + float64(g) + float64(bl)) / 3 / 257; lum > 90 {
		t.Errorf("hero background luminance %.0f, want a dark wash over the hero", lum)
	}
	// And the hero changes the card: no hero gives the flat ink field there.
	plain, _ := Render(ContributorCard{Name: "Ada", Project: "p", Version: "v1"}, goldenPreset)
	if bytes.Equal(b, plain) {
		t.Error("hero had no effect")
	}
}

func TestTicketIsTransparentOutsideItsShape(t *testing.T) {
	b, err := Render(ContributorCard{Name: "Ada", Project: "p", Version: "v1"}, goldenPreset)
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, b)
	alpha := func(x, y int) uint8 { return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA).A }
	for _, p := range [][2]int{{0, 0}, {cardW - 1, 0}, {0, cardH - 1}, {cardW - 1, cardH - 1}, {mainW, 1}, {mainW, cardH - 2}, {mainW + 10, 4}} {
		if a := alpha(p[0], p[1]); a != 0 {
			t.Errorf("(%d,%d) alpha %d, want transparent", p[0], p[1], a)
		}
	}
	for _, p := range [][2]int{{cardW / 2, cardH / 2}, {cornerR, cornerR}, {mainW + 60, cardH / 2}, {mainW - 40, 4}} {
		if a := alpha(p[0], p[1]); a != 255 {
			t.Errorf("(%d,%d) alpha %d, want opaque", p[0], p[1], a)
		}
	}
}

func TestAccentTooDarkFallsBack(t *testing.T) {
	if got := paletteOf(goldenPreset, "#101010").accent; got != goldenPreset.Eyebrow {
		t.Errorf("dark accent kept: %s", got)
	}
	if got := paletteOf(goldenPreset, "#58d3ae").accent; got != "#58D3AE" {
		t.Errorf("readable accent = %s", got)
	}
	if got := paletteOf(goldenPreset, "red").accent; got != goldenPreset.Eyebrow {
		t.Errorf("invalid accent kept: %s", got)
	}
	if Contrast(paletteOf(goldenPreset, "#58D3AE").accent, goldenPreset.Field) < 4.5 {
		t.Error("accent under 4.5:1")
	}
}

func TestGitHubMarkPathParses(t *testing.T) {
	toks := pathTokens("A8.013 8.013 0 0016 8")
	var vs []float64
	for _, tk := range toks[1:] {
		vs = append(vs, tk.v)
	}
	want := []float64{8.013, 8.013, 0, 0, 0, 16, 8}
	if len(vs) != len(want) {
		t.Fatalf("tokens %v", vs)
	}
	for i := range want {
		if vs[i] != want[i] {
			t.Fatalf("tokens %v, want %v", vs, want)
		}
	}
	if got := initials("launch-crew"); got != "LC" {
		t.Errorf("initials = %q", got)
	}
	if got := initials("Ada"); got != "AD" {
		t.Errorf("initials = %q", got)
	}
}

func TestBrandLogoFillsTheStubTile(t *testing.T) {
	red := image.NewRGBA(image.Rect(0, 0, 64, 64))
	draw.Draw(red, red.Bounds(), image.NewUniform(color.RGBA{255, 0, 0, 255}), image.Point{}, draw.Src)
	b, err := Render(ContributorCard{Name: "Ada", Project: "p", Version: "v1", Brand: Brand{Logo: red}}, goldenPreset)
	if err != nil {
		t.Fatal(err)
	}
	c := color.NRGBAModel.Convert(decodePNG(t, b).At(stubX+52, padT+52)).(color.NRGBA)
	if c.R < 240 || c.G > 20 {
		t.Errorf("tile centre %v, want the logo", c)
	}
}

func TestQRSlideDecodes(t *testing.T) {
	const url = "https://lc.example/l/aB3xYz9"
	b, err := RenderQRSlide("Shubham shipped v2.4.0", url)
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, b)
	if bd := img.Bounds(); bd.Dx() != 1920 || bd.Dy() != 1080 {
		t.Fatalf("size %v", bd)
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatal(err)
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, map[gozxing.DecodeHintType]any{gozxing.DecodeHintType_TRY_HARDER: true})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.GetText() != url {
		t.Errorf("decoded %q, want %q", res.GetText(), url)
	}
}

func TestUploadIsFetchable(t *testing.T) {
	srv := r2fake.New("lc-cards")
	defer srv.Close()
	u := NewUploader(srv.URL, "lc-cards", "ak", "sk", srv.URL+"/lc-cards")
	b, err := Render(ReleaseCard{Project: "p", Version: "v1", Headline: "h"}, goldenPreset)
	if err != nil {
		t.Fatal(err)
	}
	url, sha, err := u.Put(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if want := srv.URL + "/lc-cards/cards/" + sha + ".png"; url != want {
		t.Errorf("url = %s, want %s", url, want)
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Errorf("GET = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", cc)
	}
	obj, ok := srv.Object("cards/" + sha + ".png")
	if !ok || !bytes.Equal(obj.Body, b) {
		t.Error("stored body differs")
	}
}

// decodeSlide renders a slide and returns what a QR reader makes of it.
func decodeSlide(t *testing.T, logo SlideLogo, url string) (image.Image, string) {
	t.Helper()
	b, err := RenderQRSlideLogo("Shubham shipped v2.4.0", url, logo)
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, b)
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatal(err)
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, map[gozxing.DecodeHintType]any{gozxing.DecodeHintType_TRY_HARDER: true})
	if err != nil {
		t.Fatalf("decode with logo %+v: %v", logo, err)
	}
	return img, res.GetText()
}

func TestQRSlideCentreMark(t *testing.T) {
	const url = "https://lc.example/l/aB3xYz9"
	cx, cy := slideW/2, slideH/2
	isAccent := func(img image.Image, x, y int) bool {
		r, g, b, _ := img.At(x, y).RGBA()
		want := hexColor(accent)
		return abs(int(r>>8)-int(want.R)) < 12 && abs(int(g>>8)-int(want.G)) < 12 && abs(int(b>>8)-int(want.B)) < 12
	}
	// Default is the plane: accent pixels near the centre, white tile around them, and the code still scans.
	img, got := decodeSlide(t, SlideLogo{}, url)
	if got != url {
		t.Fatalf("plane: decoded %q", got)
	}
	var accentPx int
	for y := cy - 30; y <= cy+30; y++ {
		for x := cx - 30; x <= cx+30; x++ {
			if isAccent(img, x, y) {
				accentPx++
			}
		}
	}
	if accentPx < 200 {
		t.Errorf("plane drawn with only %d accent pixels", accentPx)
	}
	if r, g, b, _ := img.At(cx-qrMark/2+6, cy-qrMark/2+6).RGBA(); r>>8 < 0xF0 || g>>8 < 0xF0 || b>>8 < 0xF0 {
		t.Errorf("tile corner not white: %v", img.At(cx-qrMark/2+6, cy-qrMark/2+6))
	}

	// None: no tile and no accent at the centre, and the code still scans.
	img, got = decodeSlide(t, SlideLogo{Mode: LogoNone}, url)
	if got != url {
		t.Fatalf("none: decoded %q", got)
	}
	for y := cy - 30; y <= cy+30; y++ {
		for x := cx - 30; x <= cx+30; x++ {
			if isAccent(img, x, y) {
				t.Fatalf("none: accent pixel at %d,%d", x, y)
			}
		}
	}
}

func TestQRSlideCustomLogoAndFallback(t *testing.T) {
	const url = "https://lc.example/l/aB3xYz9"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/logo.png" {
			http.NotFound(w, r)
			return
		}
		img := image.NewRGBA(image.Rect(0, 0, 64, 64))
		for y := 0; y < 64; y++ {
			for x := 0; x < 64; x++ {
				img.Set(x, y, color.RGBA{200, 30, 30, 255}) // red square
			}
		}
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, img)
	}))
	defer srv.Close()
	cx, cy := slideW/2, slideH/2

	img, got := decodeSlide(t, SlideLogo{Mode: LogoCustom, URL: srv.URL + "/logo.png"}, url)
	if got != url {
		t.Fatalf("custom: decoded %q", got)
	}
	if r, g, b, _ := img.At(cx, cy).RGBA(); r>>8 < 180 || g>>8 > 80 || b>>8 > 80 {
		t.Errorf("custom logo not drawn at the centre: %v", img.At(cx, cy))
	}

	// An address that does not answer falls back to the plane, never to an error.
	img, got = decodeSlide(t, SlideLogo{Mode: LogoCustom, URL: srv.URL + "/missing.png"}, url)
	if got != url {
		t.Fatalf("fallback: decoded %q", got)
	}
	var plane int
	for y := cy - 30; y <= cy+30; y++ {
		for x := cx - 30; x <= cx+30; x++ {
			if r, g, b, _ := img.At(x, y).RGBA(); abs(int(r>>8)-0x4A) < 12 && abs(int(g>>8)-0x45) < 12 && abs(int(b>>8)-0xD1) < 12 {
				plane++
			}
		}
	}
	if plane < 200 {
		t.Errorf("fallback did not draw the plane (%d accent pixels)", plane)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func TestSoloCard(t *testing.T) {
	date := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	cases := map[string]SoloCard{
		// PRs, a previous release 42 days back, the 12th release.
		"solo.png": {
			Name: "Shubham Tomar", Handle: "shubham-tomar", Project: "launch-crew", Version: "v2.4.0",
			Commits: 41, Added: 1204, Deleted: 380, Date: date, PrevTag: "v2.3.0", DaysSince: 42, Ordinal: 12,
			Release: []PRLine{{42, "Split-pane resize with keyboard"}, {41, "Credit every PR a person has"}, {39, "Remember workspace layout"}},
		},
		// No PRs: commit subjects, no number column; the first release.
		"solo-commits.png": {
			Name: "Shubham Tomar", Handle: "shubham-tomar", Project: "launch-crew", Version: "v0.1.0",
			Commits: 9, Date: date, DaysSince: -1, Ordinal: 1, FirstRelease: true,
			Release: []PRLine{{0, "Draft posts in each person's voice"}, {0, "Release cards"}, {0, "Buffer sign-in"}}, ReleaseMore: 6,
		},
	}
	for name, c := range cases {
		b, err := Render(c, goldenPreset)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		compareGolden(t, name, b)
	}
	if got := soloMid(SoloCard{Ordinal: 20}); got.value != "20th" || got.label != "Milestone" {
		t.Errorf("20th release: %+v", got)
	}
	if got := soloMid(SoloCard{Ordinal: 7}); got.value != "7" || got.label != "Releases" {
		t.Errorf("7th release: %+v", got)
	}
	if got := soloMid(SoloCard{Major: true, Ordinal: 7}); got.value != "Major" {
		t.Errorf("major: %+v", got)
	}
	for n, want := range map[int]string{1: "1st", 2: "2nd", 3: "3rd", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 112: "112th"} {
		if ordinal(n) != want {
			t.Errorf("ordinal(%d) = %s", n, ordinal(n))
		}
	}
	if soloFoot(SoloCard{PrevTag: "v1", DaysSince: 1}) != "1 day since v1" {
		t.Error("one day")
	}
}
