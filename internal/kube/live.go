package kube

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
)

// Follow bounds stream concurrency, polls discovery and preserves parser state over
// reconnects. Queue ownership/backpressure belongs to the caller's emit function.
func (c *Collector) Follow(ctx context.Context, emit func(event.Event) error, gap func(event.Source, string)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	defer wg.Wait()
	active := map[string]context.CancelFunc{}
	done := make(chan string, c.Config.MaxStreams)
	fatal := make(chan error, 1)
	discover := func() error {
		pods, err := c.list(ctx)
		if err != nil {
			return err
		}
		present := map[string]bool{}
		for _, p := range pods {
			for _, s := range streams(p, false, true) {
				source := s.source(c.Config)
				id := event.Hash(source)
				present[id] = true
				if _, ok := active[id]; ok {
					continue
				}
				if len(active) >= c.Config.MaxStreams {
					gap(source, "stream_capacity")
					continue
				}
				child, stop := context.WithCancel(ctx)
				active[id] = stop
				wg.Add(1)
				go func(s stream, id string) {
					defer wg.Done()
					err := c.followStream(child, s, emit, gap)
					if err != nil && child.Err() == nil {
						select {
						case fatal <- err:
						default:
						}
						cancel()
					}
					select {
					case done <- id:
					case <-ctx.Done():
					}
				}(s, id)
			}
		}
		for id, stop := range active {
			if !present[id] {
				stop()
				// Keep the slot until the reader has actually joined.
				gap(event.Source{"type": "kubernetes"}, "stream_removed")
			}
		}
		return nil
	}
	if err := discover(); err != nil {
		cancel()
		return err
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			cancel()
			select {
			case err := <-fatal:
				return err
			default:
				return ctx.Err()
			}
		case id := <-done:
			delete(active, id)
		case <-ticker.C:
			if err := discover(); err != nil {
				gap(event.Source{"type": "kubernetes"}, "discovery_unavailable")
			}
		}
	}
}
func (c *Collector) followStream(ctx context.Context, s stream, emit func(event.Event) error, gap func(event.Source, string)) error {
	source := s.source(c.Config)
	parser := event.NewParser(source)
	cursor := newCursor(func(reason string) { gap(source, reason) })
	parser.AcceptLine = cursor.accept
	validationDelay := 100 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		opts := &v1.PodLogOptions{Container: s.name, Follow: true, Timestamps: true, TailLines: &c.Config.Tail, SinceSeconds: &c.Config.Since}
		if cursor.last != nil {
			opts.SinceSeconds = nil
			opts.TailLines = nil
			opts.SinceTime = metaTime(*cursor.last)
			cursor.reconnect()
		}
		if err := c.validateStream(ctx, s); err != nil {
			gap(source, identityGap(err))
			if errors.Is(err, errIdentityChanged) {
				return nil
			}
			if err := waitRetry(ctx, validationDelay); err != nil {
				return err
			}
			validationDelay = min(2*validationDelay, 5*time.Second)
			continue
		}
		validationDelay = 100 * time.Millisecond
		r, err := c.OpenLog(ctx, s.pod.Namespace, s.pod.Name, opts)
		if err == nil {
			err = readLive(ctx, r, parser, emit)
			r.Close()
		} else {
			err = errStreamRead
		}
		var consumer *consumerError
		if errors.As(err, &consumer) {
			return consumer.err
		}
		if ctx.Err() != nil {
			for _, e := range parser.Finish(true) {
				if err := emit(e); err != nil {
					return err
				}
			}
			return ctx.Err()
		}
		if err != nil && err != errStreamRead {
			return err
		}
		gap(source, "stream_reconnect")
		if err := waitRetry(ctx, time.Second); err != nil {
			return err
		}
	}
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type consumerError struct{ err error }

func (e *consumerError) Error() string { return e.err.Error() }

var errStreamRead = errors.New("log transport interrupted")

// Reads run in a joined goroutine so idle events flush while the network is quiet.
func readLive(ctx context.Context, r io.ReadCloser, p *event.Parser, emit func(event.Event) error) error {
	type chunk struct {
		data []byte
		err  error
	}
	ch := make(chan chunk, 1)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 8192)
		for {
			n, err := r.Read(buf)
			v := chunk{append([]byte{}, buf[:n]...), err}
			select {
			case ch <- v:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { close(stop); r.Close(); wg.Wait() }()
	timer := time.NewTicker(350 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if e := p.Flush(); e != nil {
				if err := emit(*e); err != nil {
					return &consumerError{err}
				}
			}
		case v := <-ch:
			for _, e := range p.Feed(v.data) {
				if err := emit(e); err != nil {
					return &consumerError{err}
				}
			}
			if v.err != nil {
				for _, e := range p.Finish(v.err != io.EOF) {
					if err := emit(e); err != nil {
						return &consumerError{err}
					}
				}

				return errStreamRead
			}
		}
	}
}
