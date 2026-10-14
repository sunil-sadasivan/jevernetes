package investigation

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/report"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixture() object {
	return object{"schema_version": 2, "mode": "offline-rules", "summary": object{"events": 2, "important": 2, "routine": 0, "unknown": 0, "uncertain": 0}, "events": []any{object{"id": "a", "text": "ERROR database unavailable", "importance": "important", "severity": "error", "category": "availability", "source": object{"type": "file", "path": "synthetic.log"}}, object{"id": "b", "text": "ERROR database unavailable", "importance": "important", "source": object{"type": "file", "path": "other.log"}}}, "coverage": []any{}}
}
func setup(t *testing.T) *Server {
	t.Helper()
	s, err := New(context.Background(), t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := report.Write(filepath.Join(s.root, "test.json"), fixture()); err != nil {
		t.Fatal(err)
	}
	return s
}
func request(s *Server, method, path string, p object, token string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(p)
	r := httptest.NewRequest(method, "http://127.0.0.1:8792"+path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Jev-Token", token)
	w := httptest.NewRecorder()
	s.Handler("127.0.0.1:8792").ServeHTTP(w, r)
	return w
}
func TestHTTPReviewReversibleAtomicAndScope(t *testing.T) {
	s := setup(t)
	p := object{"report_id": "test.json", "event_id": "a", "action": "expected", "pattern": "database unavailable", "scope": "workload"}
	if w := request(s, "POST", "/api/review", p, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request(s, "POST", "/api/review", p, s.token); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	v, _, err := s.source(object{"report_id": "test.json"})
	if err != nil {
		t.Fatal(err)
	}
	events := list(v, "events")
	if str(events[0].(object), "importance") != "routine" || str(events[1].(object), "importance") != "important" {
		t.Fatal("scope escaped", events)
	}
	rules, _ := s.rules()
	id := str(list(rules, "rules")[0].(object), "id")
	if w := request(s, "POST", "/api/review", object{"action": "toggle", "rule_id": id, "enabled": false}, s.token); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	v, _, _ = s.source(object{"report_id": "test.json"})
	if str(list(v, "events")[0].(object), "importance") != "important" {
		t.Fatal("original not restored")
	}
	before, _ := os.ReadFile(s.rulesPath)
	bad := object{"report_id": "test.json", "action": "acknowledge", "events": []any{object{"event_id": "a"}, object{"event_id": "missing"}}}
	if w := request(s, "POST", "/api/review", bad, s.token); w.Code != 400 {
		t.Fatal(w.Code)
	}
	after, _ := os.ReadFile(s.rulesPath)
	if !bytes.Equal(before, after) {
		t.Fatal("bulk review partially committed")
	}
	info, _ := os.Stat(s.rulesPath)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}
func TestSearchAndOriginBoundaries(t *testing.T) {
	s := setup(t)
	w := request(s, "POST", "/api/search", object{"report_id": "test.json", "query": "database", "mode": "literal"}, s.token)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	var start object
	_ = json.Unmarshal(w.Body.Bytes(), &start)
	deadline := time.Now().Add(time.Second)
	for {
		w = request(s, "GET", "/api/search?id="+str(start, "id"), nil, "")
		var v object
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		if str(v, "status") == "complete" {
			if number(v, "matching_instances") != 2 {
				t.Fatal(v)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("search stalled")
		}
		time.Sleep(time.Millisecond)
	}
	r := httptest.NewRequest("GET", "http://evil.invalid/api/state", nil)
	w = httptest.NewRecorder()
	s.Handler("127.0.0.1:8792").ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	w = request(s, "GET", "/api/report?id=../test.json", nil, "")
	if w.Code != 400 {
		t.Fatal("traversal accepted")
	}
}
func TestScanOwnedAndSaved(t *testing.T) {
	s := setup(t)
	started := make(chan struct{})
	s.Scan = func(ctx context.Context, p object, paths []string, update func(object)) (object, error) {
		close(started)
		<-ctx.Done()
		return fixture(), ctx.Err()
	}
	w := request(s, "POST", "/api/analyze", object{"kind": "kubernetes", "offline": true, "live": true}, s.token)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	<-started
	s.Close()
	state, err := s.state()
	if err != nil {
		t.Fatal(err)
	}
	job := obj(state, "job")
	if str(job, "status") != "complete" {
		t.Fatal(job)
	}
	if _, err = os.Stat(filepath.Join(s.root, str(job, "report_id"))); err != nil {
		t.Fatal("report not saved", err)
	}
}
func TestHumanRuleSkipsProviderWithUnknownRestorableBaseline(t *testing.T) {
	s := setup(t)
	if err := s.review(object{"report_id": "test.json", "event_id": "a", "action": "expected", "pattern": "database unavailable"}); err != nil {
		t.Fatal(err)
	}
	apply, err := Reviewer(context.Background(), s.rulesPath)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := apply(event.Event{ID: "future", Text: "ERROR database unavailable", Source: event.Source{"type": "file", "path": "synthetic.log"}})
	if !ok || e.Importance != "routine" || e.OriginalJudgment == nil || e.OriginalJudgment.Importance != "unknown" {
		t.Fatal(e, ok)
	}
	_, ok = apply(event.Event{ID: "other", Text: "ERROR database unavailable", Source: event.Source{"type": "file", "path": "other.log"}})
	if ok {
		t.Fatal("scope escaped")
	}
}
func TestLegacyRuleExtensionScopesAndConflictingIdentities(t *testing.T) {
	s := setup(t)
	rule := object{"id": "legacy", "pattern": "database", "scope": object{"type": "kubernetes", "restart_count": float64(1), "previous": true, "extension": "literal*"}, "enabled": false}
	value := object{"version": 1, "rules": []any{rule, rule}, "acknowledged": object{"test.json": []string{"a"}}}
	if err := report.Write(s.rulesPath, value); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.rules()
	if err != nil || len(list(loaded, "rules")) != 2 {
		t.Fatal("legacy scopes or identical duplicates lost", err)
	}
	other := clone(rule)
	other["enabled"] = true
	value["rules"] = []any{rule, other}
	_ = report.Write(s.rulesPath, value)
	if _, err = s.rules(); err == nil {
		t.Fatal("conflicting identities accepted")
	}
}

func TestUploadNamesNeverSelectDiskPaths(t *testing.T) {
	s := setup(t)
	observed := make(chan string, 1)
	s.Scan = func(ctx context.Context, p object, paths []string, update func(object)) (object, error) {
		if len(paths) != 2 || filepath.Base(paths[0]) != "upload.log" || filepath.Base(paths[1]) != "upload.log.gz" {
			observed <- "untrusted disk name"
			return fixture(), nil
		}
		names, ok := p["_source_names"].(map[string]string)
		if !ok || names[paths[0]] != "customer.log" || names[paths[1]] != "customer.gz" {
			observed <- "lost display names"
			return fixture(), nil
		}
		observed <- "ok"
		return fixture(), nil
	}
	bad := request(s, "POST", "/api/analyze", object{"kind": "files", "files": []any{object{"name": "../escape", "data": ""}}}, s.token)
	if bad.Code != 400 {
		t.Fatal("accepted traversal")
	}
	got := request(s, "POST", "/api/analyze", object{"kind": "files", "_source_names": object{"bad": "/private/path"}, "files": []any{object{"name": "customer.log", "data": ""}, object{"name": "customer.gz", "data": ""}}}, s.token)
	if got.Code != 202 {
		t.Fatal(got.Body.String())
	}
	if result := <-observed; result != "ok" {
		t.Fatal(result)
	}
}

func TestReportLookupOnlyUsesRegularDirectoryEntries(t *testing.T) {
	s := setup(t)
	if _, err := s.reportLocked("test.json"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.root, "test.json"), filepath.Join(s.root, "alias.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.root, "directory.json"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alias.json", "directory.json", "missing.json", "../test.json", "/test.json"} {
		if _, err := s.reportLocked(name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}
