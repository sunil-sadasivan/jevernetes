package safefile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRejectSpecialFilesAndAliases(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, link, fifo, "/dev/null"} {
		if f, err := Open(context.Background(), p); err == nil {
			f.Close()
			t.Fatal("accepted special path", p)
		}
	}
	if b, err := Read(context.Background(), regular, 4); err != nil || string(b) != "safe" {
		t.Fatal(string(b), err)
	}
	if _, err := Read(context.Background(), regular, 3); err == nil {
		t.Fatal("size limit ignored")
	}
	hard := filepath.Join(dir, "hard")
	if err := os.Link(regular, hard); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{regular, hard} {
		if f, err := Open(context.Background(), p); err == nil {
			f.Close()
			t.Fatal("accepted hardlink")
		}
	}
	t.Run("socket", func(t *testing.T) {
		socket := filepath.Join(dir, "socket")
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		if err := unix.Bind(fd, &unix.SockaddrUnix{Name: socket}); err != nil {
			t.Fatal(err)
		}

		if f, err := Open(context.Background(), socket); err == nil {
			f.Close()
			t.Fatal("accepted socket")
		}
	})
}

func TestCancellationClosesDescriptorAndJoins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		f, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		select {
		case <-f.done:
		case <-time.After(time.Second):
			t.Fatal("descriptor not closed on cancellation")
		}
		if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatal("live descriptor after cancellation", err)
		}
		var wg sync.WaitGroup
		for n := 0; n < 2; n++ {
			wg.Go(func() { _ = f.Close() })
		}
		wg.Wait()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	f, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	f.Close() // Idempotent even when cancellation never runs.
}
