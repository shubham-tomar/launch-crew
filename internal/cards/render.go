package cards

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fogleman/gg"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
)

const (
	cardW, cardH = 1200, 630
	stubW        = 300
	mainW        = cardW - stubW // 900: the perforation and notches sit here
	padL, padR   = 64, 56
	padT, padB   = 52, 44
	textW        = mainW - padL - padR // 780
	stubX        = mainW + 44          // stub content left edge
	stubTextW    = cardW - 36 - stubX  // 220
	avatarSize   = 112
	maxAvatar    = 5 << 20
	maxAvatarDim = 4096
	notchR       = 24
	cornerR      = 36
	maxPRLines   = 5
)

// HTTPClient fetches avatars and owner logos. Tests replace it.
var HTTPClient = &http.Client{Timeout: 3 * time.Second}

// Diff colours: added and removed lines, as GitHub-flavoured as the ink field allows.
const (
	addedHex   = "#58D3AE"
	deletedHex = "#FF8A74"
	// The diff colours for a light card field, where the bright pair washes out.
	addedDarkHex   = "#0A6B4E"
	deletedDarkHex = "#B3261E"
)

// PRLine is one change listed on a card.
type PRLine struct {
	Number int
	Title  string
}

// Brand is the repo's look on the stub. Logo (a captain-set image) wins, then
// LogoURL (the GitHub owner's avatar), then initials on an accent tile.
// Accent is a hex colour; one too dark to read on the field falls back to the
// preset's mark colour. Background and Text replace the preset's field and
// type colours (see Apply).
type Brand struct {
	Owner, LogoURL, Accent string
	Background, Text       string
	Logo                   image.Image
}

// Apply returns p with the brand's background and text colours. A background
// without a text colour gets white or ink, whichever reads better; a text
// colour under 4.5:1 on the field is ignored. When either is set the preset's
// tinted eyebrow and muted colours are dropped, so they derive from the new
// type over the new field.
func (b Brand) Apply(p Preset) Preset {
	bg, text := strings.ToUpper(b.Background), strings.ToUpper(b.Text)
	if !ValidAccent(bg) && !ValidAccent(text) {
		return p
	}
	if ValidAccent(bg) {
		p.Field = bg
		p.Type = AutoText(bg)
	}
	if ValidAccent(text) && Contrast(text, p.Field) >= 4.5 {
		p.Type = text
	}
	p.Mark, p.Eyebrow, p.Muted = p.Type, "", ""
	return p
}

// readable is c when it reads on field at 4.5:1, else fallback.
func readable(c, fallback, field string) string {
	if Contrast(c, field) >= 4.5 {
		return c
	}
	return fallback
}

// AutoText is white or ink, whichever has more contrast on bg.
func AutoText(bg string) string {
	if Contrast(white, bg) >= Contrast(darkType, bg) {
		return white
	}
	return darkType
}

// ContributorCard is the card for one contributor in a release: who they are
// up top, the release's headline changes in the middle, repo and version at
// the foot. Everything but Name or Handle is optional. PRTitle and PRNumber
// are the contributor's own first PR, listed when Release is empty.
type ContributorCard struct {
	Name, AvatarURL, Project, Version, PRTitle string
	Handle, Role                               string
	PRNumber, PRCount, CrewCount               int
	Added, Deleted                             int
	Release                                    []PRLine
	ReleaseMore                                int
	Date                                       time.Time
	Hero                                       image.Image
	Brand                                      Brand
}

// ReleaseCard is the card for a release. Everything but Project is optional.
type ReleaseCard struct {
	Project, Version, Headline string
	Release                    []PRLine
	ReleaseMore                int
	CrewCount                  int
	Date                       time.Time
	Hero                       image.Image
	Brand                      Brand
}

// SoloCard is the release card when one person shipped it all: the person
// leads, with their commit count and line diff, the release's changes (PRs,
// or commit subjects when there were none), and how long since the last one.
type SoloCard struct {
	Name, Handle, AvatarURL string
	Project, Version        string
	Commits                 int
	Added, Deleted          int
	Release                 []PRLine
	ReleaseMore             int
	Date                    time.Time
	// PrevTag and DaysSince read "42 days since v2.3"; DaysSince < 0 hides it.
	PrevTag   string
	DaysSince int
	// Ordinal is the release's count among the repo's releases; 0 if unknown.
	Ordinal int
	// FirstRelease and Major mark milestones on the stub.
	FirstRelease, Major bool
	Hero                image.Image
	Brand               Brand
}

func hexColor(h string) color.RGBA {
	r, g, b := parseHex(h)
	return color.RGBA{uint8(r*255 + 0.5), uint8(g*255 + 0.5), uint8(b*255 + 0.5), 255}
}

func setHex(dc *gg.Context, h string) { dc.SetColor(hexColor(h)) }

// palette is a preset resolved against the repo accent.
type palette struct {
	Preset
	accent, line, dim, body string
}

func paletteOf(p Preset, accent string) palette {
	a := p.Mark
	if p.Eyebrow != "" {
		a = p.Eyebrow
	}
	if ValidAccent(accent) && Contrast(accent, p.Field) >= 4.5 {
		a = strings.ToUpper(accent)
	}
	return palette{Preset: p, accent: a,
		line: blend(p.Type, p.Field, 0.16),
		dim:  blend(p.Type, p.Field, 0.5),
		body: blend(p.Type, p.Field, 0.85),
	}
}

// ValidAccent reports a #RRGGBB colour.
func ValidAccent(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// Render draws a 1200x630 PNG plane ticket for a ContributorCard or
// ReleaseCard: rounded corners, half-circle notches at the perforation, and
// fully transparent outside the ticket.
func Render(c any, p Preset) ([]byte, error) {
	fs, err := loadFonts()
	if err != nil {
		return nil, err
	}
	var (
		brand        Brand
		project, ver string
		mid          stubMid
		hero         image.Image
		drawMain     func(*gg.Context, palette) error
	)
	switch v := c.(type) {
	case ContributorCard:
		brand, project, ver, mid, hero = v.Brand, v.Project, v.Version, crewMid(v.CrewCount, v.Version), v.Hero
		drawMain = func(dc *gg.Context, pl palette) error { return drawContributor(dc, fs, pl, v) }
	case ReleaseCard:
		brand, project, ver, mid, hero = v.Brand, v.Project, v.Version, crewMid(v.CrewCount, v.Version), v.Hero
		drawMain = func(dc *gg.Context, pl palette) error { return drawRelease(dc, fs, pl, v) }
	case SoloCard:
		brand, project, ver, mid, hero = v.Brand, v.Project, v.Version, soloMid(v), v.Hero
		drawMain = func(dc *gg.Context, pl palette) error { return drawSolo(dc, fs, pl, v) }
	default:
		return nil, fmt.Errorf("cards: unsupported card type %T", c)
	}
	p = brand.Apply(p)
	pl := paletteOf(p, brand.Accent)

	dc := gg.NewContext(cardW, cardH)
	setHex(dc, p.Field)
	dc.Clear()
	if hero != nil {
		drawHero(dc, hero, p)
	}
	// Perforation: a dashed line just inside the main half's right edge.
	setHex(dc, blend(p.Type, p.Field, 0.2))
	dc.SetLineWidth(4)
	dc.SetDash(12, 10)
	dc.DrawLine(mainW-2, 40, mainW-2, cardH-40)
	dc.Stroke()
	dc.SetDash()

	if err := drawMain(dc, pl); err != nil {
		return nil, err
	}
	if err := drawStub(dc, fs, pl, brand, project, ver, mid); err != nil {
		return nil, err
	}
	return encode(ticket(dc.Image()))
}

// drawContributor is board H: the person, the release's top changes, then
// repo and version over the release date.
func drawContributor(dc *gg.Context, fs fontSet, pl palette, v ContributorCard) error {
	count := v.PRCount
	if count == 0 && (v.PRNumber > 0 || strings.TrimSpace(v.PRTitle) != "") {
		count = 1
	}
	foot := ""
	if !v.Date.IsZero() {
		foot = "Released " + v.Date.Format("02 Jan 2006")
	}
	return drawPerson(dc, fs, pl, v, personOpts{count: plural(count, "PR", "PRs"), showCount: count > 0, foot: foot})
}

// personOpts are what differs between a contributor's card and a solo one:
// the count in the meta line ("3 PRs", "41 commits") and the foot's right side.
type personOpts struct {
	count     string
	showCount bool
	foot      string
}

func drawPerson(dc *gg.Context, fs fontSet, pl palette, v ContributorCard, o personOpts) error {
	handle := strings.TrimPrefix(strings.TrimSpace(v.Handle), "@")
	name := keepGlyphs(fs.display, v.Name)
	if name == "" {
		name = keepGlyphs(fs.display, handle)
	}
	if name == "" {
		name = "Contributor"
	}
	role := strings.TrimSpace(v.Role)
	if role == "" {
		role = "Contributor"
	}

	// Avatar with an accent ring, vertically centred on the text block.
	const top = padT
	cx, cy := float64(padL+avatarSize/2), float64(top+66)
	setHex(dc, pl.accent)
	dc.DrawCircle(cx, cy, avatarSize/2+4)
	dc.Fill()
	drawAvatar(dc, v.AvatarURL, pl, initials(name), cx, cy, avatarSize/2)

	tx := float64(padL + avatarSize + 28)
	tw := float64(mainW-padR) - tx
	// Eyebrow: the GitHub mark, then the role in caps.
	ef := must(face(fs.label, 22))
	drawGitHubMark(dc, tx, top+2, 24, pl.accent)
	dc.SetFontFace(ef)
	setHex(dc, pl.accent)
	dc.DrawString(ellipsize(ef, strings.ToUpper(keepGlyphs(fs.label, role)), tw-36), tx+36, top+22)
	// Name: 56px, shrinking to 44 before it ellipsizes.
	nf := fitFace(fs.display, name, tw, 56, 44)
	dc.SetFontFace(nf)
	setHex(dc, pl.Type)
	dc.DrawString(ellipsize(nf, name, tw), tx, top+86)
	// Meta: @handle | N PRs +a −d
	mf := must(face(fs.body, 24))
	monof := must(face(fs.mono, 24))
	x, y := tx, float64(top+124)
	put := func(f font.Face, hex, s string) {
		if s == "" || x >= tx+tw {
			return
		}
		dc.SetFontFace(f)
		setHex(dc, hex)
		s = ellipsize(f, s, tx+tw-x)
		dc.DrawString(s, x, y)
		x += textWidth(f, s) + 14
	}
	if handle != "" {
		put(mf, pl.muted(), "@"+keepGlyphs(fs.body, handle))
	}
	if o.showCount || v.Added+v.Deleted > 0 {
		if handle != "" {
			put(mf, pl.line, "|")
		}
	}
	if o.showCount {
		put(mf, pl.muted(), o.count)
	}
	if v.Added+v.Deleted > 0 {
		put(monof, readable(addedHex, addedDarkHex, pl.Field), "+"+strconv.Itoa(v.Added))
		put(monof, readable(deletedHex, deletedDarkHex, pl.Field), minus(fs.mono)+strconv.Itoa(v.Deleted))
	}

	// Middle: what shipped in the release.
	lines, more := v.Release, v.ReleaseMore
	label := "In this release"
	if len(lines) == 0 && strings.TrimSpace(v.PRTitle) != "" {
		lines, label = []PRLine{{Number: v.PRNumber, Title: v.PRTitle}}, "Shipped"
		if v.PRCount > 1 {
			more = v.PRCount - 1
		}
	}
	const footY = cardH - padB - 66 // the divider
	drawPRList(dc, fs, pl, label, lines, more, float64(top+124), footY)

	// Foot: repo and version, the release date on the right.
	dc.SetLineWidth(3)
	setHex(dc, pl.line)
	dc.DrawLine(padL, footY, mainW-padR, footY)
	dc.Stroke()
	right := 0.0
	if s := keepGlyphs(fs.body, o.foot); s != "" {
		df := must(face(fs.body, 22))
		right = textWidth(df, s) + 20
		dc.SetFontFace(df)
		setHex(dc, pl.dim)
		dc.DrawStringAnchored(s, mainW-padR, cardH-padB-6, 1, 0)
	}
	ff := must(face(fs.display, 40))
	dc.SetFontFace(ff)
	setHex(dc, pl.Type)
	dc.DrawString(ellipsize(ff, keepGlyphs(fs.display, joinSpace(v.Project, v.Version)), textW-right), padL, cardH-padB-6)
	return nil
}

// drawRelease is board G without a person: eyebrow, repo and version large,
// the top changes, and the owner/repo over the date.
func drawRelease(dc *gg.Context, fs fontSet, pl palette, v ReleaseCard) error {
	ef := must(face(fs.label, 22))
	setHex(dc, pl.accent)
	dc.DrawCircle(padL+6, padT+14, 6)
	dc.Fill()
	dc.SetFontFace(ef)
	eyebrow := "Release"
	if !v.Date.IsZero() {
		eyebrow = joinDot(eyebrow, v.Date.Format("02 Jan 2006"))
	}
	dc.DrawString(strings.ToUpper(eyebrow), padL+24, padT+22)

	title := titleText(fs.display, joinSpace(v.Project, v.Version), "Release")
	tf := fitFace(fs.display, title, textW, 68, 52)
	dc.SetFontFace(tf)
	setHex(dc, pl.Type)
	tl := wrap(tf, title, textW, 2)
	y := float64(padT + 22 + 18)
	for _, l := range tl {
		y += float64(tf.Metrics().Ascent.Round())
		dc.DrawString(l, padL, y)
		y += 4
	}

	const footY = cardH - padB - 52
	if len(v.Release) > 0 {
		drawPRList(dc, fs, pl, "", v.Release, v.ReleaseMore, y, footY)
	} else if h := keepGlyphs(fs.body, v.Headline); h != "" {
		bf := must(face(fs.body, 30))
		dc.SetFontFace(bf)
		setHex(dc, pl.body)
		for _, l := range wrap(bf, h, textW, 2) {
			y += 44
			dc.DrawString(l, padL, y)
		}
	}
	dc.SetLineWidth(3)
	setHex(dc, pl.line)
	dc.DrawLine(padL, footY, mainW-padR, footY)
	dc.Stroke()
	foot := ""
	if owner, project := strings.TrimSpace(v.Brand.Owner), strings.TrimSpace(v.Project); owner != "" && project != "" {
		foot = "github.com/" + owner + "/" + project
	} else if v.CrewCount > 0 {
		foot = plural(v.CrewCount, "person", "people") + " shipped this release"
	}
	if foot = keepGlyphs(fs.body, foot); foot != "" {
		bf := must(face(fs.body, 24))
		dc.SetFontFace(bf)
		setHex(dc, pl.muted())
		dc.DrawString(ellipsize(bf, foot, textW), padL, cardH-padB-6)
	}
	return nil
}

// drawPRList draws an optional caps label, up to five "#N title" rows and
// "+ n more in the release notes", centred between top and bottom.
func drawPRList(dc *gg.Context, fs fontSet, pl palette, label string, lines []PRLine, more int, top, bottom float64) {
	if len(lines) > maxPRLines {
		more += len(lines) - maxPRLines
		lines = lines[:maxPRLines]
	}
	if len(lines) == 0 {
		return
	}
	const rowH = 39.0
	h := float64(len(lines)) * rowH
	if label != "" {
		h += 30
	}
	if more > 0 {
		h += rowH
	}
	y := top + (bottom-top-h)/2
	if label != "" {
		lf := must(face(fs.label, 20))
		dc.SetFontFace(lf)
		setHex(dc, pl.dim)
		dc.DrawString(strings.ToUpper(label), padL, y+20)
		y += 30
	}
	nf := must(face(fs.mono, 24))
	bf := must(face(fs.body, 24))
	// Commit subjects have no numbers: no number column then.
	numW, moreText := 0.0, " more changes"
	for _, l := range lines {
		if l.Number > 0 {
			numW, moreText = 78.0, " more in the release notes"
			break
		}
	}
	for _, l := range lines {
		base := y + 28
		if l.Number > 0 {
			dc.SetFontFace(nf)
			setHex(dc, pl.dim)
			dc.DrawString("#"+strconv.Itoa(l.Number), padL, base)
		}
		dc.SetFontFace(bf)
		setHex(dc, pl.body)
		dc.DrawString(ellipsize(bf, keepGlyphs(fs.body, l.Title), textW-numW), padL+numW, base)
		y += rowH
	}
	if more > 0 {
		mf := must(face(fs.body, 22))
		dc.SetFontFace(mf)
		setHex(dc, pl.dim)
		dc.DrawString("+ "+strconv.Itoa(more)+moreText, padL+numW, y+28)
	}
}

// drawStub is the tear-off: the repo's logo, name and owner, the collaborator
// count, and the Launch Crew mark.
func drawStub(dc *gg.Context, fs fontSet, pl palette, b Brand, project, version string, mid stubMid) error {
	const tile = 104
	x, y := float64(stubX), float64(padT)
	drawLogoTile(dc, fs, pl, b, project, x, y, tile)

	y += tile + 14
	name := keepGlyphs(fs.display, project)
	if name == "" {
		name = "Launch"
	}
	nf := must(face(fs.display, 30))
	dc.SetFontFace(nf)
	setHex(dc, pl.Type)
	for _, l := range wrap(nf, name, stubTextW, 2) {
		y += 30
		dc.DrawString(l, x, y)
		y += 4
	}
	if owner := keepGlyphs(fs.body, b.Owner); owner != "" {
		of := must(face(fs.body, 22))
		dc.SetFontFace(of)
		setHex(dc, pl.dim)
		for _, l := range wrap(of, owner+" · on GitHub", stubTextW, 2) {
			y += 30
			dc.DrawString(l, x, y)
		}
	}

	// Middle: collaborators, a milestone, or the version.
	lf := must(face(fs.label, 22))
	label, value, tail := mid.label, keepGlyphs(fs.display, mid.value), mid.tail
	if value != "" {
		dc.SetFontFace(lf)
		setHex(dc, pl.dim)
		dc.DrawString(strings.ToUpper(label), x, 396)
		vf := fitFace(fs.display, value, stubTextW, 44, 28)
		dc.SetFontFace(vf)
		setHex(dc, pl.Type)
		dc.DrawString(value, x, 450)
		if tail != "" {
			tf := must(face(fs.label, 22))
			if vx := x + textWidth(vf, value) + 10; vx+textWidth(tf, tail) <= x+stubTextW {
				dc.SetFontFace(tf)
				setHex(dc, pl.dim)
				dc.DrawString(strings.TrimSpace(tail), vx, 450)
			}
		}
	}

	// Foot: the Launch Crew mark.
	my := float64(cardH - padB - 30)
	setHex(dc, pl.accent)
	dc.DrawRoundedRectangle(x, my, 30, 30, 9)
	dc.Fill()
	drawPlane(dc, x+15, my+15, 18, 45, ink)
	mf := must(face(fs.display, 20))
	dc.SetFontFace(mf)
	setHex(dc, pl.muted())
	dc.DrawString("Launch Crew", x+40, my+22)
	return nil
}

// stubMid is the stub's middle: a caps label over a large value and a small tail.
type stubMid struct{ label, value, tail string }

// crewMid counts the collaborators, else shows the version.
func crewMid(crew int, version string) stubMid {
	if crew <= 0 {
		return stubMid{"Version", version, ""}
	}
	return stubMid{"Collaborators", strconv.Itoa(crew), " shipped it"}
}

// soloMid names a milestone when there is one, else counts the releases.
func soloMid(v SoloCard) stubMid {
	switch {
	case v.FirstRelease:
		return stubMid{"Milestone", "First", " release"}
	case v.Major:
		return stubMid{"Milestone", "Major", " release"}
	case v.Ordinal > 0 && v.Ordinal%10 == 0:
		return stubMid{"Milestone", ordinal(v.Ordinal), " release"}
	case v.Ordinal > 1:
		return stubMid{"Releases", strconv.Itoa(v.Ordinal), " shipped"}
	}
	return stubMid{"Version", v.Version, ""}
}

// ordinal is 1st, 2nd, 3rd, 4th ... 11th, 12th, 13th ... 21st.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

// drawSolo is board H for one person: "Shipped solo" over the avatar and
// name, commits and lines, the release's changes, then repo and version over
// the time since the last release.
func drawSolo(dc *gg.Context, fs fontSet, pl palette, v SoloCard) error {
	pr := 0
	for _, l := range v.Release {
		if l.Number > 0 {
			pr++
		}
	}
	c := ContributorCard{
		Name: v.Name, Handle: v.Handle, AvatarURL: v.AvatarURL, Role: "Shipped solo",
		Project: v.Project, Version: v.Version, Release: v.Release, ReleaseMore: v.ReleaseMore,
		Added: v.Added, Deleted: v.Deleted, Date: v.Date,
	}
	return drawPerson(dc, fs, pl, c, personOpts{
		count: plural(v.Commits, "commit", "commits"), showCount: v.Commits > 0,
		foot: soloFoot(v),
	})
}

// soloFoot is "42 days since v2.3", "1 day since v2.3", or the release date.
func soloFoot(v SoloCard) string {
	if v.PrevTag != "" && v.DaysSince >= 0 {
		if v.DaysSince == 0 {
			return "Same day as " + v.PrevTag
		}
		return plural(v.DaysSince, "day", "days") + " since " + v.PrevTag
	}
	if !v.Date.IsZero() {
		return "Released " + v.Date.Format("02 Jan 2006")
	}
	return ""
}

// drawLogoTile is the stub's square: the captain's logo or the owner's avatar
// on white, else the project's initials on the accent.
func drawLogoTile(dc *gg.Context, fs fontSet, pl palette, b Brand, project string, x, y, size float64) {
	img := b.Logo
	if img == nil && b.LogoURL != "" {
		img, _ = fetchAvatar(b.LogoURL)
	}
	dc.DrawRoundedRectangle(x, y, size, size, 24)
	if img == nil {
		setHex(dc, pl.accent)
		dc.Fill()
		f := must(face(fs.display, 40))
		dc.SetFontFace(f)
		setHex(dc, ink)
		dc.DrawStringAnchored(initials(keepGlyphs(fs.display, project)), x+size/2, y+size/2, 0.5, 0.35)
		return
	}
	setHex(dc, white)
	dc.Fill()
	dc.DrawRoundedRectangle(x, y, size, size, 24)
	dc.Clip()
	dc.DrawImage(coverSquare(img, int(size)), int(x), int(y))
	dc.ResetClip()
}

// initials is up to two letters: the first of the first two words, else the
// first two of one word ("launch-crew" gives LC, "Ada" gives AD).
func initials(s string) string {
	words := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '-' || r == '_' || r == '.' || r == '/' })
	var out []rune
	for _, w := range words {
		if rs := []rune(w); len(rs) > 0 {
			out = append(out, rs[0])
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 1 && len(words) == 1 {
		if rs := []rune(words[0]); len(rs) > 1 {
			out = append(out, rs[1])
		}
	}
	return strings.ToUpper(string(out))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func joinSpace(parts ...string) string {
	var out []string
	for _, s := range parts {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, " ")
}

func joinDot(parts ...string) string {
	var out []string
	for _, s := range parts {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, " · ")
}

// minus is the true minus sign when the font has it, else a hyphen.
func minus(f *sfnt.Font) string {
	if keepGlyphs(f, "−") != "" {
		return "−"
	}
	return "-"
}

// fitFace is the largest face from big down to small (in 4px steps) that fits
// s on one line; small when none does.
func fitFace(f *sfnt.Font, s string, maxW, big, small float64) font.Face {
	for px := big; px > small; px -= 4 {
		if ff := must(face(f, px)); textWidth(ff, s) <= maxW {
			return ff
		}
	}
	return must(face(f, small))
}

// drawHero lays a cover-cropped hero image behind the card, then a heavy wash
// of the field colour so every line of text stays readable.
func drawHero(dc *gg.Context, img image.Image, p Preset) {
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return
	}
	// Largest centred crop with the card's aspect ratio.
	cw, ch := b.Dx(), b.Dx()*cardH/cardW
	if ch > b.Dy() {
		cw, ch = b.Dy()*cardW/cardH, b.Dy()
	}
	x0, y0 := b.Min.X+(b.Dx()-cw)/2, b.Min.Y+(b.Dy()-ch)/2
	dst := image.NewRGBA(image.Rect(0, 0, cardW, cardH))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, image.Rect(x0, y0, x0+cw, y0+ch), xdraw.Over, nil)
	dc.DrawImage(dst, 0, 0)
	f := hexColor(p.Field)
	dc.SetColor(color.NRGBA{f.R, f.G, f.B, 224})
	dc.DrawRectangle(0, 0, cardW, cardH)
	dc.Fill()
}

// titleText strips unrenderable runes, using fallback when nothing is left.
func titleText(f *sfnt.Font, s, fallback string) string {
	if s = keepGlyphs(f, s); s != "" {
		return s
	}
	return fallback
}

func encode(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// drawAvatar draws the GitHub avatar in a circle, else the initials on a
// quiet tint of the accent.
func drawAvatar(dc *gg.Context, url string, pl palette, letters string, cx, cy, r float64) {
	img, err := fetchAvatar(url)
	if err != nil {
		setHex(dc, blend(pl.accent, pl.Field, 0.22))
		dc.DrawCircle(cx, cy, r)
		dc.Fill()
		if fs, err := loadFonts(); err == nil && letters != "" {
			dc.SetFontFace(must(face(fs.display, r*0.72)))
			setHex(dc, pl.accent)
			dc.DrawStringAnchored(letters, cx, cy, 0.5, 0.35)
		}
		return
	}
	dc.DrawCircle(cx, cy, r)
	dc.Clip()
	dc.DrawImage(coverSquare(img, int(2*r)), int(cx-r), int(cy-r))
	dc.ResetClip()
}

// coverSquare centre-crops img to a square and scales it to side x side.
func coverSquare(img image.Image, side int) image.Image {
	b := img.Bounds()
	s := min(b.Dx(), b.Dy())
	sq := image.Rect(b.Min.X+(b.Dx()-s)/2, b.Min.Y+(b.Dy()-s)/2, 0, 0)
	sq.Max = image.Pt(sq.Min.X+s, sq.Min.Y+s)
	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, sq, xdraw.Over, nil)
	return dst
}

func fetchAvatar(url string) (image.Image, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, errors.New("no avatar url")
	}
	resp, err := HTTPClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("avatar status " + strconv.Itoa(resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAvatar))
	if err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width > maxAvatarDim || cfg.Height > maxAvatarDim {
		return nil, errors.New("avatar too large")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// wrap breaks s into at most n lines, splitting over-long words by rune and
// ellipsizing the last line when text remains.
func wrap(f font.Face, s string, maxW float64, n int) []string {
	var lines []string
	cur := ""
	flush := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
	}
	for _, w := range strings.Fields(s) {
		for textWidth(f, w) > maxW { // word wider than a line: split by rune
			flush()
			rs := []rune(w)
			i := len(rs)
			for i > 1 && textWidth(f, string(rs[:i])) > maxW {
				i--
			}
			lines = append(lines, string(rs[:i]))
			w = string(rs[i:])
		}
		if cur == "" {
			cur = w
		} else if textWidth(f, cur+" "+w) <= maxW {
			cur += " " + w
		} else {
			flush()
			cur = w
		}
	}
	flush()
	if len(lines) > n {
		last := strings.Join(lines[n-1:], " ")
		lines = append(lines[:n-1], ellipsize(f, last, maxW))
	}
	return lines
}

func ellipsize(f font.Face, s string, maxW float64) string {
	if textWidth(f, s) <= maxW {
		return s
	}
	rs := []rune(s)
	for len(rs) > 0 {
		rs = rs[:len(rs)-1]
		t := strings.TrimRight(string(rs), " ") + "…"
		if textWidth(f, t) <= maxW {
			return t
		}
	}
	return ""
}

func textWidth(f font.Face, s string) float64 {
	return float64(font.MeasureString(f, s)) / 64
}
