package kube

import (
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/moby/spdystream"
	"k8s.io/apimachinery/pkg/util/httpstream"
)

// Own the pinned stream engine's Serve, ping and stream-creation lifetimes.
// The upstream httpstream adapter waits 30 seconds for an acknowledgement even
// after raw closure and does not join its workers. Keep the official wire engine
// but poll its acknowledgement with a short bounded wait and join on shutdown.
type ownedStreams struct {
	raw                 net.Conn
	conn                *spdystream.Connection
	mu                  sync.Mutex
	streams             map[uint32]*spdystream.Stream
	closed              bool
	creators            sync.WaitGroup
	serveDone, pingDone chan struct{}
	once                sync.Once
}

func newOwnedStreams(raw net.Conn) (httpstream.Connection, error) {
	conn, err := spdystream.NewConnection(raw, false)
	if err != nil {
		return nil, err
	}
	c := &ownedStreams{raw: raw, conn: conn, streams: make(map[uint32]*spdystream.Stream), serveDone: make(chan struct{}), pingDone: make(chan struct{})}
	go func() { defer close(c.serveDone); conn.Serve(func(s *spdystream.Stream) { _ = s.Reset() }) }()
	go func() {
		defer close(c.pingDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-conn.CloseChan():
				return
			case <-ticker.C:
				_, _ = conn.Ping()
			}
		}
	}()
	return c, nil
}

func (c *ownedStreams) interrupt() {
	// Never wait on a write/stream lock before interrupting raw I/O.
	_ = c.raw.Close()
	c.mu.Lock()
	c.closed = true
	for _, s := range c.streams {
		_ = s.Reset()
	}
	c.mu.Unlock()
}
func (c *ownedStreams) Close() error {
	c.once.Do(func() {
		c.interrupt()
		c.creators.Wait()
		<-c.serveDone
		<-c.pingDone
		_ = c.conn.Close()
		_ = c.conn.Wait(0)
	})
	return nil
}
func (c *ownedStreams) CreateStream(headers http.Header) (httpstream.Stream, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("forwarding connection closed")
	}
	c.creators.Add(1)
	c.mu.Unlock()
	defer c.creators.Done()
	s, err := c.conn.CreateStream(headers, nil, false)
	if err != nil {
		if s != nil {
			_ = s.Reset()
		}
		return nil, errors.New("forwarding stream unavailable")
	}
	c.mu.Lock()
	c.streams[s.Identifier()] = s
	if c.closed {
		_ = s.Reset()
	}
	c.mu.Unlock()
	deadline := time.Now().Add(30 * time.Second)
	for {
		// A queued RST in moby's frame worker sends to an unbuffered startChan.
		// Continue receiving until Serve has joined even after interruption; otherwise
		// returning on cancellation can strand that sender inside Serve itself.
		err = s.WaitTimeout(10 * time.Millisecond)
		if err == nil {
			return s, nil
		}
		if err != spdystream.ErrTimeout {
			c.RemoveStreams(s)
			return nil, errors.New("forwarding stream rejected")
		}
		select {
		case <-c.serveDone:
			c.RemoveStreams(s)
			return nil, errors.New("forwarding connection closed")
		default:
		}
		if time.Now().After(deadline) {
			c.interrupt()
		}
	}
}
func (c *ownedStreams) RemoveStreams(streams ...httpstream.Stream) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range streams {
		if s != nil {
			_ = s.Reset()
			delete(c.streams, s.Identifier())
		}
	}
}
func (c *ownedStreams) CloseChan() <-chan bool         { return c.conn.CloseChan() }
func (c *ownedStreams) SetIdleTimeout(d time.Duration) { c.conn.SetIdleTimeout(d) }
