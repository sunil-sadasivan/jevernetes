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
// reconnects. Queue ownership/backpressure belongs to the caller. finalize must
// make bounded admissions of already-read evidence even after intake cancellation.
func (c *Collector) Follow(ctx context.Context, emit, finalize func(event.Event) error, gap func(event.Source, string)) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	return c.follow(ctx, emit, finalize, gap, ticker.C)
}

func (c *Collector) follow(ctx context.Context, emit, finalize func(event.Event) error, gap func(event.Source, string), ticks <-chan time.Time) error {
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
				source := c.source(s)
				id := event.Hash(source)
				present[id] = true
				if _, ok := active[id]; ok {
					continue
				}
				if len(active) >= c.Config.MaxStreams {
					gap(source, "stream_capacity")
					continue
				}
				child, cancelChild := context.WithCancel(ctx)
				stop := sync.OnceFunc(cancelChild)
				active[id] = stop
				wg.Add(1)
				go func(s stream, id string) {
					defer wg.Done()
					defer stop()
					err := c.followStreamWithFinalizer(child, s, emit, finalize, gap)
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
				gap(c.discoverySource(), "stream_removed")
			}
		}
		return nil
	}
	if err := discover(); err != nil {
		cancel()
		return err
	}
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
		case <-ticks:
			if err := discover(); err != nil {
				gap(c.discoverySource(), "discovery_unavailable")
			}
		}
	}
}
func (c *Collector) followStream(ctx context.Context, s stream, emit func(event.Event) error, gap func(event.Source, string)) error {
	return c.followStreamWithFinalizer(ctx, s, emit, emit, gap)
}

func (c *Collector) followStreamWithFinalizer(ctx context.Context, s stream, emit, finalize func(event.Event) error, gap func(event.Source, string)) (result error) {
	source := c.source(s)
	parser := event.NewParser(source)
	defer func() {
		if ctx.Err() != nil {
			for _, e := range parser.Finish(true) {
				if err := finalize(e); err != nil {
					result = err
					break
				}
			}
		}
	}()
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
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r, err := c.OpenLog(ctx, s.pod.Namespace, s.pod.Name, opts)
		if err == nil {
			err = readLiveWithFinalizer(ctx, r, parser, emit, finalize)
			r.Close()
		} else {
			err = errStreamRead
		}
		var consumer *consumerError
		if errors.As(err, &consumer) {
			return consumer.err
		}
		if ctx.Err() != nil {
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

// Reads run in a joined goroutine so cancellation closes and joins the reader.
// A quiet interval is not an event boundary: only a new header or stream end
// finalizes a multiline event. The pending event remains byte-bounded.
func readLive(ctx context.Context, r io.ReadCloser, p *event.Parser, emit func(event.Event) error) error {
	return readLiveWithFinalizer(ctx, r, p, emit, emit)
}

func readLiveWithFinalizer(ctx context.Context, r io.ReadCloser, p *event.Parser, emit, finalize func(event.Event) error) error {
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
		defer close(ch)
		buf := make([]byte, 8192)
		for {
			if ctx.Err() != nil {
				return
			}
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
	// Only chunks already read may outlive intake. Closing the reader and draining
	// its bounded channel joins the in-flight Read without starting another one.
	finishReads := func() error {
		r.Close()
		for v := range ch {
			for _, e := range p.Feed(v.data) {
				if err := finalize(e); err != nil {
					return &consumerError{err}
				}
			}
		}
		return ctx.Err()
	}
	deliver := func(e event.Event) error {
		if ctx.Err() != nil {
			return finalize(e)
		}
		err := emit(e)
		// Admission may observe cancellation after the check above. A rejected
		// admission can still use the bounded final path; cap/errors are final.
		if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return finalize(e)
		}
		return err
	}
	for {
		if ctx.Err() != nil {
			return finishReads()
		}
		select {
		case <-ctx.Done():
			return finishReads()
		case v, ok := <-ch:
			if !ok {
				return ctx.Err()
			}
			for _, e := range p.Feed(v.data) {
				if err := deliver(e); err != nil {
					return &consumerError{err}
				}
			}
			if v.err != nil {
				for _, e := range p.Finish(v.err != io.EOF || ctx.Err() != nil) {
					if err := deliver(e); err != nil {
						return &consumerError{err}
					}
				}
				return errStreamRead
			}
		}
	}
}
