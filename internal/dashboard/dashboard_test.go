package dashboard

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/inspect"
)

type sampler struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

func (s *sampler) Sample(context.Context, string) (inspect.Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.fail {
		return inspect.Envelope{}, errors.New("private fixture")
	}
	return inspect.Envelope{Sampled: 1, Controller: map[string]any{"schema": 1}}, nil
}
func TestDashboardHostOriginAndMethods(t *testing.T) {
	d := Dashboard{}
	h := d.Handler("127.0.0.1:8765")
	for _, test := range []struct {
		host, origin, method string
		want                 int
	}{{"evil.invalid", "", "GET", 403}, {"127.0.0.1:8765", "http://evil.invalid", "GET", 403}, {"127.0.0.1:8765", "", "POST", 405}, {"127.0.0.1:8765", "", "GET", 200}} {
		r := httptest.NewRequest(test.method, "http://"+test.host+"/api/controller/config", nil)
		r.Header.Set("Origin", test.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Fatal(w.Code, test.want)
		}
	}
}
func TestFixedRoutesAndCaching(t *testing.T) {
	s := &sampler{}
	d := Dashboard{Remote: s, Interval: 5 * time.Second}
	h := d.Handler("127.0.0.1:8765")
	for _, path := range []string{"/api/controller/status", "/api/controller/status"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8765"+path, nil))
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if s.calls != 1 {
		t.Fatal("cache missing")
	}
	for _, path := range []string{"/api/controller/status?url=https://example.invalid", "/api/controller/incidents/invalid"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8765"+path, nil))
		if w.Code != 404 {
			t.Fatal(w.Code)
		}
	}
	if s.calls != 1 {
		t.Fatal("browser selected target")
	}
}
func TestFailureClearsCachedState(t *testing.T) {
	s := &sampler{fail: true}
	d := Dashboard{Remote: s}
	_, code := d.sample(context.Background(), "")
	if code != 503 || !d.status.at.IsZero() {
		t.Fatal("failure not stale")
	}
	_, code = d.sample(context.Background(), "")
	if code != 429 {
		t.Fatal("uncached attempts not throttled")
	}
}
func TestDashboardReadOnlyCSP(t *testing.T) {
	d := Dashboard{}
	w := httptest.NewRecorder()
	d.Handler("127.0.0.1:8765").ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8765/", nil))
	if w.Code != 200 || w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing static security headers")
	}
}
