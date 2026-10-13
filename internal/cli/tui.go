package cli

import (
	"context"
	"errors"
	"github.com/spf13/cobra"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
	"github.com/sunil-sadasivan/jevernetes/internal/search"
	"github.com/sunil-sadasivan/jevernetes/internal/tui"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
	"io"
	"os"
)

func (o *options) runTerminal(cmd *cobra.Command, paths []string, stdin io.Reader, deps runtimeDeps) error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			fd, e := unix.Dup(int(f.Fd()))
			if e == nil {
				tty = os.NewFile(uintptr(fd), "terminal")
				err = nil
			}
		}
		if err != nil {
			return errors.New("--tui requires an interactive terminal")
		}
	}
	defer tty.Close()
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	updates := make(chan map[string]any, 1)
	result := make(chan error, 1)
	done := make(chan error, 1)
	copy := *o
	copy.tui = false
	deps.quiet = true
	send := func(v map[string]any) {
		select {
		case updates <- v:
		default:
			select {
			case <-updates:
			default:
			}
			select {
			case updates <- v:
			default:
			}
		}
	}
	deps.update = send
	deps.finished = send
	cmd.SetContext(ctx)
	go func() { err := copy.runWith(cmd, paths, stdin, deps); result <- err; done <- err }()
	quit, uiErr := tui.Run(ctx, tty, updates, done, tui.Options{Offline: o.offline, Client: func(ctx context.Context, budget int) (search.Client, error) {
		key, err := provider.CredentialContext(ctx, "typesafe", os.Getenv)
		if err != nil {
			return nil, err
		}
		return provider.New(provider.Config{Kind: "typesafe", Model: "jev-latest", InputPrice: .042, MaxCost: .01, MaxAttempts: budget * 3, MaxBatches: budget}, key)
	}})
	cancel()
	runErr := <-result
	if uiErr != nil {
		return uiErr
	}
	if quit && errors.Is(runErr, context.Canceled) {
		return nil
	}
	return runErr
}
