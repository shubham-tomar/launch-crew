// Package httpserver builds the HTTP handler: API routes plus the embedded front end.
package httpserver

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shubham-tomar/launchcrew/internal/auth"
	"github.com/shubham-tomar/launchcrew/internal/identity"
)

//go:embed all:dist
var distFS embed.FS

// Deps holds the collaborators handlers need. Nil members leave their routes unmounted.
type Deps struct {
	BufferOAuth   *auth.BufferOAuth
	GitHubWebhook http.Handler
	Install       *identity.Install
	Verify        *identity.Verify
	// Crew serves the crew JSON routes, mounted at /api.
	Crew http.Handler
	// Captain serves the captain JSON routes, mounted at /api/captain.
	Captain http.Handler
	// Settings serves /me, /me/opt-out and /opt-out under /api.
	Settings http.Handler
	// E2E serves the test control routes at /e2e; set only when LAUNCHCREW_E2E=1.
	E2E http.Handler
	// Ready, when set, is probed by /healthz (the database ping); an error
	// answers 503 so Fly stops routing to a machine that cannot reach it.
	Ready func(context.Context) error
	// BaseURL, when set, is the one public origin: a request on any other
	// host is redirected there, so a sign-in started on an old address does
	// not come back to another one without its cookie.
	BaseURL string
}

// readyTimeout bounds the /healthz probe; Fly's check times out at 3 s.
const readyTimeout = time.Second

// New returns the application handler.
func New(deps Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(harden)
	if c, err := url.Parse(deps.BaseURL); err == nil && c.Host != "" {
		r.Use(canonical(c))
	}
	r.Get("/healthz", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if deps.Ready != nil {
			ctx, cancel := context.WithTimeout(req.Context(), readyTimeout)
			defer cancel()
			if err := deps.Ready(ctx); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("unavailable: " + err.Error()))
				return
			}
		}
		_, _ = w.Write([]byte("ok"))
	})
	if oa := deps.BufferOAuth; oa != nil {
		r.Get("/auth/buffer/start", oa.Start)
		r.Get("/auth/buffer/callback", oa.Callback)
		// POST only: a GET that signs the person out could be triggered cross-site.
		// The GET is answered here so it does not fall through to the SPA.
		r.Post("/auth/logout", oa.Logout)
		r.Get("/auth/logout", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "sign out with a POST", http.StatusMethodNotAllowed)
		})
	}
	if in := deps.Install; in != nil {
		r.Get("/github/install/start", in.Start)
		r.Get("/github/install/callback", in.Callback)
	}
	if v := deps.Verify; v != nil {
		r.Get("/github/verify/start", v.Start)
		r.Get("/github/verify/callback", v.Callback)
	}
	if h := deps.GitHubWebhook; h != nil {
		r.Method(http.MethodPost, "/webhooks/github", h)
	}
	if deps.E2E != nil {
		r.Mount("/e2e", deps.E2E)
	} else {
		r.Handle("/e2e/*", http.HandlerFunc(apiNotFound))
	}
	if deps.Captain != nil {
		r.Mount("/api/captain", deps.Captain)
	}
	if deps.Settings != nil {
		s := http.StripPrefix("/api", deps.Settings)
		for _, p := range []string{"/api/me", "/api/me/*", "/api/opt-out"} {
			r.Handle(p, s)
		}
	}
	if deps.Crew != nil {
		r.Mount("/api", deps.Crew)
	} else {
		r.Handle("/api/*", http.HandlerFunc(apiNotFound))
	}
	r.Handle("/*", spaHandler())
	return r
}

// harden sets the response headers every route can carry. There is no
// page-wide CSP: Vite's inline bootstrap would need nonces. The SPA index
// adds its own framing headers in spaHandler.
func harden(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

func apiNotFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"not found"}`))
}

// spaHandler serves built assets, falling back to index.html for client routes.
// Only GET and HEAD are pages; any other method on an unknown path is a 404,
// so a mistyped webhook URL fails visibly instead of getting a 200 page.
func spaHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			apiNotFound(w, r)
			return
		}
		index, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, "front end not built", http.StatusServiceUnavailable)
			return
		}
		p := r.URL.Path
		if len(p) > 1 {
			if st, err := fs.Stat(sub, p[1:]); err == nil && !st.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		_, _ = w.Write(index)
	})
}

// canonical redirects requests on another host to base, keeping the path and
// query. /healthz answers on any host (Fly probes the machine directly) and
// webhooks too (GitHub does not follow redirects on a POST). GET and HEAD get
// a 301; anything else a 308, which keeps the method and body.
func canonical(base *url.URL) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.EqualFold(r.Host, base.Host) || r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/webhooks/") {
				next.ServeHTTP(w, r)
				return
			}
			code := http.StatusPermanentRedirect
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				code = http.StatusMovedPermanently
			}
			http.Redirect(w, r, base.Scheme+"://"+base.Host+r.URL.RequestURI(), code)
		})
	}
}
