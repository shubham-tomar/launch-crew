package github

import "testing"

func TestWithDraftNote(t *testing.T) {
	note := "[Review the launch](https://lc.test/x)"
	block := DraftNoteStart + "\n" + note + "\n" + DraftNoteEnd
	cases := []struct{ name, body, note, want string }{
		{"empty body", "", note, block},
		{"appends", "Notes", note, "Notes\n\n" + block},
		{"replaces", "Notes\n\n" + DraftNoteStart + "\nold\n" + DraftNoteEnd, note, "Notes\n\n" + block},
		{"above roll call", "Notes\n\n" + ReleaseSection + "\nroll", note, "Notes\n\n" + block + "\n\n" + ReleaseSection + "\nroll"},
		{"removes", "Notes\n\n" + block, "", "Notes"},
		{"no note to remove", "Notes", "", "Notes"},
	}
	for _, c := range cases {
		if got := WithDraftNote(c.body, c.note); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
