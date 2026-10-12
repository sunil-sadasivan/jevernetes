package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) { return 0, errors.New("synthetic write failure") }
func TestPlainOutputError(t *testing.T) {
	code := Execute(context.Background(), []string{"files", "-", "--offline"}, strings.NewReader("fixture\n"), failedOutput{}, io.Discard)
	if code != 1 {
		t.Fatalf("plain write error ignored: exit %d", code)
	}
}
func TestPlainCancellationPipe(t *testing.T) {
	if os.Getenv("JEV_PLAIN_CHILD") == "1" {
		r, w, e := os.Pipe()
		if e != nil {
			t.Fatal(e)
		}
		defer r.Close()
		defer w.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		Execute(ctx, []string{"files", "-", "--offline", "--max-events", "1000"}, strings.NewReader(strings.Repeat("fixture message "+strings.Repeat("x", 1000)+"\n", 1000)), w, io.Discard)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPlainCancellationPipe$")
	cmd.Env = append(os.Environ(), "JEV_PLAIN_CHILD=1")
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("plain child did not join after cancellation: %v %s", e, b)
	}
}

func TestFinalJSONCancellationPipe(t *testing.T) {
	if os.Getenv("JEV_JSON_CHILD") == "1" {
		r, w, e := os.Pipe()
		if e != nil {
			t.Fatal(e)
		}
		defer r.Close()
		defer w.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		code := Execute(ctx, []string{"files", "-", "--offline", "--json"}, strings.NewReader(strings.Repeat("fixture "+strings.Repeat("x", 1000)+"\n", 1000)), w, io.Discard)
		if code == 0 {
			t.Fatal("blocked final JSON succeeded")
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFinalJSONCancellationPipe$")
	child.Env = append(os.Environ(), "JEV_JSON_CHILD=1")
	if b, e := child.CombinedOutput(); e != nil {
		t.Fatalf("final JSON child did not join: %v %s", e, b)
	}
}
