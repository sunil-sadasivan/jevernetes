// Package dashboard is a read-only local UI. Targets are operator-configured,
// never supplied by the browser; there are no browser-triggered provider calls.
package dashboard

import (
	"context"
	"embed"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/controller"
	"github.com/sunil-sadasivan/jevernetes/internal/inspect"
)

//go:embed web/*
var assets embed.FS

type Sampler interface {
	Sample(context.Context, string) (inspect.Envelope, error)
}
type cache struct {
	value inspect.Envelope
	at    time.Time
	id    string
}
type Dashboard struct {
	Remote                       Sampler
	Report                       func() any
	Interval                     time.Duration
	mu                           sync.Mutex
	busy                         bool
	status, detail               cache
	attemptStatus, attemptDetail time.Time
}

func (d *Dashboard) sample(ctx context.Context, id string) (inspect.Envelope, int) {
	d.mu.Lock()
	now := time.Now()
	entry := d.status
	last := d.attemptStatus
	if id != "" {
		entry = d.detail
		last = d.attemptDetail
	}
	interval := max(d.Interval, 2*time.Second)
	if entry.id == id && !entry.at.IsZero() && now.Sub(entry.at) < interval {
		d.mu.Unlock()
		return entry.value, 200
	}
	if d.busy || now.Sub(last) < 2*time.Second {
		d.mu.Unlock()
		return inspect.Envelope{}, 429
	}
	d.busy = true
	if id == "" {
		d.attemptStatus = now
	} else {
		d.attemptDetail = now
	}
	d.mu.Unlock()
	bounded, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	v, err := d.Remote.Sample(bounded, id)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.busy = false
	if err != nil {
		d.detail = cache{}
		d.status = cache{}
		return inspect.Envelope{}, 503
	}
	entry = cache{v, time.Now(), id}
	if id == "" {
		d.status = entry
	} else {
		d.detail = entry
	}
	return v, 200
}
func (d *Dashboard) Handler(host string) http.Handler {
	files, _ := fs.Sub(assets, "web")
	static := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if r.Host != host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+host) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			inspect.JSON(w, 403, map[string]string{"error": "local origin required"})
			return
		}
		if r.Method != "GET" || r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
			inspect.JSON(w, 405, map[string]string{"error": "read-only dashboard"})
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/api/report":
			if d.Report == nil {
				inspect.JSON(w, 200, map[string]any{"events": []any{}, "summary": map[string]int{"events": 0}})
			} else { // Reports can exceed the inspection cap; dashboard only exposes a bounded view.
				inspect.JSON(w, 200, d.Report())
			}
			return
		case "/api/controller/config":
			inspect.JSON(w, 200, map[string]any{"enabled": d.Remote != nil, "interval_seconds": int(max(d.Interval, 2*time.Second).Seconds())})
			return
		case "/", "/index.html", "/app.js", "/style.css":
			static.ServeHTTP(w, r)
			return
		}
		id := ""
		if r.URL.Path != "/api/controller/status" {
			prefix := "/api/controller/incidents/"
			if !strings.HasPrefix(r.URL.Path, prefix) {
				inspect.JSON(w, 501, map[string]string{"error": "dashboard operation unsupported in Go runtime"})
				return
			}
			id = strings.TrimPrefix(r.URL.Path, prefix)
			if !controller.ValidID(id) {
				http.NotFound(w, r)
				return
			}
		}
		if d.Remote == nil {
			inspect.JSON(w, 503, map[string]string{"error": "controller inspection not configured"})
			return
		}
		value, code := d.sample(r.Context(), id)
		if code != 200 {
			inspect.JSON(w, code, map[string]any{"error": "controller sample unavailable", "stale": true})
			return
		}
		inspect.JSON(w, 200, value)
	})
}
func (d *Dashboard) Serve(ctx context.Context, l net.Listener) error {
	return inspect.ServeWithWriteTimeout(ctx, l, d.Handler(l.Addr().String()), true, 30*time.Second)
}
