package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dochevviktor/nvidia-fan-control/internal/config"
	"github.com/dochevviktor/nvidia-fan-control/internal/control"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeService struct {
	cfg             config.Config
	updateCalls     int
	automaticCalls  int
	updateError     error
	automaticError  error
	automaticResult control.AutomaticResult
	blockCommands   bool
}

func (s *fakeService) Snapshot() control.Status {
	return control.Status{Mock: true, DesiredRevision: s.cfg.Revision}
}
func (s *fakeService) GPUs() []control.GPU                   { return nil }
func (s *fakeService) Config() config.Config                 { return config.Clone(s.cfg) }
func (s *fakeService) History(string) []control.HistoryPoint { return nil }
func (s *fakeService) Update(ctx context.Context, cfg config.Config, expected uint64) (config.Config, error) {
	s.updateCalls++
	if s.blockCommands {
		<-ctx.Done()
		return s.cfg, ctx.Err()
	}
	if s.updateError != nil {
		return s.cfg, s.updateError
	}
	if expected != s.cfg.Revision {
		return s.cfg, config.ErrConflict
	}
	cfg.Revision++
	s.cfg = config.Clone(cfg)
	return cfg, nil
}
func (s *fakeService) Automatic(ctx context.Context, _ string) (control.AutomaticResult, error) {
	s.automaticCalls++
	if s.blockCommands {
		<-ctx.Done()
		return s.automaticResult, ctx.Err()
	}
	return s.automaticResult, s.automaticError
}

func testServer(t *testing.T) (*Server, *fakeService) {
	t.Helper()
	service := &fakeService{cfg: config.Default()}
	server, err := New(service, Options{AdminToken: testToken, Assets: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>fan dashboard</html>")}}})
	if err != nil {
		t.Fatal(err)
	}
	return server, service
}

type credentials struct {
	cookie *http.Cookie
	csrf   string
}

func request(server *Server, method, target string, body []byte, auth credentials, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://example.com"+target, bytes.NewReader(body))
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if auth.cookie != nil {
		r.AddCookie(auth.cookie)
	}
	if auth.csrf != "" {
		r.Header.Set("X-CSRF-Token", auth.csrf)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	return w
}

func login(t *testing.T, server *Server) credentials {
	t.Helper()
	w := request(server, http.MethodPost, "/api/v1/session", []byte(`{"token":"`+testToken+`"}`), credentials{}, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	var result struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || result.CSRF == "" {
		t.Fatalf("missing login credentials: %s", w.Body)
	}
	return credentials{cookie: cookies[0], csrf: result.CSRF}
}

func TestProtectedEndpointsAndPublicLiveness(t *testing.T) {
	server, service := testServer(t)
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/session"}, {"GET", "/api/v1/config"}, {"GET", "/api/v1/status"}, {"GET", "/api/v1/gpus"}, {"GET", "/api/v1/history?uuid=GPU-test"}, {"PUT", "/api/v1/config"}, {"POST", "/api/v1/gpus/GPU-test/default"},
	} {
		if w := request(server, route.method, route.path, nil, credentials{}, "http://example.com"); w.Code != 401 {
			t.Errorf("%s %s: %d", route.method, route.path, w.Code)
		}
	}
	for _, path := range []string{"/healthz", "/"} {
		if w := request(server, "GET", path, nil, credentials{}, ""); w.Code != 200 {
			t.Errorf("public %s: %d", path, w.Code)
		}
	}
	if service.automaticCalls != 0 || service.updateCalls != 0 {
		t.Fatal("unauthorized command reached controller")
	}
}

func TestSessionCookieExpiryAndLogout(t *testing.T) {
	server, _ := testServer(t)
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	auth := login(t, server)
	if !auth.cookie.HttpOnly || auth.cookie.SameSite != http.SameSiteStrictMode || auth.cookie.Path != "/" || auth.cookie.MaxAge != 7200 || auth.cookie.Secure {
		t.Fatalf("incorrect cookie attributes: %+v", auth.cookie)
	}
	if w := request(server, "GET", "/api/v1/session", nil, auth, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	now = now.Add(sessionLifetime)
	if w := request(server, "GET", "/api/v1/config", nil, auth, ""); w.Code != 401 {
		t.Fatalf("expired session accepted: %d", w.Code)
	}
	auth = login(t, server)
	if w := request(server, "DELETE", "/api/v1/session", nil, auth, "http://example.com"); w.Code != 204 || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("logout: %d %s", w.Code, w.Body)
	}
	if w := request(server, "GET", "/api/v1/session", nil, auth, ""); w.Code != 401 {
		t.Fatal("logout did not revoke session")
	}
}

func TestMutationsRequireOriginAndCSRF(t *testing.T) {
	server, service := testServer(t)
	auth := login(t, server)
	for _, test := range []struct{ origin, csrf string }{
		{"", auth.csrf}, {"http://attacker.example", auth.csrf}, {"http://example.com", ""}, {"http://example.com", "wrong"},
	} {
		w := request(server, "POST", "/api/v1/gpus/GPU-test/default", nil, credentials{auth.cookie, test.csrf}, test.origin)
		if w.Code != 403 {
			t.Errorf("origin %q csrf %q: %d", test.origin, test.csrf, w.Code)
		}
	}
	if service.automaticCalls != 0 {
		t.Fatal("invalid request reached controller")
	}
	if w := request(server, "POST", "/api/v1/gpus/GPU-test/default", nil, auth, "http://example.com"); w.Code != 200 {
		t.Fatalf("valid request: %d %s", w.Code, w.Body)
	}
}

func TestLoginProtectionAndBounds(t *testing.T) {
	server, _ := testServer(t)
	if w := request(server, "POST", "/api/v1/session", []byte(`{"token":"`+testToken+`"}`), credentials{}, ""); w.Code != 403 {
		t.Fatal("login without origin accepted")
	}
	if w := request(server, "POST", "/api/v1/session", []byte(`{"token":"`+strings.Repeat("x", 5000)+`"}`), credentials{}, "http://example.com"); w.Code != 400 {
		t.Fatal("oversized login body accepted")
	}
	for i := 0; i < 10; i++ {
		if w := request(server, "POST", "/api/v1/session", []byte(`{"token":"wrong"}`), credentials{}, "http://example.com"); w.Code != 401 {
			t.Fatalf("wrong token attempt %d: %d", i, w.Code)
		}
	}
	if w := request(server, "POST", "/api/v1/session", []byte(`{"token":"`+testToken+`"}`), credentials{}, "http://example.com"); w.Code != 429 || w.Header().Get("Retry-After") != "60" {
		t.Fatalf("login limiter: %d", w.Code)
	}
}

func TestMaximumTokenCanBeEncodedAndExchanged(t *testing.T) {
	token := strings.Repeat("\\", 4096)
	server, err := New(&fakeService{cfg: config.Default()}, Options{AdminToken: token})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"token": token})
	w := request(server, "POST", "/api/v1/session", body, credentials{}, "http://example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("maximum token failed login after JSON escaping: %d %s", w.Code, w.Body)
	}
}

func TestPublicOriginAndSecureCookies(t *testing.T) {
	server, err := New(&fakeService{cfg: config.Default()}, Options{AdminToken: testToken, AllowedOrigin: "https://fans.example", SecureCookies: true})
	if err != nil {
		t.Fatal(err)
	}
	w := request(server, "POST", "/api/v1/session", []byte(`{"token":"`+testToken+`"}`), credentials{}, "https://fans.example")
	if w.Code != 200 || !w.Result().Cookies()[0].Secure {
		t.Fatalf("public proxy origin: %d", w.Code)
	}
	w = request(server, "POST", "/api/v1/session", []byte(`{"token":"`+testToken+`"}`), credentials{}, "http://example.com")
	if w.Code != 403 {
		t.Fatal("internal origin accepted when public origin configured")
	}
	for _, options := range []Options{
		{AdminToken: "short"}, {AdminToken: testToken, AllowedOrigin: "https://fans.example/path"}, {AdminToken: testToken, AllowedOrigin: "https://fans.example"}, {AdminToken: testToken, AllowedOrigin: "https://user:pass@fans.example", SecureCookies: true},
	} {
		if _, err := New(&fakeService{}, options); err == nil {
			t.Errorf("unsafe options accepted: %+v", options)
		}
	}
}

func putConfig(server *Server, auth credentials, body []byte, etag string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("PUT", "http://example.com/api/v1/config", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://example.com")
	r.Header.Set("X-CSRF-Token", auth.csrf)
	if etag != "" {
		r.Header.Set("If-Match", etag)
	}
	r.AddCookie(auth.cookie)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	return w
}

func TestConfigurationRevisionAndValidation(t *testing.T) {
	server, service := testServer(t)
	auth := login(t, server)
	w := request(server, "GET", "/api/v1/config", nil, auth, "")
	if w.Code != 200 || w.Header().Get("ETag") != `"1"` {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	body, _ := json.Marshal(service.cfg)
	if w := putConfig(server, auth, body, ""); w.Code != 428 {
		t.Fatal("missing precondition accepted")
	}
	if w := putConfig(server, auth, body, `W/"1"`); w.Code != 400 {
		t.Fatal("weak precondition accepted")
	}
	if w := putConfig(server, auth, body, `"2"`); w.Code != 400 {
		t.Fatal("body/header revision mismatch accepted")
	}
	invalid := config.Clone(service.cfg)
	invalid.SamplingSeconds = 0
	badBody, _ := json.Marshal(invalid)
	if w := putConfig(server, auth, badBody, `"1"`); w.Code != 400 {
		t.Fatal("invalid sampling accepted")
	}
	if service.updateCalls != 0 {
		t.Fatal("invalid config reached controller")
	}
	w = putConfig(server, auth, body, `"1"`)
	if w.Code != 200 || w.Header().Get("ETag") != `"2"` {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	w = putConfig(server, auth, body, `"1"`)
	if w.Code != 412 || w.Header().Get("ETag") != `"2"` {
		t.Fatalf("conflict: %d %s", w.Code, w.Body)
	}
	if service.cfg.Revision != 2 {
		t.Fatal("conflict changed revision")
	}
}

func TestConfigurationRejectsExtraAndOversizedJSON(t *testing.T) {
	server, service := testServer(t)
	auth := login(t, server)
	valid, _ := json.Marshal(service.cfg)
	for _, body := range [][]byte{append(append([]byte{}, valid...), []byte(`{}`)...), []byte(`{"unexpected":true}`), []byte(`{"unexpected":"` + strings.Repeat("x", 1<<20) + `"}`)} {
		if w := putConfig(server, auth, body, `"1"`); w.Code != 400 {
			t.Errorf("malformed JSON accepted: %d", w.Code)
		}
	}
	if service.updateCalls != 0 {
		t.Fatal("malformed JSON reached controller")
	}
}

func TestAutomaticReturnsHardwareAndStorageOutcomesSeparately(t *testing.T) {
	server, service := testServer(t)
	auth := login(t, server)
	service.automaticResult = control.AutomaticResult{UUID: "GPU-test", Restored: true, Persisted: false, StorageError: "read-only configuration directory", Revision: 4}
	w := request(server, "POST", "/api/v1/gpus/GPU-test/default", nil, auth, "http://example.com")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var result control.AutomaticResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Restored || result.Persisted || result.StorageError == "" {
		t.Fatalf("lost distinct outcomes: %+v", result)
	}
	service.automaticError = control.ErrNotFound
	if w := request(server, "POST", "/api/v1/gpus/GPU-absent/default", nil, auth, "http://example.com"); w.Code != 404 {
		t.Fatal("unknown GPU should return 404")
	}
	service.automaticError = control.ErrUnavailable
	if w := request(server, "POST", "/api/v1/gpus/GPU-test/default", nil, auth, "http://example.com"); w.Code != 503 {
		t.Fatal("unavailable controller should return 503")
	}
}

func TestFailedStorageDoesNotClaimSavedRevision(t *testing.T) {
	server, service := testServer(t)
	auth := login(t, server)
	service.updateError = errors.New("disk full")
	body, _ := json.Marshal(service.cfg)
	w := putConfig(server, auth, body, `"1"`)
	if w.Code != 500 || w.Header().Get("ETag") != "" || service.cfg.Revision != 1 {
		t.Fatalf("failed update: %d %s", w.Code, w.Body)
	}
}

func TestControllerHardwarePolicyValidation(t *testing.T) {
	server, service := testServer(t)
	auth := login(t, server)
	service.updateError = control.ErrInvalidPolicy
	body, _ := json.Marshal(service.cfg)
	w := putConfig(server, auth, body, `"1"`)
	if w.Code != http.StatusBadRequest || service.cfg.Revision != 1 {
		t.Fatalf("unsupported hardware policy: %d %s", w.Code, w.Body)
	}
}

func TestDevelopmentSessionMatchesMutationToken(t *testing.T) {
	service := &fakeService{cfg: config.Default()}
	server, err := New(service, Options{DevNoAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	w := request(server, "GET", "/api/v1/session", nil, credentials{}, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"csrf_token":"development"`) {
		t.Fatalf("dev session: %d %s", w.Code, w.Body)
	}
	if w := request(server, "POST", "/api/v1/gpus/GPU-test/default", nil, credentials{csrf: "development"}, "http://example.com"); w.Code != 200 {
		t.Fatalf("dev command: %d %s", w.Code, w.Body)
	}
}

func TestControllerCommandTimeoutDoesNotClaimCancellation(t *testing.T) {
	server, service := testServer(t)
	server.commandTimeout = 10 * time.Millisecond
	service.blockCommands = true
	auth := login(t, server)
	body, _ := json.Marshal(service.cfg)
	started := time.Now()
	for _, response := range []*httptest.ResponseRecorder{
		putConfig(server, auth, body, `"1"`),
		request(server, "POST", "/api/v1/gpus/GPU-test/default", nil, auth, "http://example.com"),
	} {
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "already-started operation may still complete") {
			t.Fatalf("uncertain command outcome not explained: %d %s", response.Code, response.Body)
		}
	}
	if time.Since(started) > time.Second {
		t.Fatal("blocked mutation did not respect its context timeout")
	}
	if w := request(server, "GET", "/api/v1/status", nil, auth, ""); w.Code != http.StatusOK {
		t.Fatal("snapshots became inaccessible after command timeout")
	}
}
