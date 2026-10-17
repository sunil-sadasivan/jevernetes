// Package safefile opens user-supplied regular files without blocking on FIFOs
// or following leaf aliases. File data is read only from the verified descriptor.
package safefile

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

var ErrUnsafe = errors.New("unsafe file type or alias")

type File struct {
	*os.File
	stop      func() bool
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func (f *File) Close() error {
	f.closeOnce.Do(func() {
		if !f.stop() {
			<-f.done
		}
		f.closeErr = f.File.Close()
	})
	return f.closeErr
}

func Open(ctx context.Context, path string) (*File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Avoid opening known devices at all. This is only a preliminary check;
	// the descriptor check below is authoritative if the pathname changes.
	var before unix.Stat_t
	if err := unix.Lstat(path, &before); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 {
		return nil, ErrUnsafe
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	raw := os.NewFile(uintptr(fd), path)
	f := &File{File: raw, done: make(chan struct{})}
	f.stop = context.AfterFunc(ctx, func() { raw.Close(); close(f.done) })
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Dev != before.Dev || st.Ino != before.Ino {
		f.Close()
		return nil, ErrUnsafe
	}
	// Regular files ignore O_NONBLOCK; leave it set to avoid changing flags
	// through a descriptor that cancellation may have concurrently closed.
	if err := ctx.Err(); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func Read(ctx context.Context, path string, limit int64) ([]byte, error) {
	f, err := Open(ctx, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if limit < 0 || st.Size() > limit {
		return nil, ErrUnsafe
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, ErrUnsafe
	}
	return raw, ctx.Err()
}
