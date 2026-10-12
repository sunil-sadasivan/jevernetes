package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/spf13/cobra"
	"github.com/sunil-sadasivan/jevernetes/internal/output"
)

// Cobra writes synchronously and sometimes discards render errors. Keep the
// first error while sending descriptor writes through the command's context.
type commandOutput struct {
	root *cobra.Command
	out  io.Writer
	mu   sync.Mutex
	err  error
}

func (w *commandOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	ctx := w.root.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	n, err := w.WriteContext(ctx, p)
	w.err = err
	return n, err
}

// Application output already supplies its own context, including final reports.
func (w *commandOutput) WriteContext(ctx context.Context, p []byte) (int, error) {
	return output.Write(ctx, w.out, p)
}

func (w *commandOutput) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func configureCommandOutput(root *cobra.Command, out, stderr io.Writer) {
	if out == nil {
		out = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	root.SetOut(&commandOutput{root: root, out: out})
	root.SetErr(&commandOutput{root: root, out: stderr})
	usage := root.UsageFunc()
	root.SetUsageFunc(func(c *cobra.Command) error {
		err := usage(c)
		if w, ok := c.OutOrStderr().(*commandOutput); ok {
			err = errors.Join(err, w.Err())
		}
		// Cobra's UsageString renders into a private buffer, then calls CheckErr.
		// Leave that rendering intact; cancellation applies to the actual write.
		return err
	})
}

func configureHelpCommand(root *cobra.Command) {
	root.InitDefaultHelpCmd()
	for _, help := range root.Commands() {
		if help.Name() != "help" {
			continue
		}
		// Keep Cobra's command metadata and rendering, but return errors instead
		// of its CheckErr/os.Exit path for an unknown help topic's usage output.
		help.Run = nil
		help.RunE = func(c *cobra.Command, args []string) error {
			cmd, _, err := c.Root().Find(args)
			if cmd == nil || err != nil {
				c.Printf("Unknown help topic %#q\n", args)
				return c.Root().Usage()
			}
			if cmd.Context() == nil {
				cmd.SetContext(c.Context())
			}
			cmd.InitDefaultHelpFlag()
			cmd.InitDefaultVersionFlag()
			return cmd.Help()
		}
	}
}
