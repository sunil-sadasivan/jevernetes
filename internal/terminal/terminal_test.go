package terminal

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHelperSuspendsUIAndCancelledFrameNeverReads(t *testing.T) {
	mode := "cooked"
	s, err := Start(context.Background(), func() error { mode = "cooked"; return nil }, func() error { mode = "raw"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	release, err := Interactive(context.Background())
	if err != nil || mode != "cooked" {
		t.Fatalf("helper terminal: %s %v", mode, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := s.Frame(ctx, func() error { t.Error("UI read helper input"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := release(); err != nil || mode != "raw" {
		t.Fatalf("resume: %s %v", mode, err)
	}
	if err := s.Frame(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil || mode != "cooked" {
		t.Fatalf("close: %s %v", mode, err)
	}
}
