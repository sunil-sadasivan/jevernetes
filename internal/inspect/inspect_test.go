package inspect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/sunil-sadasivan/jevernetes/internal/controller"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/kube"
)

func store(t *testing.T) *controller.Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := controller.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestServerHTTPBoundaries(t *testing.T) {
	s := store(t)
	h := Handler(s, NewState())
	for _, test := range []struct {
		method, path string
		code         int
	}{{"GET", "/v1/status", 200}, {"POST", "/v1/status", 400}, {"GET", "/v1/status?x=y", 404}, {"GET", "/v1/incidents/../status", 404}, {"GET", "/v1/incidents/nope", 404}, {"GET", "/v1/incidents/" + strings.Repeat("a", 64), 404}} {
		r := httptest.NewRequest(test.method, test.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.code {
			t.Fatalf("%s got %d wanted %d", test.path, w.Code, test.code)
		}
	}
	r := httptest.NewRequest("GET", "/v1/status", nil)
	r.Header.Set("X-Large", strings.Repeat("x", 1100))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("oversized header")
	}
}
func TestHealthMetricsAndReadiness(t *testing.T) {
	state := NewState()
	h := Health(state)
	for _, ready := range []bool{false, true} {
		state.Ready.Store(ready)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
		if (w.Code == 200) != ready {
			t.Fatal("readiness")
		}
	}
	state.Add("received", 2)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), "jevernetes_received 2") {
		t.Fatal("metrics")
	}
}

type fakeTunnel struct {
	url    string
	closed bool
}

func (t *fakeTunnel) BaseURL() string { return t.url }
func (t *fakeTunnel) Close() error    { t.closed = true; return nil }

type fakeForward struct {
	t       *fakeTunnel
	ns, pod string
	port    int
}

func (f *fakeForward) Open(_ context.Context, ns, pod string, port int) (kube.Tunnel, error) {
	f.ns = ns
	f.pod = pod
	f.port = port
	return f.t, nil
}
func TestRemoteTunnelLifecycleAndValidation(t *testing.T) {
	s := store(t)
	handler := Handler(s, NewState())
	pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "demo"}, Status: v1.PodStatus{Phase: v1.PodRunning}}
	api := fake.NewClientset(pod)
	tunnel := &fakeTunnel{url: "http://127.0.0.1:12345"}
	forward := &fakeForward{t: tunnel}
	c := Client{http: &http.Client{Transport: handlerTransport{handler}}, API: api.CoreV1(), Forward: forward, Port: 9091, Config: kube.Config{Namespace: "demo", Pod: "one", MaxBytes: 1024, MaxStreams: 1}}
	e, err := c.Sample(context.Background(), "")
	if err != nil || e.Target.Pod != "one" || !tunnel.closed || forward.port != 9091 {
		t.Fatal(e, err)
	}
	if _, err = c.Sample(context.Background(), "invalid/id"); err == nil {
		t.Fatal("invalid id reached transport")
	}
}
func TestRemoteRejectsOversizeAndMalformedSchema(t *testing.T) {
	valid := controller.Status{Schema: 1, Sampled: time.Now().Unix(), Recent: []controller.Incident{}, Metrics: map[string]uint64{}}
	raw, _ := json.Marshal(valid)
	if _, err := Validate(raw, ""); err != nil {
		t.Fatal(err)
	}
	valid.Complete = true
	raw, _ = json.Marshal(valid)
	if _, err := Validate(raw, ""); err == nil {
		t.Fatal("complete claim accepted")
	}
	if _, err := Validate([]byte(strings.Repeat("x", MaxResponse+1)), ""); err == nil {
		t.Fatal("oversize")
	}
	if _, err := Validate([]byte(`{"schema":1,"schema":1}`), ""); err == nil {
		t.Fatal("duplicate keys")
	}
}

type handlerTransport struct{ handler http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.handler.ServeHTTP(w, r)
	return w.Result(), nil
}
func TestInspectionRejectsHTML(t *testing.T) {
	if _, err := Validate([]byte(`<html>redirect</html>`), ""); err == nil {
		t.Fatal("HTML accepted")
	}
}
func TestDetailValidatesTypedMetadataAndErrors(t *testing.T) {
	id := strings.Repeat("a", 64)
	detail := controller.Detail{Schema: 1, Incident: controller.Incident{ID: id}, Notification: &controller.Notification{Schema: 2, ID: strings.Repeat("b", 64), IncidentID: id, EventID: strings.Repeat("c", 20), SourceID: strings.Repeat("d", 64), Decision: "review", Judgment: event.Unknown("analysis cancelled")}}
	raw, _ := json.Marshal(detail)
	if _, err := Validate(raw, id); err != nil {
		t.Fatal(err)
	}
	detail.Notification.Judgment.Category = "arbitrary text"
	raw, _ = json.Marshal(detail)
	if _, err := Validate(raw, id); err == nil {
		t.Fatal("untyped judgment accepted")
	}
	if _, err := Validate([]byte(`{"schema":1}`), ""); err == nil {
		t.Fatal("missing required fields accepted")
	}
}
