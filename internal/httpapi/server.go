// Package httpapi exposes authenticated snapshots and controller commands. It
// never accesses hardware; the controller serializes every hardware operation.
package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dochevviktor/server-fan-control/internal/config"
	"github.com/dochevviktor/server-fan-control/internal/control"
)

const cookieName = "fan_control_session"
const sessionLifetime = 2 * time.Hour

type Service interface {
	Snapshot() control.Status
	GPUs() []control.GPU
	Config() config.Config
	History(string) []control.HistoryPoint
	Update(context.Context, config.Config, uint64) (config.Config, error)
	Automatic(context.Context, string) (control.AutomaticResult, error)
}

var _ Service = (*control.Service)(nil)

type Options struct {
	AdminToken    string
	DevNoAuth     bool
	SecureCookies bool
	// AllowedOrigin is the public HTTP(S) origin when a reverse proxy terminates
	// TLS. Forwarded headers are deliberately not trusted to set this value.
	AllowedOrigin string
	Assets        fs.FS
}

type session struct {
	CSRF    string
	Expires time.Time
}

type Server struct {
	service        Service
	options        Options
	token          [32]byte
	now            func() time.Time
	commandTimeout time.Duration
	mu             sync.Mutex
	sessions       map[string]session
	loginWindow    time.Time
	loginCount     int
}

// New requires a strong secret unless the caller explicitly enables local mock
// development. The CLI must restrict DevNoAuth to mock mode on a loopback bind.
func New(service Service, options Options) (*Server, error) {
	if service == nil {
		return nil, errors.New("HTTP controller is required")
	}
	if !options.DevNoAuth && (len(options.AdminToken) < 32 || len(options.AdminToken) > 4096) {
		return nil, errors.New("admin token must contain 32..4096 bytes")
	}
	if options.AllowedOrigin != "" {
		u, err := url.Parse(options.AllowedOrigin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("public origin must be an HTTP(S) origin without path, query or credentials")
		}
		if u.Scheme == "https" && !options.SecureCookies {
			return nil, errors.New("HTTPS public origin requires secure cookies")
		}
	}
	s := &Server{service: service, options: options, token: sha256.Sum256([]byte(options.AdminToken)), now: time.Now, commandTimeout: 10 * time.Second, sessions: make(map[string]session)}
	// Keep only the hash in memory after construction.
	s.options.AdminToken = ""
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if r.URL.Path == "/healthz" {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, "GET, HEAD")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		s.serveAsset(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/api/v1/session" && r.Method == http.MethodPost {
		if !s.checkOrigin(w, r) {
			return
		}
		s.login(w, r)
		return
	}
	sess, ok := s.authenticate(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if !s.checkOrigin(w, r) {
			return
		}
		provided := sha256.Sum256([]byte(r.Header.Get("X-CSRF-Token")))
		expected := sha256.Sum256([]byte(sess.CSRF))
		if subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
	}
	s.route(w, r, sess)
}

func (s *Server) route(w http.ResponseWriter, r *http.Request, sess session) {
	switch r.URL.Path {
	case "/api/v1/session":
		switch r.Method {
		case http.MethodGet:
			s.writeSession(w, sess)
		case http.MethodDelete:
			if cookie, err := r.Cookie(cookieName); err == nil {
				s.mu.Lock()
				delete(s.sessions, cookie.Value)
				s.mu.Unlock()
			}
			http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.options.SecureCookies, SameSite: http.SameSiteStrictMode})
			w.WriteHeader(http.StatusNoContent)
		default:
			methodNotAllowed(w, "GET, POST, DELETE")
		}
	case "/api/v1/status":
		if !onlyGET(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, s.service.Snapshot())
	case "/api/v1/gpus":
		if !onlyGET(w, r) {
			return
		}
		gpus := s.service.GPUs()
		if gpus == nil {
			gpus = []control.GPU{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"gpus": gpus})
	case "/api/v1/history":
		if !onlyGET(w, r) {
			return
		}
		uuid := r.URL.Query().Get("uuid")
		if uuid == "" || len(uuid) > 128 {
			writeError(w, http.StatusBadRequest, "a GPU uuid is required")
			return
		}
		history := s.service.History(uuid)
		if history == nil {
			history = []control.HistoryPoint{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"history": history})
	case "/api/v1/config":
		switch r.Method {
		case http.MethodGet:
			cfg := s.service.Config()
			w.Header().Set("ETag", revisionETag(cfg.Revision))
			writeJSON(w, http.StatusOK, cfg)
		case http.MethodPut:
			s.updateConfig(w, r)
		default:
			methodNotAllowed(w, "GET, PUT")
		}
	default:
		const prefix, suffix = "/api/v1/gpus/", "/default"
		if strings.HasPrefix(r.URL.Path, prefix) && strings.HasSuffix(r.URL.Path, suffix) {
			if r.Method != http.MethodPost {
				methodNotAllowed(w, "POST")
				return
			}
			uuid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), suffix)
			if uuid == "" || len(uuid) > 128 || strings.Contains(uuid, "/") {
				writeError(w, http.StatusBadRequest, "invalid GPU uuid")
				return
			}
			// No config write precondition may prevent an emergency restoration.
			ctx, cancel := context.WithTimeout(r.Context(), s.commandTimeout)
			defer cancel()
			result, err := s.service.Automatic(ctx, uuid)
			if err != nil {
				s.serviceError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
		writeError(w, http.StatusNotFound, "endpoint not found")
	}
}

func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request) {
	match := r.Header.Get("If-Match")
	if match == "" {
		writeError(w, http.StatusPreconditionRequired, "If-Match with the current configuration ETag is required")
		return
	}
	if len(match) < 3 || match[0] != '"' || match[len(match)-1] != '"' {
		writeError(w, http.StatusBadRequest, "If-Match must be a quoted revision")
		return
	}
	expected, err := strconv.ParseUint(match[1:len(match)-1], 10, 64)
	if err != nil || revisionETag(expected) != match {
		writeError(w, http.StatusBadRequest, "If-Match must be a quoted revision")
		return
	}
	var cfg config.Config
	if !decodeJSON(w, r, &cfg, 1<<20) {
		return
	}
	if cfg.Revision != expected {
		writeError(w, http.StatusBadRequest, "configuration revision must match If-Match")
		return
	}
	if err := config.Validate(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.commandTimeout)
	defer cancel()
	updated, err := s.service.Update(ctx, cfg, expected)
	if err != nil {
		s.serviceError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(updated.Revision))
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) serviceError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := err.Error()
	if errors.Is(err, config.ErrConflict) {
		status = http.StatusPreconditionFailed
		w.Header().Set("ETag", revisionETag(s.service.Config().Revision))
	} else if errors.Is(err, control.ErrNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, control.ErrInvalidPolicy) {
		status = http.StatusBadRequest
	} else if errors.Is(err, control.ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusServiceUnavailable
		if errors.Is(err, context.DeadlineExceeded) {
			message = "timed out waiting for the controller; an already-started operation may still complete; refresh status before retrying"
		} else if errors.Is(err, context.Canceled) {
			message = "request canceled while waiting for the controller; an already-started operation may still complete; refresh status before retrying"
		}
	}
	writeError(w, status, message)
}

func (s *Server) checkOrigin(w http.ResponseWriter, r *http.Request) bool {
	expected := s.options.AllowedOrigin
	if expected == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		expected = scheme + "://" + r.Host
	}
	if r.Header.Get("Origin") != expected || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, http.StatusForbidden, "request origin is not allowed")
		return false
	}
	return true
}

func (s *Server) authenticate(r *http.Request) (session, bool) {
	if s.options.DevNoAuth {
		return session{CSRF: "development", Expires: s.now().Add(sessionLifetime)}, true
	}
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[cookie.Value]
	if !ok {
		return session{}, false
	}
	if !s.now().Before(sess.Expires) {
		delete(s.sessions, cookie.Value)
		return session{}, false
	}
	return sess, true
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token string `json:"token"`
	}
	// Allow JSON escaping overhead for the largest token accepted by the CLI.
	if !decodeJSON(w, r, &input, 32<<10) {
		return
	}
	if len(input.Token) > 4096 {
		writeError(w, http.StatusBadRequest, "admin token exceeds 4096 bytes")
		return
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Sub(s.loginWindow) >= time.Minute {
		s.loginWindow, s.loginCount = now, 0
	}
	if s.loginCount >= 10 {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many login attempts; try again in a minute")
		return
	}
	s.loginCount++
	provided := sha256.Sum256([]byte(strings.TrimSpace(input.Token)))
	if !s.options.DevNoAuth && subtle.ConstantTimeCompare(provided[:], s.token[:]) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid admin token")
		return
	}
	if s.options.DevNoAuth {
		s.writeSession(w, session{CSRF: "development", Expires: now.Add(sessionLifetime)})
		return
	}
	for key, sess := range s.sessions {
		if !now.Before(sess.Expires) {
			delete(s.sessions, key)
		}
	}
	// A single-admin service needs few sessions. Keep memory bounded without
	// silently invalidating a currently active browser session.
	if len(s.sessions) >= 64 {
		writeError(w, http.StatusTooManyRequests, "too many active sessions; sign out or wait for expiry")
		return
	}
	id, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot create session")
		return
	}
	csrf, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot create session")
		return
	}
	sess := session{CSRF: csrf, Expires: now.Add(sessionLifetime)}
	s.sessions[id] = sess
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: id, Path: "/", Expires: sess.Expires, MaxAge: int(sessionLifetime.Seconds()), HttpOnly: true, Secure: s.options.SecureCookies, SameSite: http.SameSiteStrictMode})
	s.writeSession(w, sess)
}

func (s *Server) writeSession(w http.ResponseWriter, sess session) {
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": sess.CSRF, "expires_at": sess.Expires})
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if s.options.Assets == nil {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.options.Assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any, maxBytes int64) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request must contain one JSON value")
		return false
	}
	return true
}

func randomToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func revisionETag(revision uint64) string { return fmt.Sprintf("\"%d\"", revision) }

func onlyGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	methodNotAllowed(w, "GET")
	return false
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
