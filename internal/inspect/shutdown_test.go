package inspect

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

type pipeListener struct {
	server     net.Conn
	once       sync.Once
	closedOnce sync.Once
	closed     chan struct{}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	var conn net.Conn
	l.once.Do(func() { conn = l.server })
	if conn != nil {
		return conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *pipeListener) Close() error { l.closedOnce.Do(func() { close(l.closed) }); return nil }

func (l *pipeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}
}

func TestInspectionShutdownJoinsHandlers(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	drained := make(chan struct{})
	go func() { defer close(drained); _, _ = io.Copy(io.Discard, client) }()
	defer func() { client.Close(); <-drained }()
	listener := &pipeListener{server: server, closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done()
			close(cancelled)
			<-release
		}), true)
	}()
	go func() { _, _ = client.Write([]byte("GET / HTTP/1.1\r\nHost: fixture.invalid\r\n\r\n")) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("handler context not cancelled")
	}
	select {
	case <-done:
		t.Error("inspection shutdown returned before handler joined")
		unblock()
		return
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown failed to join released handler")
	}
}
