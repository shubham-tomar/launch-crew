package launch

import (
	"encoding/json"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
)

// postFormat is the captain's chosen format for a channel on this launch:
// "story", "document", or "" for a plain post. A stored value the channel's
// service cannot take reads as a plain post.
func postFormat(l sqlc.Launch, channelID, service string) string {
	var m map[string]string
	if len(l.PostFormats) == 0 || json.Unmarshal(l.PostFormats, &m) != nil {
		return ""
	}
	if f := m[channelID]; buffer.FormatAllowed(service, f) {
		return f
	}
	return ""
}

// formatsFor lists the formats beyond a plain post a channel on service can
// choose, for the launch page; empty when there is no choice to offer.
func formatsFor(service string) []string {
	out := []string{}
	for _, f := range []string{buffer.FormatStory, buffer.FormatDocument} {
		if buffer.FormatAllowed(service, f) {
			out = append(out, f)
		}
	}
	return out
}
