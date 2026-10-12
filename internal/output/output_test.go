package output

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestWriteContracts(t *testing.T) {
	var b bytes.Buffer
	if n, e := Write(context.Background(), &b, []byte("fixture\n")); e != nil || n != 8 || b.String() != "fixture\n" {
		t.Fatal(n, e, b.String())
	}
	if _, e := Write(context.Background(), shortWriter{}, []byte("xx")); !errors.Is(e, io.ErrShortWrite) {
		t.Fatal(e)
	}
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	r.Close()
	defer w.Close()
	if _, e := Write(context.Background(), w, []byte("xx")); e == nil {
		t.Fatal("closed pipe error lost")
	}
}
func TestPipeCancellationRestoresFlags(t *testing.T) {
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	defer w.Close()
	// Establish the descriptor's post-first-write state before comparing flags
	// (Darwin records first-write state in F_GETFL).
	if _, e := w.Write([]byte("x")); e != nil {
		t.Fatal(e)
	}
	var one [1]byte
	if _, e := r.Read(one[:]); e != nil {
		t.Fatal(e)
	}
	fd := w.Fd()
	flags, e := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	n, e := Write(ctx, w, []byte(strings.Repeat("x", 4<<20)))
	if n == 0 || !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(n, e)
	}
	after, e := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if e != nil || after != flags {
		t.Fatal("descriptor flags changed", flags, after, e)
	}
}
func TestGateWaitCancellation(t *testing.T) {
	f, e := os.CreateTemp(t.TempDir(), "out")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	release, e := own(context.Background(), int(f.Fd()))
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, e := Write(ctx, f, []byte("xx")); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}
func TestIndependentDescriptorsProgress(t *testing.T) {
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := Write(ctx, w, []byte(strings.Repeat("x", 4<<20))); done <- e }()
	var one [1]byte
	if _, e := r.Read(one[:]); e != nil {
		t.Fatal(e)
	}
	reader, f, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	defer f.Close()
	other, stop := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer stop()
	_, e = Write(other, f, []byte("independent"))
	cancel()
	<-done
	if e != nil {
		t.Fatal("blocked stdout monopolized unrelated stderr", e)
	}
	gates.Lock()
	remaining := len(gates.entries)
	gates.Unlock()
	if remaining != 0 {
		t.Fatal("descriptor ownership leaked", remaining)
	}
}
func TestFinalDrainingPipe(t *testing.T) {
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	defer w.Close()
	done := make(chan string, 1)
	go func() {
		b, e := io.ReadAll(r)
		if e != nil {
			done <- "error"
			return
		}
		done <- string(b)
	}()
	want := strings.Repeat("fixture\n", 10000)
	if e := Final(w, func(out io.Writer) error { _, e := io.WriteString(out, want); return e }); e != nil {
		t.Fatal(e)
	}
	w.Close()
	if got := <-done; got != want {
		t.Fatal("output bytes changed")
	}
}
