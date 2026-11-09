package drafting

import (
	"testing"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
)

func TestRulesForMaxImages(t *testing.T) {
	for n, want := range map[Network]int{Twitter: 4, Bluesky: 4, Mastodon: 4, Threads: 4, LinkedIn: 20, Facebook: 10, Instagram: 20} {
		if got := RulesFor(n, nil).MaxImages; got != want {
			t.Errorf("%s: MaxImages = %d, want %d", n, got, want)
		}
	}
	// Buffer's configuration knows nothing of image counts; the built-in
	// number fills in, and a network without images gets 0.
	cfg := &buffer.Config{Services: map[string]buffer.ServiceRules{
		"twitter": {MaxChars: 280, ImageOK: true},
		"youtube": {MaxChars: 5000},
	}}
	if got := RulesFor(Twitter, cfg).MaxImages; got != 4 {
		t.Errorf("twitter from config: MaxImages = %d, want 4", got)
	}
	if got := RulesFor("youtube", cfg).MaxImages; got != 0 {
		t.Errorf("youtube: MaxImages = %d, want 0", got)
	}
}

func TestRulesForCapsAndByteLimits(t *testing.T) {
	// Buffer's media guide and the user's check: X, Bluesky, Mastodon and
	// Threads take 4; LinkedIn 20. Only Bluesky has a per-image size Buffer
	// cannot shrink to.
	for n, want := range map[Network]int{Threads: 4, LinkedIn: 20} {
		if got := RulesFor(n, nil).MaxImages; got != want {
			t.Errorf("%s: MaxImages = %d, want %d", n, got, want)
		}
	}
	if got := RulesFor(Bluesky, nil).MaxImageBytes; got != 1_000_000 {
		t.Errorf("bluesky: MaxImageBytes = %d, want 1000000", got)
	}
	if got := RulesFor(Twitter, nil).MaxImageBytes; got != 0 {
		t.Errorf("twitter: MaxImageBytes = %d, want 0 (Buffer resizes)", got)
	}
	cfg := &buffer.Config{Services: map[string]buffer.ServiceRules{"bluesky": {MaxChars: 300, ImageOK: true}}}
	if got := RulesFor(Bluesky, cfg).MaxImageBytes; got != 1_000_000 {
		t.Errorf("bluesky from config: MaxImageBytes = %d, want the built-in 1000000", got)
	}
}
