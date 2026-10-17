// Package terminal serializes full-screen UI frames and interactive credential
// helpers. A helper receives the cooked terminal and owns it until it is joined.
package terminal

import (
	"context"
	"errors"
)

var gate = make(chan struct{}, 1)
var active *Session // protected by gate, including all Session fields

type Session struct {
	suspend, resume func() error
	err             error
}

func acquire(ctx context.Context) (func(), error) {
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Start enters UI mode only after any foreground helper has released ownership.
// Both callbacks run while the terminal lease is held.
func Start(ctx context.Context, suspend, resume func() error) (*Session, error) {
	release, err := acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if active != nil {
		return nil, errors.New("terminal already has an active UI")
	}
	if err = resume(); err != nil {
		_ = suspend()
		return nil, err
	}
	s := &Session{suspend: suspend, resume: resume}
	active = s
	return s, nil
}

// Frame serializes a bounded render, poll and input read with helper handoffs.
func (s *Session) Frame(ctx context.Context, frame func() error) error {
	release, err := acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if active != s {
		return errors.New("terminal UI is closed")
	}
	if s.err != nil {
		return s.err
	}
	return frame()
}

// Close waits for the bounded credential helper to join before restoring the
// original terminal, even when the caller's command context is cancelled.
func (s *Session) Close() error {
	release, _ := acquire(context.Background())
	defer release()
	if active != s {
		return nil
	}
	active = nil
	return s.suspend()
}

// Interactive leaves full-screen/raw mode while holding exclusive ownership.
// Call release only after restoring the helper's foreground process group and
// saved terminal attributes. A resume failure also reaches the UI frame owner.
func Interactive(ctx context.Context) (func() error, error) {
	release, err := acquire(ctx)
	if err != nil {
		return nil, err
	}
	s := active
	if s != nil {
		if err = s.suspend(); err != nil {
			s.err = err
			release()
			return nil, err
		}
	}
	return func() error {
		defer release()
		if s != nil {
			s.err = s.resume()
			return s.err
		}
		return nil
	}, nil
}
