// Package inspect serves and validates the bounded loopback inspection protocol.
package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/net/netutil"

	"github.com/sunil-sadasivan/jevernetes/internal/controller"
)

const MaxResponse = 256 << 10

// State protects shared counters independently of the durable writer.
type State struct {
	Ready   atomic.Bool
	Stopped atomic.Bool
	mu      sync.Mutex
	metrics map[string]uint64
}

func NewState() *State                  { return &State{metrics: map[string]uint64{}} }
func (s *State) Add(k string, n uint64) { s.mu.Lock(); s.metrics[k] += n; s.mu.Unlock() }
func (s *State) Merge(m map[string]uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range m {
		s.metrics[k] = v
	}
}
func (s *State) Metrics() map[string]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]uint64{}
	for k, v := range s.metrics {
		m[k] = v
	}
	if s.Stopped.Load() {
		m["collection_stopped"] = 1
	}
	return m
}
func JSON(w http.ResponseWriter, code int, value any) {
	b, err := json.Marshal(value)
	if err != nil || len(b) > MaxResponse {
		code = 503
		b = []byte(`{"error":"response unavailable"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}
func Handler(store *controller.Store, state *State) http.Handler {
	gate := make(chan struct{}, 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
			JSON(w, 400, map[string]string{"error": "invalid request"})
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" {
			http.NotFound(w, r)
			return
		}
		headerBytes := len(r.RequestURI) + len(r.Host)
		for k, vs := range r.Header {
			for _, v := range vs {
				headerBytes += len(k) + len(v) + 4
			}
		}
		if headerBytes > 1024 {
			JSON(w, 400, map[string]string{"error": "request exceeds limit"})
			return
		}
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		default:
			JSON(w, 503, map[string]string{"error": "inspection busy"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		switch r.URL.Path {
		case "/v1/status", "/v1/incidents":
			s, err := store.Status(ctx)
			if err != nil {
				JSON(w, 503, map[string]string{"error": "inspection unavailable"})
				return
			}
			s.Sampled = time.Now().Unix()
			s.Ready = state.Ready.Load()
			s.Metrics = state.Metrics()
			if r.URL.Path == "/v1/incidents" {
				JSON(w, 200, map[string]any{"schema": 1, "recent_incidents": s.Recent})
			} else {
				JSON(w, 200, s)
			}
		default:
			id := strings.TrimPrefix(r.URL.Path, "/v1/incidents/")
			if !strings.HasPrefix(r.URL.Path, "/v1/incidents/") || !controller.ValidID(id) {
				http.NotFound(w, r)
				return
			}
			d, err := store.Detail(ctx, strings.ToLower(id))
			if err != nil {
				JSON(w, 503, map[string]string{"error": "inspection unavailable"})
				return
			}
			if d == nil {
				http.NotFound(w, r)
				return
			}
			JSON(w, 200, d)
		}
	})
}
func Health(state *State) http.Handler {
	registry := prometheus.NewRegistry()
	for _, name := range []string{"received", "dropped", "notifications_enqueued", "collection_stopped", "provider_batches", "coverage_gaps", "truncated_events", "parse_uncertain", "outbox_backpressure", "state_seen_evicted", "state_incidents_evicted", "state_verdicts_evicted", "state_outbox_evicted", "coverage_cursor_overflow", "coverage_cursor_untimestamped", "coverage_cursor_truncated", "coverage_cursor_out_of_order", "coverage_stream_reconnect", "coverage_identity_changed", "coverage_stream_capacity"} {
		name := name
		registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "jevernetes_" + name, Help: "Current process " + name}, func() float64 { return float64(state.Metrics()[name]) }))
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{MaxRequestsInFlight: 2, Timeout: 2 * time.Second}))
	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		w.WriteHeader(200)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		if !state.Ready.Load() {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	})
	return mux
}
func Serve(ctx context.Context, listener net.Listener, handler http.Handler, loopback bool) error {
	return ServeWithWriteTimeout(ctx, listener, handler, loopback, 2*time.Second)
}
func ServeWithWriteTimeout(ctx context.Context, listener net.Listener, handler http.Handler, loopback bool, writeTimeout time.Duration) error {
	if writeTimeout < time.Second || writeTimeout > 30*time.Second {
		listener.Close()
		return errors.New("invalid HTTP timeout")
	}

	if loopback {
		addr, ok := listener.Addr().(*net.TCPAddr)
		if !ok || !addr.IP.IsLoopback() {
			listener.Close()
			return errors.New("inspection requires loopback listener")
		}
	}
	if loopback {
		listener = netutil.LimitListener(listener, 1)
	} else {
		listener = netutil.LimitListener(listener, 16)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: writeTimeout, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 1024, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
		case <-done:
		}
	}()
	err := srv.Serve(listener)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return errors.New("HTTP service failed")
}
