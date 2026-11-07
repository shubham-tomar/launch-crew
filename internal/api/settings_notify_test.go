package api_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/shubham-tomar/launchcrew/internal/api"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/httpserver"
	"github.com/shubham-tomar/launchcrew/internal/mail"
	"github.com/shubham-tomar/launchcrew/internal/session"
)

type mailbox struct {
	mu   sync.Mutex
	msgs []mail.Message
}

func (m *mailbox) Send(_ context.Context, msg mail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, msg)
	return nil
}

func TestCaptainNotifySettings(t *testing.T) {
	e := setup(t)
	box := &mailbox{}
	e.h = httpserver.New(httpserver.Deps{Settings: api.NewSettings(e.deps, e.st, "", api.WithMail(box))})

	u, _ := e.q.CreateUser(e.ctx)
	t.Cleanup(func() { _, _ = e.deps.Pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID) })
	c := e.cookie(t, session.Session{UserID: u.ID})
	// Crew only: no notification settings.
	if v := decode(t, e.do("GET", "/api/me", "", nil, c)); v["notify"] != nil {
		t.Fatalf("crew sees notify: %v", v)
	}
	repo, err := e.q.UpsertRepo(e.ctx, sqlc.UpsertRepoParams{
		InstallationID: 7_000_000_000 + int64(uuid.New().ID()), OwnerUserID: uuid.NullUUID{UUID: u.ID, Valid: true}, FullName: "acme/notify",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.deps.Pool.Exec(context.Background(), `DELETE FROM repos WHERE id = $1`, repo.ID) })

	notify := func() map[string]any {
		t.Helper()
		n, _ := decode(t, e.do("GET", "/api/me", "", nil, c))["notify"].(map[string]any)
		return n
	}
	if n := notify(); n["github"] != true || n["releaseNotes"] != false || n["email"] != false || n["mailAvailable"] != true {
		t.Fatalf("defaults %v", n)
	}

	// Email cannot turn on before an address is verified.
	rr := e.do("PUT", "/api/me/notify", `{"github":false,"releaseNotes":true,"email":true}`, nil, c)
	if v := decode(t, rr); rr.Code != 200 || v["github"] != false || v["releaseNotes"] != true || v["email"] != false {
		t.Fatalf("put: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do("PUT", "/api/me/notify", `{"github":true}`, nil, c); rr.Code != http.StatusBadRequest {
		t.Fatalf("partial put: %d", rr.Code)
	}
	if rr := e.do("PUT", "/api/me/notify", `{"github":true,"releaseNotes":true,"email":true}`, nil, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", rr.Code)
	}

	for _, bad := range []string{"", "nope", "a@b", "Cap <cap@lc.test>", "a@b.test, c@d.test"} {
		if rr := e.do("POST", "/api/me/notify/email", `{"address":"`+bad+`"}`, nil, c); rr.Code != http.StatusBadRequest {
			t.Fatalf("address %q: %d", bad, rr.Code)
		}
	}
	rr = e.do("POST", "/api/me/notify/email", `{"address":"cap@lc.test"}`, nil, c)
	if v := decode(t, rr); rr.Code != 200 || v["emailPending"] != "cap@lc.test" || v["email"] != false {
		t.Fatalf("start: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do("POST", "/api/me/notify/email", `{"address":"cap@lc.test"}`, nil, c); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("resend at once: %d", rr.Code)
	}
	if len(box.msgs) != 1 || box.msgs[0].To != "cap@lc.test" {
		t.Fatalf("mail %+v", box.msgs)
	}
	_, link, _ := strings.Cut(box.msgs[0].Text, "https://")
	link, _, _ = strings.Cut(link, "\n")
	path := link[strings.Index(link, "/api/"):]

	// The link works without a session, once.
	rr = e.do("GET", path, "", nil, nil)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/settings?email=verified" {
		t.Fatalf("verify: %d %s", rr.Code, rr.Header().Get("Location"))
	}
	if n := notify(); n["email"] != true || n["emailAddress"] != "cap@lc.test" || n["emailPending"] != "" {
		t.Fatalf("after verify %v", n)
	}
	if rr := e.do("GET", path, "", nil, nil); rr.Header().Get("Location") != "/settings?email=expired" {
		t.Fatalf("reused link: %s", rr.Header().Get("Location"))
	}
	if rr := e.do("DELETE", "/api/me/notify/email", "", nil, c); rr.Code != 200 || decode(t, rr)["emailAddress"] != "" {
		t.Fatalf("clear: %d %s", rr.Code, rr.Body)
	}
	if n := notify(); n["email"] != false || n["releaseNotes"] != true {
		t.Fatalf("after clear %v", n)
	}
}

func TestEmailUnavailableWithoutMail(t *testing.T) {
	e := settingsEnv(t)
	u, _ := e.q.CreateUser(e.ctx)
	t.Cleanup(func() { _, _ = e.deps.Pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID) })
	c := e.cookie(t, session.Session{UserID: u.ID})
	if rr := e.do("POST", "/api/me/notify/email", `{"address":"cap@lc.test"}`, nil, c); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("no mailer: %d", rr.Code)
	}
}
