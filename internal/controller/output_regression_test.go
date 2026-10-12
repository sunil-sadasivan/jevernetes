package controller

import (
	"bytes"
	"context"
	"github.com/sunil-sadasivan/jevernetes/internal/output"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestStdoutCancellationPipe(t *testing.T) {
	if os.Getenv("JEV_PIPE_CHILD") == "1" {
		r, w, e := os.Pipe()
		if e != nil {
			t.Fatal(e)
		}
		defer r.Close()
		defer w.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if e := (StdoutSink{Writer: w}).Send(ctx, "", []byte(strings.Repeat("x", 4<<20))); e == nil {
			t.Fatal("blocked output succeeded")
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStdoutCancellationPipe$")
	cmd.Env = append(os.Environ(), "JEV_PIPE_CHILD=1")
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("stdout child did not join after cancellation: %v %s", e, b)
	}
}

type enteredSink struct {
	sink    Sink
	entered chan struct{}
}

func (s enteredSink) Send(ctx context.Context, id string, p []byte) error {
	close(s.entered)
	return s.sink.Send(ctx, id, p)
}
func TestDeliveryWorkerJoinsBlockedPipe(t *testing.T) {
	s := openTest(t)
	if _, e := s.Apply(context.Background(), occurrence(1), "fixture", DefaultPolicy(), time.Now().Unix()); e != nil {
		t.Fatal(e)
	}
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	defer w.Close()
	fill, stop := context.WithTimeout(context.Background(), 40*time.Millisecond)
	_, e = output.Write(fill, w, []byte(strings.Repeat("x", 4<<20)))
	stop()
	if e == nil {
		t.Fatal("pipe not filled")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- s.Deliver(ctx, enteredSink{StdoutSink{Writer: w}, entered}) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("delivery not attempted")
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		w.Close()
		<-done
		t.Fatal("delivery worker did not join")
	}
	status, e := s.Status(context.Background())
	if e != nil || status.Pending != 1 {
		t.Fatal("cancelled delivery acknowledged", status, e)
	}
}

func TestInheritedStdoutCancellation(t *testing.T) {
	if os.Getenv("JEV_INHERITED_STDOUT") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		defer cancel()
		e := (StdoutSink{Writer: os.Stdout}).Send(ctx, "", []byte(strings.Repeat("x", 4<<20)))
		if e == nil {
			os.Exit(9)
		}
		os.Exit(0) // The testing runner must not print PASS into the still-full pipe.
	}
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInheritedStdoutCancellation$")
	child.Env = append(os.Environ(), "JEV_INHERITED_STDOUT=1")
	child.Stdout = w
	var stderr bytes.Buffer
	child.Stderr = &stderr
	if e := child.Run(); e != nil {
		t.Fatalf("inherited stdout child failed to join: %v %s", e, stderr.String())
	}
}
