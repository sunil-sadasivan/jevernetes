package cli

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// contextInput is the sole reader of a process stdin descriptor. Polling avoids
// changing inherited descriptor flags, and avoids relying on Close to interrupt a
// blocking file read (which is not guaranteed for standard descriptors on macOS).
type contextInput struct {
	ctx  context.Context
	file *os.File
}

func (r contextInput) Read(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	fds := []unix.PollFd{{Fd: int32(r.file.Fd()), Events: unix.POLLIN}}
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		n, err := unix.Poll(fds, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, errors.New("stdin polling failed")
		}
		if n == 0 {
			continue
		}
		if fds[0].Revents&unix.POLLNVAL != 0 {
			return 0, errors.New("stdin unavailable")
		}
		return r.file.Read(buf)
	}
}
