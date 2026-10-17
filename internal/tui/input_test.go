package tui

import (
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestInputWithoutReadyByteDoesNotBlockAndRestoresFlags(t *testing.T) {
	for _, nonblocking := range []bool{false, true} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		defer w.Close()
		fd := int(r.Fd())
		if err := unix.SetNonblock(fd, nonblocking); err != nil {
			t.Fatal(err)
		}
		before, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err != nil {
			t.Fatal(err)
		}
		// If a regression blocks the read, closing the writer releases it, allowing
		// the test to fail without leaving a leaked goroutine or hanging the suite.
		deadline := time.AfterFunc(time.Second, func() { w.Close() })
		started := time.Now()
		buf := make([]byte, 16)
		n, err := readInput(fd, buf)
		deadline.Stop()
		if err != nil || n != 0 || time.Since(started) >= 500*time.Millisecond {
			t.Fatalf("idle read blocked or failed: n=%d err=%v", n, err)
		}
		after, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err != nil || after != before {
			t.Fatalf("descriptor flags changed: %d => %d: %v", before, after, err)
		}
		if _, err := w.Write([]byte("f")); err != nil {
			t.Fatal(err)
		}
		n, err = readInput(fd, buf)
		if err != nil || n != 1 || buf[0] != 'f' {
			t.Fatalf("input lost: n=%d err=%v", n, err)
		}
		w.Close()
		if _, err := readInput(fd, buf); !errors.Is(err, io.EOF) {
			t.Fatalf("hangup not reported: %v", err)
		}
	}
}
