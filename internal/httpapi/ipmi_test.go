package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dochevviktor/server-fan-control/internal/config"
	"github.com/dochevviktor/server-fan-control/internal/ipmi"
)

type fakeSystemFans struct {
	cfg    ipmi.Settings
	calls  int
	err    error
	result ipmi.Result
}

func (f *fakeSystemFans) Snapshot() ipmi.Snapshot { return ipmi.Snapshot{Enabled: true, Mock: true} }
func (f *fakeSystemFans) Config() ipmi.Settings   { return f.cfg }
func (f *fakeSystemFans) Update(_ context.Context, c ipmi.Settings, rev uint64) (ipmi.Settings, error) {
	f.calls++
	if f.err != nil {
		return f.cfg, f.err
	}
	if rev != f.cfg.Revision {
		return f.cfg, config.ErrConflict
	}
	c.Revision++
	f.cfg = c
	return c, nil
}
func (f *fakeSystemFans) Set(context.Context, int, int) (ipmi.Result, error) {
	f.calls++
	return f.result, f.err
}
func (f *fakeSystemFans) Automatic(context.Context) (ipmi.Result, error) {
	f.calls++
	return f.result, f.err
}

func TestIPMISecurityAndDisabledState(t *testing.T) {
	s, _ := testServer(t)
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/ipmi"}, {"GET", "/api/v1/ipmi/config"}, {"PUT", "/api/v1/ipmi/config"}, {"POST", "/api/v1/ipmi/target"}, {"POST", "/api/v1/ipmi/default"}} {
		if w := request(s, route.method, route.path, nil, credentials{}, "http://example.com"); w.Code != 401 {
			t.Fatalf("unprotected %s: %d", route.path, w.Code)
		}
	}
	auth := login(t, s)
	if w := request(s, "GET", "/api/v1/ipmi", nil, auth, ""); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"enabled":false`)) {
		t.Fatalf("disabled snapshot %d %s", w.Code, w.Body)
	}
	if w := request(s, "POST", "/api/v1/ipmi/target", []byte(`{"fan":3,"duty":35}`), auth, "http://example.com"); w.Code != 503 {
		t.Fatal(w.Code)
	}
	f := &fakeSystemFans{}
	s.options.IPMI = f
	for _, authCase := range []credentials{{cookie: auth.cookie}, {cookie: auth.cookie, csrf: "wrong"}} {
		if w := request(s, "POST", "/api/v1/ipmi/target", []byte(`{"fan":3,"duty":35}`), authCase, "http://example.com"); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	if w := request(s, "POST", "/api/v1/ipmi/default", nil, auth, "http://attacker.example"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if f.calls != 0 {
		t.Fatal("untrusted request reached IPMI worker")
	}
}
func TestIPMITargetValidationAndRestorationOutcomes(t *testing.T) {
	s, _ := testServer(t)
	auth := login(t, s)
	f := &fakeSystemFans{result: ipmi.Result{Restored: true, Persisted: false, StorageError: "disk full"}}
	s.options.IPMI = f
	for _, body := range []string{`{"fan":0,"duty":35}`, `{"fan":8,"duty":35}`, `{"fan":3,"duty":0}`, `{"fan":3,"duty":101}`, `{"fan":3,"duty":35,"raw":"0x01"}`, `{"fan":3,"duty":35} {}`} {
		if w := request(s, "POST", "/api/v1/ipmi/target", []byte(body), auth, "http://example.com"); w.Code != 400 {
			t.Fatalf("invalid target accepted: %s %d", body, w.Code)
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid target reached worker")
	}
	if w := request(s, "POST", "/api/v1/ipmi/target", []byte(`{"fan":3,"duty":35}`), auth, "http://example.com"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	w := request(s, "POST", "/api/v1/ipmi/default", nil, auth, "http://example.com")
	var got ipmi.Result
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || !got.Restored || got.Persisted || got.StorageError == "" {
		t.Fatalf("outcomes lost: %s", w.Body)
	}
	f.err = ipmi.ErrInvalid
	if w := request(s, "POST", "/api/v1/ipmi/target", []byte(`{"fan":3,"duty":35}`), auth, "http://example.com"); w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestIPMIConfigHasIndependentRevisions(t *testing.T) {
	s, gpuService := testServer(t)
	auth := login(t, s)
	f := &fakeSystemFans{cfg: ipmi.Settings{Revision: 7}}
	s.options.IPMI = f
	put := func(rev string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "http://example.com/api/v1/ipmi/config", bytes.NewReader(body))
		r.AddCookie(auth.cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://example.com")
		r.Header.Set("X-CSRF-Token", auth.csrf)
		r.Header.Set("If-Match", rev)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	body, _ := json.Marshal(f.cfg)
	for _, tc := range []struct {
		etag   string
		status int
	}{{"", 428}, {`W/"7"`, 400}, {`"8"`, 400}, {`"7"`, 200}, {`"7"`, 412}} {
		w := put(tc.etag, body)
		if w.Code != tc.status {
			t.Fatalf("etag %q got %d %s", tc.etag, w.Code, w.Body)
		}
		if w.Code == 412 && w.Header().Get("ETag") != `"8"` {
			t.Fatal("conflict returned GPU revision")
		}
	}
	if gpuService.updateCalls != 0 || gpuService.cfg.Revision != 1 {
		t.Fatal("IPMI edit touched GPU configuration")
	}
}
