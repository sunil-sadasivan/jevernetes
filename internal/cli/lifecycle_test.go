package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

type streamingCollector struct{}

func (streamingCollector) Snapshot(context.Context, func(event.Event) error, func(event.Source, string)) error {
	return errors.New("unexpected snapshot")
}
func (streamingCollector) Follow(ctx context.Context, emit func(event.Event) error, _ func(event.Source, string)) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		e := event.Event{ID: fmt.Sprintf("event%d", i), Text: fmt.Sprintf("ERROR synthetic %d", i), Source: event.Source{"type": "kubernetes"}, LineCount: 1, Judgment: event.Unknown("")}
		if err := emit(e); err != nil {
			return err
		}
	}
}
func TestControllerBudgetStopsEvenWithHoldAndInspection(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "synthetic")
	release := make(chan struct{})
	close(release)
	j := &delayedJudge{release: release}
	deps := providerDeps(j)
	deps.collector = func(kube.Config) (logCollector, error) { return streamingCollector{}, nil }
	var out, stderr bytes.Buffer
	cmd := newCommand(strings.NewReader(""), &out, &stderr, deps)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cmd.SetArgs([]string{"controller", "--namespace", "synthetic", "--state", filepath.Join(dir, "state.db"), "--listen", "127.0.0.1:0", "--hold-after-stop", "--max-batches", "3", "--batch-size", "1"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cmd.ExecuteContext(ctx); err == nil || !strings.Contains(err.Error(), "provider budget exhausted") {
		t.Fatal(err)
	}
	if ctx.Err() != nil || j.calls < 3 {
		t.Fatal("collector stayed live after budget exhaustion", ctx.Err(), j.calls)
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
