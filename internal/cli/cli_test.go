package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/controller"
)

func execute(t *testing.T, args []string, input string) (int, map[string]any, string) {
	t.Helper()
	var out, err bytes.Buffer
	code := Execute(context.Background(), args, strings.NewReader(input), &out, &err)
	var value map[string]any
	_ = json.Unmarshal(out.Bytes(), &value)
	return code, value, err.String()
}
func TestOfflineCLIAndReportContract(t *testing.T) {
	code, v, err := execute(t, []string{"files", "-", "--offline", "--json"}, "ERROR synthetic failure\nINFO ready\n")
	if code != 0 {
		t.Fatal(code, err)
	}
	summary := v["summary"].(map[string]any)
	if summary["events"] != float64(2) || summary["important"] != float64(1) || summary["uncertain"] != float64(1) || v["runtime"] != "go" {
		t.Fatal(v)
	}
}
func TestCLIValidationBeforeInputsOrCredentials(t *testing.T) {
	for _, args := range [][]string{{"files", "/does-not-exist", "--batch-size", "0"}, {"files", "/does-not-exist", "--input-price", "NaN"}, {"files", "/does-not-exist", "--risk-provider", "arbitrary"}, {"files", "/does-not-exist", "--template-provider", "openai"}, {"remote", "--namespace", "../demo"}, {"remote", "--namespace", "demo", "--incident", "bad"}, {"dashboard", "--listen", "0.0.0.0:8765"}, {"controller", "--state", "/does-not-exist"}, {"search", "--anything"}} {
		code, _, err := execute(t, args, "")
		if code == 0 || strings.Contains(err, "provider key") || strings.Contains(err, "Kubernetes configuration") {
			t.Fatal(args, code, err)
		}
	}
}
func TestGzipGlobalCapAndPartialCoverage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := gzip.NewWriter(f)
	_, _ = z.Write([]byte("INFO one\nINFO two\n"))
	z.Close()
	f.Close()
	code, v, errText := execute(t, []string{"files", path, "-", "--offline", "--json", "--max-events", "2"}, "INFO three\n")
	if code != 2 || v["summary"].(map[string]any)["events"].(float64) > 2 {
		t.Fatal(code, v, errText)
	}
	code, v, _ = execute(t, []string{"files", filepath.Join(dir, "missing"), path, "--offline", "--json"}, "")
	if code != 2 || v["summary"].(map[string]any)["events"] != float64(2) {
		t.Fatal(code, v)
	}
}
func TestByteCapAndOutputFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	code, v, err := execute(t, []string{"files", "-", "--offline", "--json", "--output", path, "--max-file-bytes", "5"}, "INFO longer\n")
	if code != 2 || v == nil {
		t.Fatal(code, err)
	}
	st, e := os.Stat(path)
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal(e)
	}
}
func TestSignalContextClosesBlockingStdinAndReports(t *testing.T) {
	r, w := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	var out, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- Execute(ctx, []string{"files", "-", "--offline", "--json"}, r, &out, &stderr) }()
	_, _ = w.Write([]byte("INFO synthetic\n"))
	cancel()
	select {
	case code := <-done:
		if code != 130 {
			t.Fatal(code, stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled stdin blocked")
	}
	w.Close()
	if !json.Valid(out.Bytes()) {
		t.Fatal("missing shutdown report")
	}
}
func TestDurationGrammar(t *testing.T) {
	for s, want := range map[string]int64{"0": 0, "2m": 120, "1d": 86400} {
		got, err := seconds(s)
		if err != nil || got != want {
			t.Fatal(got, err)
		}
	}
	for _, s := range []string{"-1", "1.2h", "999999999999d", "no"} {
		if _, err := seconds(s); err == nil {
			t.Fatal("bad duration")
		}
	}
}
func TestCompatibilityAlias(t *testing.T) {
	root := New(strings.NewReader(""), io.Discard, io.Discard)
	cmd, _, err := root.Find([]string{"kubernetes"})
	if err != nil || cmd.Name() != "k8s" {
		t.Fatal("alias missing")
	}
}
func TestUnknownCommandDoesNotEchoArbitraryInput(t *testing.T) {
	code, _, err := execute(t, []string{"private-input-fixture"}, "")
	if code == 0 || strings.Contains(err, "private-input-fixture") {
		t.Fatal("unsafe CLI diagnostic")
	}
}
func TestCancellationWithOperatingSystemPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- Execute(ctx, []string{"files", "-", "--offline", "--json"}, r, &out, &stderr) }()
	_, _ = w.Write([]byte("INFO synthetic\n"))
	cancel()
	select {
	case code := <-done:
		if code != 130 || !json.Valid(out.Bytes()) {
			t.Fatal(code, stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("operating system pipe did not cancel")
	}
}

func TestOversizedInputCannotClaimComplete(t *testing.T) {
	code, v, _ := execute(t, []string{"files", "-", "--offline", "--json"}, strings.Repeat("x", 70000)+"\n")
	if code != 2 || v["summary"].(map[string]any)["complete_within_window"] != false || v["metrics"].(map[string]any)["truncated_events"] != float64(1) {
		t.Fatal("false complete oversized input")
	}
}
func TestControllerAllowsUnboundedEventsOnlyForController(t *testing.T) {
	// Invalid sink stops before any listener/configuration/provider access.
	path := filepath.Join(t.TempDir(), "state.db")
	for _, cap := range []string{"0", "100001"} {
		code, _, err := execute(t, []string{"controller", "--namespace", "demo", "--state", path, "--offline", "--sink", "invalid", "--max-events", cap}, "")
		if code == 0 || !strings.Contains(err, "invalid notification sink") {
			t.Fatal(code, err)
		}
	}
	code, _, err := execute(t, []string{"files", "-", "--offline", "--max-events", "0"}, "")
	if code == 0 || !strings.Contains(err, "invalid collection") {
		t.Fatal(code, err)
	}
}

func TestOutboxBackpressureRespectsCollectionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	_, err := waitForState(ctx, func() (controller.Applied, error) {
		attempts++
		return controller.Applied{}, controller.ErrBackpressure
	}, cancel)
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatal("collection stop stuck behind pending delivery", err, attempts)
	}
}

func TestPrepareStateVolumeCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0770); err != nil {
		t.Fatal(err)
	}
	code, _, errText := execute(t, []string{"prepare-state-volume", dir}, "")
	if code != 0 || errText != "" {
		t.Fatal(code, errText)
	}
	info, err := os.Stat(filepath.Join(dir, "private"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatal(info.Mode())
	}
	for _, args := range [][]string{{"prepare-state-volume"}, {"prepare-state-volume", dir, dir}, {"prepare-state-volume", filepath.Join(dir, "missing")}} {
		code, _, _ := execute(t, args, "")
		if code == 0 {
			t.Fatal("invalid preparation accepted", args)
		}
	}
}
