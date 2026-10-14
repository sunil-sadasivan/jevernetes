// Package investigation serves the investigation UI entirely in the Go process.
package investigation

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
	"github.com/sunil-sadasivan/jevernetes/internal/report"
	"github.com/sunil-sadasivan/jevernetes/internal/search"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

//go:embed web/*
var assets embed.FS

// Scan receives only validated upload paths created by this server. It must join
// collection and analysis before returning, including on cancellation.
type Scan func(context.Context, object, []string, func(object)) (object, error)
type ContextFetch func(context.Context, event.Event, int) (object, error)
type Server struct {
	ctx                    context.Context
	cancel                 context.CancelFunc
	wg                     sync.WaitGroup
	mu                     sync.Mutex
	root, rulesPath, token string
	job, live              object
	stop                   context.CancelFunc
	search                 *search.Run
	stopSearch             context.CancelFunc
	cache                  search.Cache
	Scan                   Scan
	Context                ContextFetch
	contextSlots           chan struct{}
	initial                object
	initialID              string
	offline                bool
}

func New(ctx context.Context, root string, offline bool) (*Server, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.New("report directory unavailable")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("report directory must be a regular directory")
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Server{ctx: ctx, cancel: cancel, root: root, rulesPath: filepath.Join(root, ".review-rules.json"), token: hex.EncodeToString(b), contextSlots: make(chan struct{}, 2), offline: offline}, nil
}
func (s *Server) Close() { s.cancel(); s.wg.Wait() }
func (s *Server) Import(path string) error {
	v, err := readReport(s.ctx, path)
	if err != nil {
		return err
	}
	s.initial = v
	s.initialID = filepath.Base(path)
	return nil
}
func (s *Server) reportLocked(id string) (object, error) {
	if id == "" || filepath.Base(id) != id || strings.HasPrefix(id, ".") || !strings.HasSuffix(id, ".json") {
		return nil, errors.New("invalid report id")
	}
	if id == s.initialID {
		return clone(s.initial), nil
	}
	return readReport(s.ctx, filepath.Join(s.root, filepath.Base(id)))
}
func (s *Server) sourceLocked(p object) (object, string, error) {
	liveID, reportID := str(p, "live_id"), str(p, "report_id")
	if liveID != "" && reportID != "" {
		return nil, "", errors.New("choose one report")
	}
	if liveID != "" {
		if s.job == nil || str(s.job, "id") != liveID || s.live == nil {
			return nil, "", errors.New("live session changed")
		}
		return clone(s.live), liveID + ".json", nil
	}
	v, err := s.reportLocked(reportID)
	return v, reportID, err
}
func (s *Server) source(p object) (object, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, key, err := s.sourceLocked(p)
	if err != nil {
		return nil, "", err
	}
	rules, err := s.rules()
	if err != nil {
		return nil, "", err
	}
	return project(v, key, rules), key, nil
}
func (s *Server) state() (object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rules, err := s.rules()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, errors.New("report directory unavailable")
	}
	type item struct {
		name string
		at   time.Time
	}
	items := []item{}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			if info, err := e.Info(); err == nil {
				items = append(items, item{e.Name(), info.ModTime()})
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].at.After(items[j].at) })
	if len(items) > 100 {
		items = items[:100]
	}
	if s.initial != nil {
		items = append([]item{{s.initialID, time.Now()}}, items...)
	}
	reports := []any{}
	seen := map[string]bool{}
	for _, item := range items {
		if seen[item.name] {
			continue
		}
		seen[item.name] = true
		v, err := s.reportLocked(item.name)
		if err != nil {
			continue
		}
		v = project(v, item.name, rules)
		reports = append(reports, object{"id": item.name, "created_at": v["created_at"], "mode": v["mode"], "scope": v["scope"], "summary": v["summary"]})
	}
	available := false
	if !s.offline {
		key, _ := provider.CredentialContext(s.ctx, "typesafe", os.Getenv)
		available = key != ""
	}
	var job any
	if s.job != nil {
		job = clone(s.job)
	}
	return object{"csrf": s.token, "jev_available": available, "job": job, "reports": reports}, nil
}
func jsonResponse(w http.ResponseWriter, code int, value any) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 128<<20 {
		code = 413
		raw = []byte(`{"error":"response exceeds limit"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(raw)
}
func (s *Server) Handler(host string) http.Handler {
	files, _ := fs.Sub(assets, "web")
	static := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if r.Host != host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+host) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			jsonResponse(w, 403, object{"error": "local origin required"})
			return
		}
		if r.URL.RawPath != "" || len(r.URL.RawQuery) > 4096 {
			jsonResponse(w, 400, object{"error": "invalid path"})
			return
		}
		if r.Method != "GET" && r.Method != "POST" {
			jsonResponse(w, 405, object{"error": "method unavailable"})
			return
		}
		var value any
		var err error
		code := 200
		if r.Method == "GET" {
			switch r.URL.Path {
			case "/", "/index.html", "/app.js", "/search.js", "/context.js", "/prompt.js", "/style.css", "/favicon.svg":
				static.ServeHTTP(w, r)
				return
			case "/api/state":
				value, err = s.state()
			case "/api/report":
				value, _, err = s.source(object{"report_id": r.URL.Query().Get("id")})
			case "/api/live":
				s.mu.Lock()
				id := str(s.job, "id")
				s.mu.Unlock()
				value, _, err = s.source(object{"live_id": id})
			case "/api/rules":
				s.mu.Lock()
				var rules object
				rules, err = s.rules()
				s.mu.Unlock()
				if err == nil {
					value = object{"expected": rules["rules"], "baseline": baselineRules}
				}
			case "/api/search":
				offset := 0
				if raw := r.URL.Query().Get("offset"); raw != "" {
					offset, err = strconv.Atoi(raw)
				}
				if err != nil || offset < 0 || offset > 100000 {
					err = errors.New("invalid offset")
					break
				}
				s.mu.Lock()
				run := s.search
				s.mu.Unlock()
				if run == nil || run.ID != r.URL.Query().Get("id") {
					err = errors.New("search expired")
				} else {
					value = run.Snapshot(offset)
				}
			default:
				code = 404
				err = errors.New("not found")
			}
		} else {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Jev-Token")), []byte(s.token)) != 1 {
				jsonResponse(w, 403, object{"error": "request not authorized"})
				return
			}
			if r.Header.Get("Content-Type") != "application/json" {
				jsonResponse(w, 415, object{"error": "JSON required"})
				return
			}
			raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
			var p object
			if e != nil || provider.Strict(raw, &p) != nil || p == nil {
				jsonResponse(w, 400, object{"error": "invalid JSON request"})
				return
			}
			switch r.URL.Path {
			case "/api/analyze":
				value, err = s.start(p)
				code = 202
			case "/api/live/stop":
				s.mu.Lock()
				if s.stop == nil || s.job == nil || str(s.job, "status") != "running" {
					err = errors.New("no active collection")
				} else {
					s.stop()
					s.job["status"] = "stopping"
					value = object{"status": "stopping"}
				}
				s.mu.Unlock()
			case "/api/review":
				err = s.review(p)
				value = object{"status": "saved"}
			case "/api/context":
				value, err = s.fetchContext(r.Context(), p)
			case "/api/search":
				value, err = s.startSearch(p)
				code = 202
			case "/api/search/stop":
				s.mu.Lock()
				if s.search == nil || s.search.ID != str(p, "id") {
					err = errors.New("search expired")
				} else if s.stopSearch != nil {
					s.stopSearch()
				}
				s.mu.Unlock()
				value = object{"status": "stopping"}
			default:
				code = 404
				err = errors.New("not found")
			}
		}
		if err != nil {
			if code != 404 {
				code = 400
			}
			value = object{"error": err.Error()}
		}
		jsonResponse(w, code, value)
	})
}
func (s *Server) start(p object) (object, error) {
	if s.Scan == nil {
		return nil, errors.New("collection unavailable")
	}
	p["_review_rules"] = s.rulesPath
	kind := str(p, "kind")
	if kind != "files" && kind != "kubernetes" {
		return nil, errors.New("choose files or Kubernetes")
	}
	if s.offline {
		p["offline"] = true
	}
	for _, k := range []string{"offline", "grouping", "live"} {
		if v, ok := p[k]; ok {
			if _, ok = v.(bool); !ok {
				return nil, errors.New("invalid scan mode")
			}
		}
	}
	if p["live"] == true && kind != "kubernetes" {
		return nil, errors.New("live requires Kubernetes")
	}
	for k, bounds := range map[string][2]float64{"max_batches": {1, 12500}, "tail": {0, 10000}, "max_streams": {1, 256}, "retain_events": {1, 5000}, "max_cost": {.000001, 100}} {
		if _, ok := p[k]; ok {
			v := number(p, k)
			if v < bounds[0] || v > bounds[1] || (k != "max_cost" && v != float64(int(v))) {
				return nil, errors.New("invalid scan limit")
			}
		}
	}
	for _, k := range []string{"context", "namespace", "selector", "since"} {
		if v, ok := p[k]; ok {
			text, ok := v.(string)
			if !ok || len(text) > 256 || strings.HasPrefix(text, "-") || strings.IndexFunc(text, unicode.IsControl) >= 0 {
				return nil, errors.New("invalid Kubernetes scope")
			}
		}
	}
	type upload struct {
		name string
		data []byte
	}
	uploads := []upload{}
	if kind == "files" {
		files := list(p, "files")
		if len(files) < 1 || len(files) > 20 {
			return nil, errors.New("choose 1 to 20 files")
		}
		total := 0
		for _, raw := range files {
			file, ok := raw.(map[string]any)
			name := str(file, "name")
			data, err := base64.StdEncoding.Strict().DecodeString(str(file, "data"))
			if !ok || err != nil || name == "" || filepath.Base(name) != name || name == "." || name == ".." || len(name) > 180 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
				return nil, errors.New("invalid upload")
			}
			total += len(data)
			if total > 10<<20 {
				return nil, errors.New("uploads exceed 10 MiB")
			}
			uploads = append(uploads, upload{name, data})
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job != nil && (str(s.job, "status") == "running" || str(s.job, "status") == "stopping") {
		return nil, errors.New("a scan is already running")
	}
	id := time.Now().UTC().Format("20060102-150405") + "-" + event.Hash(time.Now().UnixNano())[:12]
	ctx, cancel := context.WithCancel(s.ctx)
	s.stop = cancel
	s.job = object{"id": id, "status": "running", "message": "Starting analysis…", "report_id": nil, "live": p["live"] == true}
	s.live = normalize(object{"schema_version": float64(2), "mode": "offline-rules", "summary": object{}, "events": []any{}, "tail_events": []any{}, "live": object{"status": "running", "message": "Starting collection"}})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		var err error
		var value object
		temp, e := os.MkdirTemp("", "jevernetes-upload-*")
		if e != nil {
			err = e
		} else {
			defer os.RemoveAll(temp)
			paths := []string{}
			names := map[string]string{}
			for i, file := range uploads {
				dir := filepath.Join(temp, strconv.Itoa(i))
				if err = os.Mkdir(dir, 0700); err != nil {
					break
				}
				name := "upload.log"
				if strings.HasSuffix(file.name, ".gz") {
					name += ".gz"
				}
				path := filepath.Join(dir, name)
				if err = os.WriteFile(path, file.data, 0600); err != nil {
					break
				}
				paths = append(paths, path)
				names[path] = file.name
			}
			rename := func(v object) object {
				v = clone(v)
				for _, key := range []string{"events", "tail_events", "coverage", "important_groups"} {
					for _, raw := range list(v, key) {
						if e, ok := raw.(map[string]any); ok {
							src := obj(e, "source")
							if name, ok := names[str(src, "path")]; ok {
								src["path"] = name
							}
						}
					}
				}
				if len(names) > 0 {
					ns := []string{}
					for _, path := range paths {
						ns = append(ns, names[path])
					}
					v["scope"] = object{"files": ns}
				}
				return normalize(v)
			}
			if err == nil {
				p["_source_names"] = names
				value, err = s.Scan(ctx, p, paths, func(v object) { v = rename(v); s.mu.Lock(); s.live = v; s.mu.Unlock() })
				if value != nil {
					value = rename(value)
					writeErr := report.Write(filepath.Join(s.root, id+".json"), value)
					if writeErr != nil {
						err = writeErr
					}
				}
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if value != nil {
			s.live = value
		}
		s.job["status"] = "complete"
		s.job["message"] = "Analysis complete; review coverage and unknown events"
		s.job["report_id"] = id + ".json"
		if err != nil && !errors.Is(err, context.Canceled) {
			s.job["status"] = "error"
			s.job["message"] = "Analysis failed; check collection configuration and provider credentials"
		}
		s.stop = nil
	}()
	return object{"id": id}, nil
}
func (s *Server) startSearch(p object) (object, error) {
	v, _, err := s.source(p)
	if err != nil {
		return nil, err
	}
	query, mode := str(p, "query"), str(p, "mode")
	if mode == "" {
		mode = "jev"
	}
	if s.offline && mode != "literal" {
		return nil, errors.New("dashboard is offline; use exact text filtering")
	}
	budget := 16
	if p["max_batches"] != nil {
		n := number(p, "max_batches")
		if n != float64(int(n)) {
			return nil, errors.New("invalid search budget")
		}
		budget = int(n)
	}
	scope := obj(p, "scope")
	for k, value := range scope {
		text, ok := value.(string)
		if !ok || len(text) > 1000 || (k != "namespace" && k != "pod" && k != "container") {
			return nil, errors.New("invalid source filter")
		}
	}
	source := str(p, "source")
	events := []event.Event{}
	selected := []any{}
	seen := map[string]bool{}
	for _, collection := range []string{"events", "tail_events"} {
		for _, raw := range list(v, collection) {
			e, ok := raw.(map[string]any)
			if !ok || seen[str(e, "id")] {
				continue
			}
			seen[str(e, "id")] = true
			src := obj(e, "source")
			matches := true
			for k, value := range scope {
				if value != "" && src[k] != value {
					matches = false
				}
			}
			name := str(src, "path")
			if str(src, "type") == "kubernetes" {
				name = str(src, "namespace") + "/" + str(src, "pod") + "/" + str(src, "container")
			}
			if source != "" && source != "all" && source != name {
				matches = false
			}
			if matches {
				data, _ := json.Marshal(e)
				var typed event.Event
				if json.Unmarshal(data, &typed) == nil {
					events = append(events, typed)
					selected = append(selected, e)
				}
			}
		}
	}
	if len(events) == 0 {
		return nil, errors.New("no retained events in these filters")
	}
	run, err := search.New(events, query, mode, budget, &s.cache)
	if err != nil {
		return nil, err
	}
	var client search.Client
	if mode == "jev" {
		key, err := provider.CredentialContext(s.ctx, "typesafe", os.Getenv)
		if err != nil {
			return nil, err
		}
		client, err = provider.New(provider.Config{Kind: "typesafe", Model: "jev-latest", InputPrice: .042, MaxCost: .01, MaxAttempts: budget * 3, MaxBatches: budget}, key)
		if err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.search != nil && s.search.Snapshot(0)["status"] == "running" {
		return nil, errors.New("a search is already running")
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.search = run
	s.stopSearch = cancel
	s.wg.Add(1)
	go func() { defer s.wg.Done(); defer cancel(); run.Execute(ctx, client) }()
	v["events"] = selected
	v["tail_events"] = []any{}
	return object{"id": run.ID, "report": v}, nil
}
func (s *Server) fetchContext(ctx context.Context, p object) (object, error) {
	if s.Context == nil {
		return nil, errors.New("context unavailable")
	}
	n := number(p, "window_seconds")
	if n != 30 && n != 120 && n != 300 && n != 900 {
		return nil, errors.New("choose a context window of 30, 120, 300 or 900 seconds")
	}
	v, _, err := s.source(p)
	if err != nil {
		return nil, err
	}
	var selected event.Event
	for _, collection := range []string{"events", "tail_events"} {
		for _, raw := range list(v, collection) {
			e, ok := raw.(map[string]any)
			if ok && str(e, "id") == str(p, "event_id") {
				b, _ := json.Marshal(e)
				_ = json.Unmarshal(b, &selected)
			}
		}
	}
	if selected.ID == "" {
		return nil, errors.New("selected event is unavailable")
	}
	select {
	case s.contextSlots <- struct{}{}:
		defer func() { <-s.contextSlots }()
	default:
		return nil, errors.New("context lookups busy")
	}
	return s.Context(ctx, selected, int(n))
}

var baselineRules = []object{{"id": "errors", "name": "Errors and failures", "description": "Errors, fatal failures, panics, exceptions and HTTP 5xx responses require review."}, {"id": "warnings", "name": "Warnings", "description": "Warnings remain visible for operational review."}}
