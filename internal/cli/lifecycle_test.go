package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
func TestMaxEventsDrainsOnlineAnalysisBeforeAndDuringProviderCall(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "synthetic")
	for _, timing := range []string{"before", "during"} {
		t.Run(timing, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			j := &delayedJudge{started: started, release: release}
			var out, stderr bytes.Buffer
			r, w := io.Pipe()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := newCommand(r, &out, &stderr, providerDeps(j))
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
				_, _ = io.WriteString(w, "ERROR third\nERROR fourth\n")
			} else {
				_, _ = io.WriteString(w, "ERROR first\nERROR second\nERROR third\nERROR fourth\n")
			}
			// The writer closes when intake hits the cap. Let cancellation propagate
			// while the already admitted provider call remains blocked.
			time.Sleep(30 * time.Millisecond)
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

type streamingCollector struct{ stopped chan struct{} }

func (streamingCollector) Snapshot(context.Context, func(event.Event) error, func(event.Source, string)) error {
	return errors.New("unexpected snapshot")
}
func (c streamingCollector) Follow(ctx context.Context, emit func(event.Event) error, _ func(event.Source, string)) error {
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
