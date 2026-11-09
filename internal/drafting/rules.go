package drafting

import (
	"regexp"
	"strings"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
)

var defaultRules = map[Network]Rules{
	Twitter:   {MaxChars: 280, Threads: true, ImageOK: true, MaxImages: 4},
	Bluesky:   {MaxChars: 300, Threads: true, ImageOK: true, MaxImages: 4, MaxImageBytes: 1_000_000},
	Threads:   {MaxChars: 500, Threads: true, ImageOK: true, MaxImages: 4},
	Mastodon:  {MaxChars: 500, Threads: true, ImageOK: true, MaxImages: 4},
	LinkedIn:  {MaxChars: 3000, ImageOK: true, LinkInFirstComment: true, MaxImages: 20},
	Facebook:  {MaxChars: 3000, ImageOK: true, MaxImages: 10},
	Instagram: {MaxChars: 2200, ImageOK: true, MaxImages: 20},
}

func serviceKey(n Network) string {
	if n == "x" {
		return string(Twitter)
	}
	return strings.ToLower(string(n))
}

// RulesFor returns the rules for a network, preferring cfg over defaults.
func RulesFor(n Network, cfg *buffer.Config) Rules {
	key := serviceKey(n)
	def, ok := defaultRules[Network(key)]
	if !ok {
		def = Rules{MaxChars: 280}
	}
	if cfg == nil {
		return def
	}
	sr, ok := cfg.Services[key]
	if !ok {
		return def
	}
	r := Rules{
		MaxChars: sr.MaxChars, Threads: sr.Threads, ImageOK: sr.ImageOK, LinkInFirstComment: def.LinkInFirstComment,
		MaxImages: sr.MaxImages, MaxImageBytes: sr.MaxImageBytes,
	}
	if r.MaxChars <= 0 {
		r.MaxChars = def.MaxChars
	}
	if !r.ImageOK {
		r.MaxImages, r.MaxImageBytes = 0, 0
	} else {
		if r.MaxImages <= 0 {
			r.MaxImages = def.MaxImages
		}
		if r.MaxImageBytes <= 0 {
			r.MaxImageBytes = def.MaxImageBytes
		}
	}
	if Network(key) == LinkedIn {
		r.LinkInFirstComment = sr.FirstComment
	}
	return r
}

var urlRe = regexp.MustCompile(`https?://\S+`)

func runeLen(s string) int { return len([]rune(s)) }

// FitToRules makes a draft respect the rules. It is idempotent.
func FitToRules(d Draft, r Rules) Draft {
	if strings.TrimSpace(d.Text) == "" {
		d.Text = ""
		return d
	}
	if r.LinkInFirstComment {
		if u := findURL(d.Text); u != "" {
			d.Text = tidy(strings.Replace(d.Text, u, "", 1))
			d.FirstComment = u
		}
	}
	if r.MaxChars <= 0 || runeLen(d.Text) <= r.MaxChars {
		return d
	}
	if r.Threads {
		d.Thread = splitThread(d.Text, r.MaxChars)
		d.Text = d.Thread[0]
		return d
	}
	url := findURL(d.Text)
	d.Text = truncate(d.Text, r.MaxChars)
	d.Thread = nil
	if url != "" && !strings.Contains(d.Text, url) && d.FirstComment == "" {
		d.FirstComment = url
	}
	return d
}

var (
	spaceRunRe = regexp.MustCompile(`[ \t]+`)
	lineEdgeRe = regexp.MustCompile(`[ \t]*\n[ \t]*`)
	orphanRe   = regexp.MustCompile(`[ \t]+([.,;:!?])`)
)

// findURL returns the first URL in s without trailing punctuation.
func findURL(s string) string {
	return strings.TrimRight(urlRe.FindString(s), `.,;:!?)"'`)
}

// tidy collapses space runs left by a removal and keeps newlines.
func tidy(s string) string {
	s = spaceRunRe.ReplaceAllString(s, " ")
	s = orphanRe.ReplaceAllString(s, "$1")
	s = lineEdgeRe.ReplaceAllString(s, "\n")
	return strings.TrimSpace(s)
}

// sentences splits after . ! or ? followed by whitespace.
func sentences(s string) []string {
	var out []string
	var cur []rune
	rs := []rune(strings.TrimSpace(s))
	for i, c := range rs {
		cur = append(cur, c)
		if (c == '.' || c == '!' || c == '?') && i+1 < len(rs) && (rs[i+1] == ' ' || rs[i+1] == '\n') {
			out = append(out, strings.TrimSpace(string(cur)))
			cur = nil
		}
	}
	if t := strings.TrimSpace(string(cur)); t != "" {
		out = append(out, t)
	}
	return out
}

func splitThread(text string, max int) []string {
	var out []string
	cur := ""
	flush := func() {
		if cur != "" {
			out = append(out, cur)
			cur = ""
		}
	}
	for _, s := range sentences(text) {
		if runeLen(s) > max {
			flush()
			parts := wrap(s, max)
			out = append(out, parts[:len(parts)-1]...)
			cur = parts[len(parts)-1]
			continue
		}
		switch {
		case cur == "":
			cur = s
		case runeLen(cur)+1+runeLen(s) <= max:
			cur += " " + s
		default:
			flush()
			cur = s
		}
	}
	flush()
	return out
}

// wrap breaks s at word boundaries into pieces of at most max runes.
func wrap(s string, max int) []string {
	var out []string
	cur := ""
	for _, w := range strings.Fields(s) {
		for runeLen(w) > max {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			rw := []rune(w)
			out = append(out, string(rw[:max]))
			w = string(rw[max:])
		}
		switch {
		case cur == "":
			cur = w
		case runeLen(cur)+1+runeLen(w) <= max:
			cur += " " + w
		default:
			out = append(out, cur)
			cur = w
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func truncate(text string, max int) string {
	cur := ""
	for _, s := range sentences(text) {
		next := s
		if cur != "" {
			next = cur + " " + s
		}
		if runeLen(next) > max {
			break
		}
		cur = next
	}
	if cur != "" {
		return cur
	}
	return wrap(text, max)[0]
}
