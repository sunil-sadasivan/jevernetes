package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/kube"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type admittedCollector struct {
	quietCollector
	admitted chan struct{}
}

func (c admittedCollector) Follow(ctx context.Context, emit, finalize func(event.Event) error, gap func(event.Source, string)) error {
	// Signal only after every enqueue succeeded, then wait for cancellation.
	for i := range c.count {
		if err := emit(event.Event{ID: event.Hash(i)[:20], Text: "ERROR admitted " + event.Hash(i)[:8], Source: event.Source{"type": "kubernetes"}, LineCount: 1}); err != nil {
			return err
		}
	}
	close(c.admitted)
	<-ctx.Done()
	return ctx.Err()
}

func savedEvents(t *testing.T, path string) []event.Event {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report struct{ Events []event.Event }
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	return report.Events
}

func TestPlainCancellationDrainsAdmittedReportAndState(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "synthetic")
	for _, mode := range []string{"k8s", "controller"} {
		t.Run(mode, func(t *testing.T) {
			started, admitted := make(chan struct{}), make(chan struct{})
			j := &delayedJudge{started: started, release: make(chan struct{})}
			deps := providerDeps(j)
			deps.collector = func(kube.Config) (logCollector, error) {
				return admittedCollector{quietCollector: quietCollector{count: 4}, admitted: admitted}, nil
			}
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path, statePath := filepath.Join(dir, "report.json"), filepath.Join(dir, "state.db")
			var out, stderr bytes.Buffer
			cmd := newCommand(strings.NewReader(""), &out, &stderr, deps)
			args := []string{mode, "--output", path, "--batch-size", "2", "--queue-size", "4"}
			if mode == "controller" {
				args = append(args, "--namespace", "demo", "--state", statePath, "--listen", "127.0.0.1:0")
			} else {
				args = append(args, "--follow")
			}
			cmd.SetArgs(args)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- cmd.ExecuteContext(ctx) }()
			for _, ready := range []chan struct{}{admitted, started} {
				select {
				case <-ready:
				case err := <-done:
					t.Fatalf("command stopped before admission: %v", err)
				case <-time.After(5 * time.Second):
					t.Fatal("fixture did not admit/start")
				}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("signal exit error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown did not join")
			}
			events := savedEvents(t, path)
			if len(events) != 4 {
				t.Errorf("lost admitted batch/queue events: got %d, want 4", len(events))
			}
			if mode == "controller" {
				db, err := sql.Open("sqlite", "file:"+statePath+"?mode=ro")
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var count int
				if err := db.QueryRow("SELECT count(*) FROM seen").Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 4 {
					t.Errorf("lost persisted admissions: got %d, want 4", count)
				}
			}
		})
	}
}

func TestLiveQuietPendingEventSurvivesIntakeStop(t *testing.T) {
	for _, stop := range []string{"duration", "cancel"} {
		t.Run(stop, func(t *testing.T) {
			p := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "demo", UID: "uid-one"}, Spec: v1.PodSpec{Containers: []v1.Container{{Name: "app"}}}, Status: v1.PodStatus{Phase: v1.PodRunning, ContainerStatuses: []v1.ContainerStatus{{Name: "app", ContainerID: "runtime://one", State: v1.ContainerState{Running: &v1.ContainerStateRunning{}}}}}}
			r, w := io.Pipe()
			defer w.Close()
			closed := make(chan struct{})
			input := &observedInput{PipeReader: r, closed: closed}
			deps := runtimeDeps{collector: func(cfg kube.Config) (logCollector, error) {
				return &kube.Collector{Config: cfg, API: fake.NewClientset(p).CoreV1(), OpenLog: func(context.Context, string, string, *v1.PodLogOptions) (io.ReadCloser, error) { return input, nil }}, nil
			}}
			var out, stderr bytes.Buffer
			path := filepath.Join(t.TempDir(), "report.json")
			cmd := newCommand(strings.NewReader(""), &out, &stderr, deps)
			args := []string{"k8s", "--namespace", "demo", "--follow", "--offline", "--output", path}
			if stop == "duration" {
				args = append(args, "--duration", "1")
			}
			cmd.SetArgs(args)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- cmd.ExecuteContext(ctx) }()
			written := make(chan struct{})
			go func() { _, _ = io.WriteString(w, "2026-01-01T00:00:00Z ERROR quiet failure\n"); close(written) }()
			select {
			case <-written:
			case err := <-done:
				t.Fatalf("command stopped before stream read: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("stream not read")
			}
			if stop == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				var partial exitError
				if stop == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if stop == "duration" && (!errors.As(err, &partial) || partial.code != 2) {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("stream shutdown did not join")
			}
			select {
			case <-closed:
			default:
				t.Fatal("stream not closed")
			}
			events := savedEvents(t, path)
			if len(events) != 1 {
				t.Fatalf("pending quiet event lost: got %d", len(events))
			}
			e := events[0]
			if e.Text != "ERROR quiet failure" || !e.Truncated || !e.ParseUncertain || e.Importance == "routine" {
				t.Fatalf("unsafe final evidence: %+v", e)
			}
		})
	}
}

// This writer models an undrained descriptor without starting a write goroutine.
// Production descriptors use the same context via output.Write's poll loop.
type blockedEvidence struct{ calls int }

func (w *blockedEvidence) Write([]byte) (int, error) { panic("missing output context") }
func (w *blockedEvidence) WriteContext(ctx context.Context, _ []byte) (int, error) {
	w.calls++
	<-ctx.Done()
	return 0, ctx.Err()
}

func TestPlainOutputFailureStillDrainsAdmissions(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "synthetic")
	for _, failure := range []string{"failed", "blocked"} {
		t.Run(failure, func(t *testing.T) {
			started, admitted, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			j := &delayedJudge{started: started, release: release}
			deps := providerDeps(j)
			deps.collector = func(kube.Config) (logCollector, error) {
				return admittedCollector{quietCollector: quietCollector{count: 4}, admitted: admitted}, nil
			}
			var writer io.Writer = failedOutput{}
			blocked := &blockedEvidence{}
			if failure == "blocked" {
				writer = blocked
			}
			path := filepath.Join(t.TempDir(), "report.json")
			cmd := newCommand(strings.NewReader(""), writer, io.Discard, deps)
			cmd.SetArgs([]string{"k8s", "--follow", "--output", path, "--batch-size", "2", "--queue-size", "4"})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- cmd.ExecuteContext(ctx) }()
			for _, ready := range []chan struct{}{admitted, started} {
				select {
				case <-ready:
				case err := <-done:
					t.Fatal(err)
				case <-ctx.Done():
					t.Fatal("admission timed out")
				}
			}
			close(release)
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "cannot write evidence output") || ctx.Err() != nil {
					t.Fatalf("output failure not surfaced with bounded shutdown: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("blocked output prevented drain")
			}
			if events := savedEvents(t, path); len(events) != 4 {
				t.Fatalf("output failure lost admissions: %d", len(events))
			}
			if failure == "blocked" && blocked.calls != 1 {
				t.Fatalf("retried blocked output %d times", blocked.calls)
			}
		})
	}
}

type finalizingCollector struct {
	started <-chan struct{}
	release chan struct{}
	cap     bool
}

func (finalizingCollector) Snapshot(context.Context, func(event.Event) error, func(event.Source, string)) error {
	return errors.New("unexpected snapshot")
}
func (c finalizingCollector) Follow(ctx context.Context, emit, finalize func(event.Event) error, _ func(event.Source, string)) error {
	defer close(c.release)
	e := event.Event{ID: "first", Text: "ERROR first", Source: event.Source{"type": "kubernetes"}, LineCount: 1}
	if err := emit(e); err != nil {
		return err
	}
	<-c.started // The first event is in a blocked provider call; queue is empty.
	e.ID, e.Text = "second", "ERROR second"
	err := emit(e) // Fill the sole queue slot (and reach the cap if configured).
	if c.cap && !errors.Is(err, errCap) {
		return errors.New("exact cap not reached")
	}
	if !c.cap {
		<-ctx.Done()
	}
	if ctx.Err() == nil {
		return errors.New("intake not cancelled")
	}
	if err := emit(e); err == nil {
		return errors.New("fresh admission after cancellation")
	}
	for i := range 20 {
		e.ID = event.Hash(i)[:20]
		err := finalize(e)
		if c.cap && !errors.Is(err, errCap) {
			return errors.New("finalization exceeded event cap")
		}
		if !c.cap && err != nil {
			return err
		}
	}
	return ctx.Err()
}

func TestFinalAdmissionKeepsQueueAndExactCapBounds(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "synthetic")
	for _, cap := range []bool{false, true} {
		t.Run(map[bool]string{false: "full_queue", true: "exact_cap"}[cap], func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			j := &delayedJudge{started: started, release: release}
			deps := providerDeps(j)
			deps.collector = func(kube.Config) (logCollector, error) { return finalizingCollector{started, release, cap}, nil }
			path := filepath.Join(t.TempDir(), "report.json")
			cmd := newCommand(strings.NewReader(""), io.Discard, io.Discard, deps)
			args := []string{"k8s", "--follow", "--duration", "1", "--queue-size", "1", "--batch-size", "1", "--output", path}
			if cap {
				args = append(args, "--max-events", "2")
			}
			cmd.SetArgs(args)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var partial exitError
			if err := cmd.ExecuteContext(ctx); !errors.As(err, &partial) || partial.code != 2 || ctx.Err() != nil {
				t.Fatalf("finalization did not join: %v", err)
			}
			if events := savedEvents(t, path); len(events) != 2 {
				t.Fatalf("queue/cap changed: %d", len(events))
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var report struct{ Metrics map[string]uint64 }
			if err = json.Unmarshal(raw, &report); err != nil {
				t.Fatal(err)
			}
			if !cap && report.Metrics["dropped"] != 20 {
				t.Fatalf("finalization queue loss not accounted: %v", report.Metrics)
			}
		})
	}
}

type cancelEvidence struct{ cancel context.CancelFunc }

func (w cancelEvidence) Write([]byte) (int, error) { panic("missing output context") }
func (w cancelEvidence) WriteContext(ctx context.Context, _ []byte) (int, error) {
	w.cancel()
	return 0, ctx.Err()
}

func TestPlainCancellationDuringEvidenceWriteSavesBatchAndExits130(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "report.json")
	code := Execute(ctx, []string{"files", "-", "--offline", "--batch-size", "4", "--output", path}, strings.NewReader("ERROR one\nERROR two\nERROR three\nERROR four\n"), cancelEvidence{cancel}, io.Discard)
	if code != 130 {
		t.Fatalf("signal exit changed: %d", code)
	}
	if events := savedEvents(t, path); len(events) != 4 {
		t.Fatalf("cancelled evidence write lost batch: %d", len(events))
	}
}
