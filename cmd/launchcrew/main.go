package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/shubham-tomar/launchcrew/internal/api"
	"github.com/shubham-tomar/launchcrew/internal/auth"
	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/crew"
	"github.com/shubham-tomar/launchcrew/internal/crypto"
	"github.com/shubham-tomar/launchcrew/internal/db"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/drafting"
	"github.com/shubham-tomar/launchcrew/internal/github"
	"github.com/shubham-tomar/launchcrew/internal/httpserver"
	"github.com/shubham-tomar/launchcrew/internal/identity"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/launch"
	"github.com/shubham-tomar/launchcrew/internal/mail"
	"github.com/shubham-tomar/launchcrew/internal/preflight"
	"github.com/shubham-tomar/launchcrew/internal/session"
	"github.com/shubham-tomar/launchcrew/internal/trigger"
	"github.com/shubham-tomar/launchcrew/internal/webhook"
)

// On SIGTERM the HTTP server gets shutdownTimeout to finish requests, then
// the worker gets drainTimeout to finish in-flight jobs. Fly kills the
// machine kill_timeout (fly.toml) after SIGTERM, which must cover both.
const (
	shutdownTimeout = 10 * time.Second
	drainTimeout    = 15 * time.Second
)

func main() {
	cfg, e2e, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if err := checkDirectURL(cfg.DatabaseURL); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatal(err)
	}
	keyring, err := crypto.ParseKeyring(cfg.Keyring)
	if err != nil {
		log.Fatal(err)
	}
	q := sqlc.New(pool)
	sessions := &session.Store{Keyring: keyring, PlainHTTP: e2e != nil}
	ghc, err := github.NewFromConfig(cfg, e2e.githubOptions()...)
	if err != nil {
		log.Fatal(err)
	}
	buf := buffer.New(&http.Client{Timeout: 30 * time.Second}, q, keyring,
		append([]buffer.Option{buffer.WithCredentials(cfg.BufferClientID, cfg.BufferClientSecret)}, e2e.bufferOptions()...)...)
	claude := drafting.NewClaude(cfg.AnthropicAPIKey, e2e.claudeOptions()...)
	enqueue := func(ctx context.Context, kind jobs.Kind, payload any, runAt time.Time, launchID *uuid.UUID) (int64, error) {
		return jobs.Enqueue(ctx, q, kind, payload, runAt, launchID)
	}
	crewDeps := crew.Deps{
		Q: q, Pool: pool, Buffer: buf,
		Preflight: preflight.Deps{Fetch: preflight.HTTPFetch, Config: buf.Configuration, Limits: buf.DailyPostingLimits},
		Drafting:  &drafting.Service{Model: claude},
		Cards:     cards.NewUploader(cfg.R2Endpoint, cfg.R2Bucket, cfg.R2AccessKey, cfg.R2SecretKey, cfg.R2PublicBase),
		Enqueue:   enqueue,
		BaseURL:   cfg.BaseURL,
		Keyring:   keyring,
	}
	mailer := mail.New(cfg.MailAPIKey, cfg.MailFrom)
	launchDeps := launch.Deps{
		Q: q, Pool: pool, Buffer: buf, GitHub: ghc, Cards: crewDeps.Cards,
		Preflight: preflight.Deps{
			Fetch: preflight.HTTPFetch, Config: buf.Configuration, Limits: buf.DailyPostingLimits,
			RateHeadroom: launch.RateHeadroom(q, nil),
		},
		Keyring:       keyring,
		BaseURL:       cfg.BaseURL,
		AllowLoopback: e2e != nil,
		Mail:          mailer,
	}
	deps := httpserver.Deps{
		Ready:    pool.Ping,
		Crew:     api.NewCrew(crewDeps, sessions),
		Captain:  api.NewCaptain(launchDeps, sessions),
		Settings: api.NewSettings(crewDeps, sessions, cfg.SupportEmail, api.WithMail(mailer)),
		BufferOAuth: &auth.BufferOAuth{
			ClientID:     cfg.BufferClientID,
			ClientSecret: cfg.BufferClientSecret,
			BaseURL:      cfg.BaseURL,
			Q:            q,
			Pool:         pool,
			Keyring:      keyring,
			Sessions:     sessions,
		},
		Install: &identity.Install{Sessions: sessions, Pool: pool, GH: ghc, OAuth: ghc.UserToServerConfigFor("/github/install/callback"), AppSlug: cfg.GitHubAppSlug, Keyring: keyring},
		Verify:  &identity.Verify{OAuth: ghc.UserToServerConfig(), Q: q, Sessions: sessions},
		GitHubWebhook: &webhook.GitHub{
			Secret: []byte(cfg.GitHubWebhookSecret),
			Q:      q,
			Enqueue: func(ctx context.Context, kind jobs.Kind, payload any) error {
				_, err := jobs.Enqueue(ctx, q, kind, payload, time.Time{}, nil)
				return err
			},
		},
	}
	e2e.wire(&deps, pool)
	h := httpserver.New(deps)
	worker := &jobs.Worker{
		Pool:        pool,
		Handlers:    workerHandlers(crewDeps, launchDeps, ghc, claude),
		ID:          workerID(),
		Concurrency: cfg.WorkerConcurrency,
	}
	if err := ensureReencrypt(ctx, q, keyring); err != nil {
		log.Printf("reencrypt: %v", err)
	}
	go sweepHourly(ctx, crewDeps)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		_ = worker.Run(ctx)
	}()

	srv := newServer(cfg.Port, h)
	log.Printf("listening on :%s", cfg.Port)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	select {
	case err = <-serveErr:
		stop() // listen failed: stop the worker too
	case <-ctx.Done():
		err = nil
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	_ = srv.Shutdown(sctx)
	cancel()
	if !awaitDrain(workerDone, drainTimeout) {
		log.Printf("worker did not drain within %s; exiting with jobs in flight", drainTimeout)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Print(err)
		pool.Close()
		os.Exit(1)
	}
}

// awaitDrain waits for done, giving up after d. It reports whether done closed.
func awaitDrain(done <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

func newServer(port string, h http.Handler) *http.Server {
	return &http.Server{
		Addr: ":" + port, Handler: h,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second,
	}
}

// checkDirectURL refuses Neon's pooled endpoint: LaunchJob holds a session
// advisory lock, which a transaction pooler would hand to other clients.
func checkDirectURL(dbURL string) error {
	u, err := url.Parse(dbURL)
	if err != nil {
		return fmt.Errorf("DATABASE_URL: %w", err)
	}
	if strings.Contains(u.Hostname(), "-pooler") {
		return errors.New("DATABASE_URL points at a -pooler host; use Neon's direct connection string, since LaunchJob holds a session advisory lock that a pooler would leak")
	}
	return nil
}

// workerHandlers maps every enqueued job kind to its handler.
func workerHandlers(cd crew.Deps, ld launch.Deps, ghc *github.Client, claude *drafting.Claude) map[jobs.Kind]jobs.Handler {
	return map[jobs.Kind]jobs.Handler{
		jobs.ProcessRelease: trigger.ReleaseJob(trigger.ReleaseDeps{
			Pool: ld.Pool, Cards: cd.Cards,
			Trigger: trigger.GitHubRelease{GH: ghc, Headlines: func(ctx context.Context, notes string, prs []drafting.PR) ([]drafting.Change, []drafting.Change, error) {
				return drafting.PickHeadlines(ctx, claude, notes, prs)
			}},
		}),
		jobs.VoicedDrafts:  crew.VoicedDraftsHandler(cd),
		jobs.SweepOpened:   crew.SweepOpenedHandler(cd),
		jobs.LaunchJob:     launch.LaunchJobHandler(ld),
		jobs.SendComments:  launch.SendCommentsHandler(ld),
		jobs.CloseWindow:   launch.CloseWindowHandler(ld),
		jobs.PollStatus:    launch.PollStatusHandler(ld),
		jobs.NotifyCaptain: launch.NotifyCaptainHandler(ld),

		jobs.ReencryptTokens: jobs.ReencryptHandler(cd.Q, cd.Keyring),
	}
}

// ensureReencrypt enqueues one ReencryptTokens job when some stored token is
// off the current key and no such job is already waiting.
func ensureReencrypt(ctx context.Context, q *sqlc.Queries, kr *crypto.Keyring) error {
	pending, err := q.AnyTokenNotOnKey(ctx, sqlc.AnyTokenNotOnKeyParams{
		KeyID: kr.CurrentKeyID(), Prefix: []byte(kr.CurrentKeyID() + ":"),
	})
	if err != nil || !pending.Bool {
		return err
	}
	n, err := q.CountLiveJobsOfKind(ctx, string(jobs.ReencryptTokens))
	if err != nil || n > 0 {
		return err
	}
	_, err = jobs.Enqueue(ctx, q, jobs.ReencryptTokens, struct{}{}, time.Time{}, nil)
	return err
}

// sweepHourly keeps one SweepOpened job queued each hour.
func sweepHourly(ctx context.Context, d crew.Deps) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := crew.EnsureSweep(ctx, d); err != nil && ctx.Err() == nil {
			log.Printf("sweep: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func workerID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
