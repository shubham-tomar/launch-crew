// Package jobs is the Postgres-backed job queue and its worker.
package jobs

// Kind names a job type; a Handler is registered per Kind.
type Kind string

const (
	ProcessRelease  Kind = "process_release"
	RenderCards     Kind = "render_cards"
	LaunchJob       Kind = "launch"
	CloseWindow     Kind = "close_window"
	PollStatus      Kind = "poll_status"
	SweepOpened     Kind = "sweep_opened"
	ReencryptTokens Kind = "reencrypt_tokens"
	VoicedDrafts    Kind = "voiced_drafts"
	SendComments    Kind = "send_comments"
	NotifyCaptain   Kind = "notify_captain"
)
