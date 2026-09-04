package console

import (
	"embed"
	"fmt"
	"hash/crc32"
	"io/fs"
	"net/http"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/retrofilter/rf/core"
)

//go:embed static
var staticFS embed.FS

var staticETags = func() map[string]string {
	m := map[string]string{}
	fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := staticFS.ReadFile(p)
		if err != nil {
			return err
		}
		m["/"+p] = fmt.Sprintf(`"%08x"`, crc32.ChecksumIEEE(b))
		return nil
	})
	return m
}()

const cookieName = "rf_web_session"

const loginFailDelay = 300 * time.Millisecond

// Config wires a console server.
type Config struct {
	Token        string           // static token — tests and webshot; ignored when TokenPath is set
	TokenPath    string           // persistent token file, re-read and age-rotated lazily (token.go)
	SessionsPath string           // signed-in browser sessions file (auth.go); "" keeps them in memory
	Port         int              // stamped into spawned sessions as RF_WEB_PORT
	SpawnCommand string           // test override; "" means the running rf binary
	Store        *core.GraphStore // the knowledge-base graphs behind the overview; nil renders it empty
	StatePath    string           // fleet state file for restart recovery (state.go); "" disables
}

type Server struct {
	cfg   Config
	auth  *authState
	mgr   *Manager
	reg   *Registry
	store *core.GraphStore
	usage *usageCache
	sys   *sysStats
	mux   *http.ServeMux
}

func NewServer(cfg Config) *Server {
	mgr := NewManager(cfg.SpawnCommand, cfg.Port)
	mgr.statePath = cfg.StatePath
	if cfg.StatePath != "" {
		go mgr.persistLoop()
	}
	s := &Server{cfg: cfg, auth: newAuthState(cfg.Token, cfg.TokenPath, cfg.SessionsPath), mgr: mgr, reg: NewRegistry(mgr), store: cfg.Store, usage: newUsageCache(), sys: newSysStats(), mux: http.NewServeMux()}
	mgr.onExit = s.reg.Invalidate
	if cfg.Store != nil {
		go s.scheduleLoop()
	}

	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	staticSrv := http.FileServer(http.FS(staticFS))
	s.mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag, ok := staticETags[r.URL.Path]; ok {
			w.Header().Set("ETag", tag)
			w.Header().Set("Cache-Control", "no-cache")
		}
		staticSrv.ServeHTTP(w, r)
	}))
	s.mux.HandleFunc("GET /ui/poll", s.handlePoll)
	s.mux.HandleFunc("POST /ui/sessions", s.handleSpawn)
	s.mux.HandleFunc("GET /ui/overview", s.handleOverview)
	s.mux.HandleFunc("GET /ui/graph", s.handleGraph)
	s.mux.HandleFunc("POST /ui/close", s.handleClose)
	s.mux.HandleFunc("POST /ui/start", s.handleStart)
	s.mux.HandleFunc("GET /api/attach/{id}", s.handleAttach)
	return s
}

// Handler is the full route tree behind auth.
func (s *Server) Handler() http.Handler {
	root := http.NewServeMux()
	root.HandleFunc("POST /api/hook", s.requireQueryToken(s.handleHook))
	root.HandleFunc("POST /api/rename", s.requireQueryToken(s.handleRename))
	root.HandleFunc("GET /api/sessions", s.requireQueryToken(s.handleSessions))
	root.HandleFunc("POST /api/sessions", s.requireQueryToken(s.handleNewSession))
	root.HandleFunc("GET /login", s.handleLoginPage)
	root.HandleFunc("POST /login", s.handleLogin)
	root.Handle("/", s.withAuth(s.mux))
	return root
}

// MintLaunchNonce issues a single-use, short-TTL sign-in secret for the auto-
// opened browser URL, so the persistent token never reaches the browser or
// its history.
func (s *Server) MintLaunchNonce() string {
	return s.auth.mintNonce()
}

// Recover respawns the fleet recorded in the state file — call once at
// startup, before serving. See Manager.Recover.
func (s *Server) Recover() ([]string, error) {
	return s.mgr.Recover()
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok := r.URL.Query().Get("token"); tok != "" {
			if !s.auth.takeNonce(tok) && !s.auth.tokenOK(tok) {
				s.unauthorized(w, r)
				return
			}
			if c, err := r.Cookie(cookieName); err != nil || !s.auth.sessionOK(c.Value) {
				s.setSessionCookie(w, r)
			}
			if r.Method == http.MethodGet && r.Header.Get("Upgrade") == "" {
				u := *r.URL
				q := u.Query()
				q.Del("token")
				u.RawQuery = q.Encode()
				http.Redirect(w, r, u.String(), http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(cookieName); err == nil && s.auth.sessionOK(c.Value) {
			next.ServeHTTP(w, r)
			return
		}
		s.unauthorized(w, r)
	})
}

func (s *Server) unauthorized(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintln(w, "sign in at /login with the token from `rf token`")
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    s.auth.mintSession(),
		Path:     "/",
		MaxAge:   int(sessionMaxAge / time.Second),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil && s.auth.sessionOK(c.Value) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = loginPage("").Render(r.Context(), w)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.auth.tokenOK(strings.TrimSpace(r.FormValue("token"))) {
		time.Sleep(loginFailDelay)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = loginPage("invalid token").Render(r.Context(), w)
		return
	}
	s.setSessionCookie(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) requireQueryToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.tokenOK(r.URL.Query().Get("token")) {
			http.Error(w, "token required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func whoami() string {
	name := "rf"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	host, err := os.Hostname()
	if err != nil {
		return name
	}
	host, _, _ = strings.Cut(host, ".")
	return name + "@" + strings.ToLower(host)
}
