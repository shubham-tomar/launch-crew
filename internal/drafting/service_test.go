package drafting

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type stub struct {
	fn    func(ctx context.Context, n int, req DraftRequest) (Draft, error)
	calls int
	reqs  []DraftRequest
}

func (s *stub) Draft(ctx context.Context, req DraftRequest) (Draft, error) {
	s.calls++
	s.reqs = append(s.reqs, req)
	return s.fn(ctx, s.calls, req)
}

func testReq() DraftRequest {
	return DraftRequest{
		Subject:    Subject{Project: "tuios", Version: "v1.4", Headline: "window tiling rewrite"},
		Hook:       HookPersonal,
		Network:    Bluesky,
		Rules:      Rules{MaxChars: 300},
		AllowModel: true,
	}
}

func TestServiceAllowModelFalseNeverCallsModel(t *testing.T) {
	m := &stub{fn: func(context.Context, int, DraftRequest) (Draft, error) { return Draft{Text: "x"}, nil }}
	req := testReq()
	req.AllowModel = false
	res, err := (&Service{Model: m}).DraftFor(context.Background(), req, nil)
	if err != nil || m.calls != 0 || !res.UsedTemplate || res.Draft.Text == "" {
		t.Fatalf("err=%v calls=%d res=%+v", err, m.calls, res)
	}
}

func TestServiceTimeoutReturnsTemplate(t *testing.T) {
	m := &stub{fn: func(ctx context.Context, _ int, _ DraftRequest) (Draft, error) {
		select {
		case <-ctx.Done():
			return Draft{}, ctx.Err()
		case <-time.After(9 * time.Second):
			return Draft{Text: "late"}, nil
		}
	}}
	start := time.Now()
	res, err := (&Service{Model: m}).DraftFor(context.Background(), testReq(), nil)
	if el := time.Since(start); el >= 8500*time.Millisecond {
		t.Fatalf("took %v", el)
	}
	if err != nil || !res.UsedTemplate || res.Draft.Text == "" {
		t.Fatalf("err=%v res=%+v", err, res)
	}
}

func TestServiceModelErrorReturnsTemplate(t *testing.T) {
	m := &stub{fn: func(context.Context, int, DraftRequest) (Draft, error) { return Draft{}, errors.New("boom") }}
	res, err := (&Service{Model: m}).DraftFor(context.Background(), testReq(), nil)
	if err != nil || !res.UsedTemplate || res.Draft.Text == "" || m.calls != 1 {
		t.Fatalf("err=%v calls=%d res=%+v", err, m.calls, res)
	}
}

func TestServiceRefusalReturnsTemplate(t *testing.T) {
	m := &stub{fn: func(context.Context, int, DraftRequest) (Draft, error) { return Draft{}, ErrRefused }}
	res, err := (&Service{Model: m}).DraftFor(context.Background(), testReq(), nil)
	if err != nil || !res.UsedTemplate || res.Draft.Text == "" {
		t.Fatalf("err=%v res=%+v", err, res)
	}
}

func TestServiceRegeneratesOnLintTell(t *testing.T) {
	m := &stub{fn: func(_ context.Context, n int, _ DraftRequest) (Draft, error) {
		if n == 1 {
			return Draft{Text: "tiling got faster — a lot", FromModel: true}, nil
		}
		return Draft{Text: "tiling got faster, finally", FromModel: true}, nil
	}}
	res, err := (&Service{Model: m}).DraftFor(context.Background(), testReq(), nil)
	if err != nil || m.calls != 2 || res.Draft.Text != "tiling got faster, finally" || res.UsedTemplate {
		t.Fatalf("err=%v calls=%d res=%+v", err, m.calls, res)
	}
	if res.Draft.Hook != HookPersonal || m.reqs[1].Hook != HookPersonal {
		t.Fatalf("hook changed: %s", m.reqs[1].Hook)
	}
	if got := strings.Join(m.reqs[1].Avoid, "|"); !strings.Contains(got, "do not: em dash") {
		t.Fatalf("avoid=%q", got)
	}
}

func TestServiceKeepsDraftWhenLintPersists(t *testing.T) {
	m := &stub{fn: func(context.Context, int, DraftRequest) (Draft, error) {
		return Draft{Text: "tiling — faster", FromModel: true}, nil
	}}
	res, err := (&Service{Model: m}).DraftFor(context.Background(), testReq(), nil)
	if err != nil || m.calls != 2 || res.UsedTemplate || res.Draft.Text == "" {
		t.Fatalf("err=%v calls=%d res=%+v", err, m.calls, res)
	}
}

func TestServiceRegenerateTwiceThenFlag(t *testing.T) {
	const text = "Tiling got a full rewrite and it feels great to use now"
	m := &stub{fn: func(context.Context, int, DraftRequest) (Draft, error) {
		return Draft{Text: text, FromModel: true}, nil
	}}
	res, err := (&Service{Model: m}).DraftFor(context.Background(), testReq(), []string{text})
	if err != nil || m.calls != 3 || !res.Flagged || res.UsedTemplate || res.Draft.Text == "" {
		t.Fatalf("err=%v calls=%d res=%+v", err, m.calls, res)
	}
	if m.reqs[1].Hook == m.reqs[0].Hook || m.reqs[2].Hook == m.reqs[1].Hook || m.reqs[2].Hook == m.reqs[0].Hook {
		t.Fatalf("hooks not rotated: %v %v %v", m.reqs[0].Hook, m.reqs[1].Hook, m.reqs[2].Hook)
	}
	if res.Draft.Hook != m.reqs[2].Hook {
		t.Fatalf("hook %s, want %s", res.Draft.Hook, m.reqs[2].Hook)
	}
	if res.Overlap < 0.9 {
		t.Fatalf("overlap %v", res.Overlap)
	}
}

func TestServiceRegenerationClearsOverlap(t *testing.T) {
	m := &stub{fn: func(_ context.Context, n int, _ DraftRequest) (Draft, error) {
		if n == 1 {
			return Draft{Text: "Tiling got a full rewrite and it feels great", FromModel: true}, nil
		}
		return Draft{Text: "Quarterly gardening notes: tomatoes are ripe", FromModel: true}, nil
	}}
	res, _ := (&Service{Model: m}).DraftFor(context.Background(), testReq(), []string{"Tiling got a full rewrite and it feels great"})
	if m.calls != 2 || res.Flagged || res.Overlap >= 0.35 {
		t.Fatalf("calls=%d res=%+v", m.calls, res)
	}
}

func TestServiceTimeoutShortBudget(t *testing.T) {
	m := &stub{fn: func(ctx context.Context, _ int, _ DraftRequest) (Draft, error) {
		<-ctx.Done()
		return Draft{}, ctx.Err()
	}}
	start := time.Now()
	res, err := (&Service{Model: m, Budget: 50 * time.Millisecond}).DraftFor(context.Background(), testReq(), nil)
	if el := time.Since(start); el >= 2*time.Second {
		t.Fatalf("budget ignored: %v", el)
	}
	if err != nil || !res.UsedTemplate || m.calls != 1 || res.Draft.Hook != HookPersonal {
		t.Fatalf("err=%v calls=%d res=%+v", err, m.calls, res)
	}
}

func TestServiceLintThenShortensOnTwitter(t *testing.T) {
	first := "Tiling got a full rewrite — windows snap into place much faster now. " +
		"Workspaces remember their layout between sessions. Mouse drag resizing finally respects gaps. " +
		"Configuration reloads without restarting anything. Plugins can register custom keybindings freely. " +
		"Documentation covers every flag with examples."
	second := strings.ReplaceAll(first, " —", ",") + " Zebras quietly graze beyond foggy valleys."
	if len([]rune(second)) <= 280 {
		t.Fatal("fixture too short")
	}
	third := "Tiling got a full rewrite in tuios v1.4: windows snap into place much faster now."
	m := &stub{fn: func(_ context.Context, n int, _ DraftRequest) (Draft, error) {
		switch n {
		case 1:
			return Draft{Text: first, FromModel: true}, nil // an em dash tell
		case 2:
			return Draft{Text: second, FromModel: true}, nil // clean, but over 280
		}
		return Draft{Text: third, FromModel: true}, nil
	}}
	req := testReq()
	req.Network = Twitter
	req.Rules = Rules{MaxChars: 280, Threads: true}
	svc := &Service{Model: m}
	res, err := svc.DraftFor(context.Background(), req, []string{"Quarterly gardening notes: tomatoes are ripe"})
	if err != nil || m.calls != 3 || !res.Draft.FromModel || res.UsedTemplate {
		t.Fatalf("err=%v calls=%d res=%+v", err, m.calls, res)
	}
	// An X post is one post under the limit, never a thread the person did not ask for.
	if len(res.Draft.Thread) != 0 || res.Draft.Text != third {
		t.Fatalf("thread=%q text=%q", res.Draft.Thread, res.Draft.Text)
	}
	if res.Overlap >= 0.1 {
		t.Fatalf("overlap %v", res.Overlap)
	}
}

func TestServiceRegeneratesAnOverLongDraftOnce(t *testing.T) {
	long := strings.Repeat("Shipped a thing today. ", 30) // ~690 characters
	m := &stub{fn: func(_ context.Context, n int, _ DraftRequest) (Draft, error) {
		if n == 1 {
			return Draft{Text: long}, nil
		}
		return Draft{Text: "tuios v1.4 is out with the window tiling rewrite."}, nil
	}}
	req := testReq()
	req.Network, req.Rules = Twitter, Rules{MaxChars: 280, Threads: true, ImageOK: true}
	res, err := (&Service{Model: m}).DraftFor(context.Background(), req, nil)
	if err != nil || m.calls != 2 || res.UsedTemplate {
		t.Fatalf("err=%v calls=%d res=%+v", err, m.calls, res)
	}
	if len(res.Draft.Thread) > 0 || len([]rune(res.Draft.Text)) > 280 {
		t.Fatalf("draft = %+v, want one post under 280", res.Draft)
	}
	hint := strings.Join(m.reqs[1].Avoid, "|")
	if !strings.Contains(hint, "do not: run past 280 characters") {
		t.Fatalf("second request did not say how far over it was: %q", hint)
	}
}

func TestServiceOverLongTwiceFallsBackToTemplate(t *testing.T) {
	long := strings.Repeat("Shipped a thing today. ", 30)
	m := &stub{fn: func(context.Context, int, DraftRequest) (Draft, error) { return Draft{Text: long}, nil }}
	req := testReq()
	req.Network, req.Rules = Twitter, Rules{MaxChars: 280, Threads: true, ImageOK: true}
	res, err := (&Service{Model: m}).DraftFor(context.Background(), req, nil)
	if err != nil || !res.UsedTemplate || m.calls != 2 {
		t.Fatalf("err=%v calls=%d res=%+v", err, m.calls, res)
	}
	if len(res.Draft.Thread) > 0 || len([]rune(res.Draft.Text)) > 280 {
		t.Fatalf("template draft = %+v, want one post under 280", res.Draft)
	}
}

func TestOverByIgnoresALinkThatMovesToTheFirstComment(t *testing.T) {
	text := strings.Repeat("word ", 55) + "https://example.com/a/very/long/release/notes/link/that/pushes/it/over"
	if n := overBy(Draft{Text: text}, Rules{MaxChars: 300, LinkInFirstComment: true}); n != 0 {
		t.Fatalf("overBy = %d, want 0 once the link moves out", n)
	}
	if n := overBy(Draft{Text: text}, Rules{MaxChars: 300}); n <= 0 {
		t.Fatalf("overBy = %d, want over when the link stays", n)
	}
}
