package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/output"
	"golang.org/x/sys/unix"
)

var renderCases = map[string][]string{
	"help":         {"--help"},
	"command-help": {"files", "--help"},
	"help-command": {"help", "files"},
	"root":         {},
	"version":      {"--version"},
	"unknown-help": {"help", "missing"},
}

type shortRenderWriter struct{}

func (shortRenderWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

func TestRenderWriteFailures(t *testing.T) {
	for name, args := range renderCases {
		for kind, writer := range map[string]io.Writer{"failed": failedOutput{}, "short": shortRenderWriter{}} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				if code := Execute(context.Background(), args, strings.NewReader(""), writer, io.Discard); code != 1 {
					t.Fatalf("write failure exit=%d, want 1", code)
				}
			})
		}
	}
}

func TestRenderOutputCompatibility(t *testing.T) {
	for name, args := range renderCases {
		t.Run(name, func(t *testing.T) {
			// Compare against the same command tree with Cobra's raw writers.
			var expected, expectedErr, actual, actualErr bytes.Buffer
			cmd := New(strings.NewReader(""), &expected, &expectedErr)
			cmd.SetOut(&expected)
			cmd.SetErr(&expectedErr)
			cmd.SetArgs(args)
			if err := cmd.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if code := Execute(context.Background(), args, strings.NewReader(""), &actual, &actualErr); code != 0 {
				t.Fatalf("exit=%d", code)
			}
			if actual.String() != expected.String() || actualErr.String() != expectedErr.String() {
				t.Fatal("Cobra rendering changed")
			}
		})
	}
}

func TestRenderBlockedPipe(t *testing.T) {
	if name := os.Getenv("JEV_RENDER_CHILD"); name != "" {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		defer w.Close()
		// Fill the OS pipe before even the short version output is attempted.
		raw, err := w.SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		if err := raw.Control(func(fd uintptr) {
			flags, e := unix.FcntlInt(fd, unix.F_GETFL, 0)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = unix.FcntlInt(fd, unix.F_SETFL, flags|unix.O_NONBLOCK); e != nil {
				t.Fatal(e)
			}
			for {
				_, e = unix.Write(int(fd), []byte{0})
				if e == unix.EAGAIN || e == unix.EWOULDBLOCK {
					break
				}
				if e != nil {
					t.Fatal(e)
				}
			}
			if _, e = unix.FcntlInt(fd, unix.F_SETFL, flags); e != nil {
				t.Fatal(e)
			}
		}); err != nil {
			t.Fatal(err)
		}
		var ctx context.Context
		var cancel context.CancelFunc
		if os.Getenv("JEV_RENDER_SIGNAL") == "1" {
			ctx, cancel = signal.NotifyContext(context.Background(), syscall.SIGTERM)
			defer cancel()
			timer := time.AfterFunc(100*time.Millisecond, func() { _ = syscall.Kill(os.Getpid(), syscall.SIGTERM) })
			defer timer.Stop()
		} else {
			ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
		}
		// Both streams blocked: Cobra's error rendering must also be cancellable.
		code := Execute(ctx, renderCases[name], strings.NewReader(""), w, w)
		if code != 130 {
			t.Fatalf("cancelled render exit=%d, want 130", code)
		}
		return
	}
	for name := range renderCases {
		for _, mode := range []string{"cancel", "signal"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRenderBlockedPipe$")
				child.Env = append(os.Environ(), "JEV_RENDER_CHILD="+name)
				if mode == "signal" {
					child.Env = append(child.Env, "JEV_RENDER_SIGNAL=1")
				}
				if b, err := child.CombinedOutput(); err != nil {
					t.Fatalf("blocked render did not exit: %v %s", err, b)
				}
			})
		}
	}
}

func TestUsageWriteFailure(t *testing.T) {
	cmd := New(strings.NewReader(""), failedOutput{}, io.Discard)
	cmd.SetContext(context.Background())
	if err := cmd.Usage(); err == nil {
		t.Fatal("usage write error ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd = New(strings.NewReader(""), io.Discard, io.Discard)
	cmd.SetContext(ctx)
	if err := cmd.Usage(); !errors.Is(err, context.Canceled) {
		t.Fatalf("usage cancellation=%v", err)
	}
}

func TestRenderWrapperPreservesFinalGrace(t *testing.T) {
	var out bytes.Buffer
	cmd := New(strings.NewReader(""), &out, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd.SetContext(ctx)
	// A failed direct render must not poison independently owned final output.
	if _, err := cmd.OutOrStdout().Write([]byte("cancelled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("direct write=%v", err)
	}
	if err := output.Final(cmd.OutOrStdout(), func(w io.Writer) error {
		_, err := io.WriteString(w, "final report\n")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "final report\n" {
		t.Fatalf("final output=%q", out.String())
	}
}
