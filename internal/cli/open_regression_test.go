package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Real child processes bound regressions even when open ignores cancellation.
func TestUnsafeOpenChild(t *testing.T) {
	if os.Getenv("JEV_OPEN_CHILD") != "1" {
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Exit(Execute(ctx, args, os.Stdin, os.Stdout, os.Stderr))
}
func TestFIFOOpenIsBounded(t *testing.T) {
	for _, kind := range []string{"input", "rules", "policy", "dashboard", "credential", "signal"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input")
			if err := unix.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
			reportPath := filepath.Join(t.TempDir(), "report.json")
			args := []string{"files", path, "--offline", "--json", "--output", reportPath}
			switch kind {
			case "rules":
				args = []string{"files", "-", "--offline", "--json", "--grouping-strategy", "semantic", "--template-rules", path}
			case "policy":
				args = []string{"controller", "--namespace", "synthetic", "--offline", "--state", filepath.Join(t.TempDir(), "state.db"), "--policy", path}
			case "dashboard":
				args = []string{"dashboard", "--report", path}
			case "credential":
				args = []string{"files", "-"}
			case "signal":
				args = []string{"files", path, "-", "--offline", "--json", "--output", reportPath}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestUnsafeOpenChild$", "--"}, args...)...)
			cmd.Env = []string{"JEV_OPEN_CHILD=1", "TYPESAFE_API_KEY_FILE=" + path}
			var out, stderr bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &stderr
			if kind == "signal" {
				in, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				defer in.Close()
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				_, _ = in.Write([]byte("INFO ready\n"))
				time.Sleep(200 * time.Millisecond)
				_ = cmd.Process.Signal(syscall.SIGTERM)
			} else if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			err := cmd.Wait()
			if ctx.Err() != nil {
				t.Fatalf("%s open blocked past deadline (including SIGTERM): %v", kind, err)
			}
			code := cmd.ProcessState.ExitCode()
			if kind == "input" || kind == "signal" {
				want := 2
				if kind == "signal" {
					want = 130
				}
				if code != want || !json.Valid(out.Bytes()) {
					t.Fatalf("code=%d want=%d report=%s stderr=%s", code, want, out.String(), stderr.String())
				}
				var report struct {
					Runtime string
					Metrics map[string]uint64
				}
				if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Runtime != "go" || report.Metrics["coverage_gaps"] == 0 {
					t.Fatal("missing incomplete-coverage report", out.String())
				}
				saved, err := os.ReadFile(reportPath)
				var savedValue, stdoutValue any
				if err != nil || json.Unmarshal(saved, &savedValue) != nil || json.Unmarshal(out.Bytes(), &stdoutValue) != nil || !reflect.DeepEqual(savedValue, stdoutValue) {
					t.Fatal("saved final report differs", err)
				}
				st, err := os.Stat(reportPath)
				if err != nil || st.Mode().Perm() != 0600 {
					t.Fatal("final report is not private", err)
				}
			} else if code == 0 {
				t.Fatal("special configuration accepted")
			}
		})
	}
}
