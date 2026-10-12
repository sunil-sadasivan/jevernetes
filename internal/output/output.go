// Package output owns synchronous, cancellable command descriptor writes.
package output

import (
	"context"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// File output must have one in-process owner. Serialize the temporary descriptor
// flag change, including aliases of stdout/stderr. No write goroutine is started.
type fileIdentity struct{ dev, ino uint64 }
type fileGate struct {
	ready chan struct{}
	refs  int
}

var gates = struct {
	sync.Mutex
	entries map[fileIdentity]*fileGate
}{entries: make(map[fileIdentity]*fileGate)}

func own(ctx context.Context, fd int) (func(), error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	key := fileIdentity{uint64(st.Dev), uint64(st.Ino)}
	gates.Lock()
	g := gates.entries[key]
	if g == nil {
		g = &fileGate{ready: make(chan struct{}, 1)}
		gates.entries[key] = g
	}
	g.refs++
	gates.Unlock()
	drop := func() {
		gates.Lock()
		g.refs--
		if g.refs == 0 {
			delete(gates.entries, key)
		}
		gates.Unlock()
	}
	select {
	case g.ready <- struct{}{}:
		return func() { <-g.ready; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

type Writer struct {
	Context context.Context
	Output  io.Writer
}

func (w Writer) Write(p []byte) (int, error) { return Write(w.Context, w.Output, p) }
func Write(ctx context.Context, w io.Writer, p []byte) (n int, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	f, ok := w.(*os.File)
	if !ok {
		// Library-provided writers are synchronous trusted code (buffers in tests).
		// A custom blocking writer must implement its own context ownership.
		n, err = w.Write(p)
		if err == nil && n != len(p) {
			err = io.ErrShortWrite
		}
		return
	}
	// SyscallConn keeps the file alive while polling/writing, and avoids Fd's
	// side effect of disabling Go's nonblocking mode for pollable descriptors.
	raw, e := f.SyscallConn()
	if e != nil {
		return 0, e
	}
	e = raw.Control(func(fd uintptr) {
		release, e := own(ctx, int(fd))
		if e != nil {
			err = e
			return
		}
		defer release()
		flags, e := unix.FcntlInt(fd, unix.F_GETFL, 0)
		if e != nil {
			err = e
			return
		}
		if _, e = unix.FcntlInt(fd, unix.F_SETFL, flags|unix.O_NONBLOCK); e != nil {
			err = e
			return
		}
		defer func() {
			_, e := unix.FcntlInt(fd, unix.F_SETFL, flags)
			if err == nil {
				err = e
			}
		}()
		for n < len(p) {
			if err = ctx.Err(); err != nil {
				return
			}
			count, e := unix.Write(int(fd), p[n:])
			if count > 0 {
				n += count
			}
			if e == unix.EINTR {
				continue
			}
			if e != nil && e != unix.EAGAIN && e != unix.EWOULDBLOCK {
				err = e
				return
			}
			if e == nil && count == 0 {
				err = io.ErrShortWrite
				return
			}
			if n == len(p) {
				return
			}
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
			if _, e = unix.Poll(poll, 25); e != nil && e != unix.EINTR {
				err = e
				return
			}
		}
	})
	if err == nil {
		err = e
	}
	return
}

// Final output gets a short independent grace window so signal shutdown can
// still emit a complete report to a draining consumer after workers are joined.
func Final(w io.Writer, fn func(io.Writer) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return fn(Writer{ctx, w})
}
