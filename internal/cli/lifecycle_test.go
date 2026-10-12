package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/controller"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/kube"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

type delayedJudge struct {
	started, release chan struct{}
	calls            int
	limit            int
}

func (j *delayedJudge) Judge(ctx context.Context, ds []provider.Dossier) ([]event.Judgment, error) {
	j.calls++
	if j.started != nil {
		close(j.started)
		j.started = nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-j.release:
	}
	out := make([]event.Judgment, len(ds))
	for i := range out {
		out[i] = event.Judgment{Importance: "important", Severity: "degraded", Category: "dependency", ImportanceConfidence: event.Confidence(.95), SeverityConfidence: event.Confidence(.95), CategoryConfidence: event.Confidence(.95)}
	}
	return out, nil
}
func (j *delayedJudge) Usage() provider.Usage {
	return provider.Usage{Batches: j.calls, Attempts: j.calls, Complete: true}
}
func providerDeps(j *delayedJudge) runtimeDeps {
	return runtimeDeps{provider: func(provider.Config, string) (riskProvider, error) { return j, nil }}
}

type observedInput struct {
	*io.PipeReader
	closed chan struct{}
	once   sync.Once
}

func (r *observedInput) Close() error {
	err := r.PipeReader.Close()
	r.once.Do(func() { close(r.closed) })
	return err
}

func TestMaxEventsDrainsOnlineAnalysisBeforeAndDuringProviderCall(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "synthetic")
	for _, timing := range []string{"before", "during"} {
		t.Run(timing, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			j := &delayedJudge{started: started, release: release}
			var out, stderr bytes.Buffer
			r, w := io.Pipe()
			input := &observedInput{PipeReader: r, closed: make(chan struct{})}
			defer w.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := newCommand(input, &out, &stderr, providerDeps(j))
			cmd.SetArgs([]string{"files", "-", "--json", "--batch-size", "1", "--max-events", "2"})
			done := make(chan error, 1)
			go func() { done <- cmd.ExecuteContext(ctx) }()
			if timing == "during" {
				_, _ = io.WriteString(w, "ERROR first\nERROR second\n")
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("provider not entered")
				}
				_, _ = io.WriteString(w, "ERROR third\n")
			} else {
				_, _ = io.WriteString(w, "ERROR first\nERROR second\nERROR third\n")
			}
			// The third header finalizes the second event; no third event is
			// finalized. Keep the writer open and the provider blocked until the
			// Nth admission closes intake, without needing N+1 or EOF.
			select {
			case <-input.closed:
			case <-ctx.Done():
				t.Fatal("exact cap did not stop open input")
			}
			close(release)
			w.Close()
			err := <-done
			var partial exitError
			if !errors.As(err, &partial) || partial.code != 2 {
				t.Fatal(err, stderr.String())
			}
			var value struct{ Events []event.Event }
			if err := json.Unmarshal(out.Bytes(), &value); err != nil {
				t.Fatal(err)
			}
			if len(value.Events) != 2 || j.calls != 2 {
				t.Fatal(len(value.Events), j.calls)
			}
			for _, e := range value.Events {
				if e.Importance != "important" {
					t.Fatal("admitted judgment cancelled", e)
				}
			}
		})
	}
}

type quietCollector struct {
	count     int
	stopped   chan struct{}
	delivered <-chan struct{}
}

func (quietCollector) Snapshot(context.Context, func(event.Event) error, func(event.Source, string)) error {
	return errors.New("unexpected snapshot")
}

func (c quietCollector) Follow(ctx context.Context, emit, finalize func(event.Event) error, _ func(event.Source, string)) error {
	if c.stopped != nil {
		defer close(c.stopped)
	}
	for i := range c.count {
		e := event.Event{ID: event.Hash(i)[:20], Text: fmt.Sprintf("ERROR quiet producer %d", i), Source: event.Source{"type": "kubernetes"}, LineCount: 1}
		if err := emit(e); err != nil {
			if errors.Is(err, errCap) && ctx.Err() == nil {
				return errors.New("cap failed to cancel intake")
			}
			return err
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.delivered:
		return nil
	}
}

func TestMaxEventsStopsQuietLiveProducerAndDrainsQueue(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "synthetic")
	started, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	j := &delayedJudge{started: started, release: release}
	deps := providerDeps(j)
	deps.collector = func(kube.Config) (logCollector, error) { return quietCollector{count: 2, stopped: stopped}, nil }
	var out, stderr bytes.Buffer
	cmd := newCommand(strings.NewReader(""), &out, &stderr, deps)
	cmd.SetArgs([]string{"k8s", "--follow", "--max-events", "2", "--queue-size", "2", "--batch-size", "1", "--json"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("quiet producer waited for N+1")
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("analysis not admitted")
	}
	close(release)
	var partial exitError
	if err := <-done; !errors.As(err, &partial) || partial.code != 2 {
		t.Fatal(err)
	}
	var value struct{ Events []event.Event }
	if err := json.Unmarshal(out.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Events) != 2 || j.calls != 2 {
		t.Fatal("admitted queue lost", len(value.Events), j.calls)
	}
	for _, e := range value.Events {
		if e.Importance != "important" {
			t.Fatal("admitted analysis cancelled", e)
		}
	}
}

type notificationOutput struct {
	bytes.Buffer
	delivered chan struct{}
	once      sync.Once
}

func (w *notificationOutput) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	w.once.Do(func() { close(w.delivered) })
	return n, err
}

func TestControllerDeliversNotificationOnlyJSONOnStdout(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "synthetic")
	release := make(chan struct{})
	close(release)
	j := &delayedJudge{release: release}
	deps := providerDeps(j)
	out := &notificationOutput{delivered: make(chan struct{})}
	stopIntake := make(chan struct{})
	deps.collector = func(kube.Config) (logCollector, error) {
		return quietCollector{count: 1, delivered: stopIntake}, nil
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd := newCommand(strings.NewReader(""), out, &stderr, deps)
	cmd.SetArgs([]string{"controller", "--namespace", "synthetic", "--state", filepath.Join(dir, "state.db"), "--listen", "127.0.0.1:0", "--batch-size", "1", "--sink", "stdout"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	select {
	case <-out.delivered:
	case <-ctx.Done():
		t.Fatal("no notification delivered")
	}
	// Wait for the real delivery acknowledgement before ending collection.
	// Reading this private fixture database avoids timing sleeps or a fake sink.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "state.db")+"?mode=ro&_pragma=busy_timeout%3d1000")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var delivered int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM outbox WHERE status='delivered'").Scan(&delivered); err != nil {
			t.Fatal(err)
		}
		if delivered > 0 {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("delivery was not acknowledged")
		}
	}
	close(stopIntake)
	var partial exitError
	if err := <-done; !errors.As(err, &partial) || partial.code != 2 {
		t.Fatal(err)
	}
	if ctx.Err() != nil || j.calls != 1 {
		t.Fatal("notification delivery timed out", ctx.Err(), j.calls)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if out.Len() == 0 {
		t.Fatal("no notification delivered")
	}
	for _, line := range lines {
		var n controller.Notification
		if err := provider.Strict([]byte(line), &n); err != nil || n.ID == "" {
			t.Fatal("stdout is not notification JSON", line, err)
		}
	}
	for _, want := range []string{"[important/degraded/dependency]", "ERROR quiet producer 0", "events=1"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatal("plain evidence/summary missing from stderr", want, stderr.String())
		}
	}
}

type streamingCollector struct{ stopped chan struct{} }

func (streamingCollector) Snapshot(context.Context, func(event.Event) error, func(event.Source, string)) error {
	return errors.New("unexpected snapshot")
}
func (c streamingCollector) Follow(ctx context.Context, emit, finalize func(event.Event) error, _ func(event.Source, string)) error {
	if c.stopped != nil {
		defer close(c.stopped)
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		e := event.Event{ID: event.Hash(i)[:20], Text: fmt.Sprintf("ERROR synthetic %d", i), Source: event.Source{"type": "kubernetes"}, LineCount: 1, Judgment: event.Unknown("")}
		if err := emit(e); err != nil {
			return err
		}
	}
}
func TestControllerBudgetStopsEvenWithHoldAndInspection(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "synthetic")
	release := make(chan struct{})
	close(release)

	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Model supervisor restarts with the same persisted state and fresh bounded
	// provider budgets. Each process must terminate, releasing the state lock.
	for cycle := 0; cycle < 3; cycle++ {
		j := &delayedJudge{release: release}
		deps := providerDeps(j)
		deps.collector = func(kube.Config) (logCollector, error) { return streamingCollector{}, nil }
		var out, stderr bytes.Buffer
		cmd := newCommand(strings.NewReader(""), &out, &stderr, deps)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		_, port, _ := net.SplitHostPort(listener.Addr().String())
		listener.Close()
		cmd.SetArgs([]string{"controller", "--namespace", "synthetic", "--state", filepath.Join(dir, "state.db"), "--listen", "127.0.0.1:0", "--inspect-port", port, "--hold-after-stop", "--max-batches", "3", "--batch-size", "1"})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = cmd.ExecuteContext(ctx)
		if err == nil || !strings.Contains(err.Error(), "provider budget exhausted") {
			cancel()
			t.Fatal(err)
		}
		if ctx.Err() != nil || j.calls < 3 {
			cancel()
			t.Fatal("collector stayed live after budget exhaustion", ctx.Err(), j.calls)
		}
		cancel()
		for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			if line != "" && !json.Valid([]byte(line)) {
				t.Fatal("notification stdout mixed with plain output")
			}
		}
	}

}
func TestPlainOutputIncludesSafeEvidenceAndJudgment(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"files", "-", "--offline"}, strings.NewReader("ERROR connection refused\n  at service.call\nINFO done\n"), &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	for _, want := range []string{"[important/degraded/unknown]", "connection refused", "  at service.call", "events=2"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}

func TestDurationStopsIntakeAndDrainsInFlightProvider(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "synthetic")
	started, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	j := &delayedJudge{started: started, release: release}
	deps := providerDeps(j)
	deps.collector = func(kube.Config) (logCollector, error) { return streamingCollector{stopped: stopped}, nil }
	var out, stderr bytes.Buffer
	cmd := newCommand(strings.NewReader(""), &out, &stderr, deps)
	cmd.SetArgs([]string{"k8s", "--follow", "--duration", "1", "--queue-size", "2", "--batch-size", "1", "--json"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("analysis did not start")
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("intake deadline ignored")
	}
	close(release)
	var partial exitError
	if err := <-done; !errors.As(err, &partial) || partial.code != 2 {
		t.Fatal(err)
	}
	var report struct{ Events []event.Event }
	if json.Unmarshal(out.Bytes(), &report) != nil || len(report.Events) == 0 {
		t.Fatal("missing report")
	}
	for _, e := range report.Events {
		if e.Importance != "important" {
			t.Fatal("deadline cancelled admitted analysis", e)
		}
	}
}
