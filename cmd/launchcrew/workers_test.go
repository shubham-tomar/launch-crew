package main

import (
	"testing"

	"github.com/shubham-tomar/launchcrew/internal/crew"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/launch"
)

// Every kind some code enqueues needs a handler, or its jobs sit forever.
// Keep this list in step with the jobs.Enqueue callers.
func TestWorkerHandlesEveryEnqueuedKind(t *testing.T) {
	h := workerHandlers(crew.Deps{}, launch.Deps{}, nil, nil)
	for _, k := range []jobs.Kind{
		jobs.ProcessRelease, jobs.VoicedDrafts, jobs.SweepOpened, jobs.LaunchJob,
		jobs.SendComments, jobs.CloseWindow, jobs.PollStatus, jobs.ReencryptTokens, jobs.NotifyCaptain,
	} {
		if h[k] == nil {
			t.Errorf("no handler for %s", k)
		}
	}
}
