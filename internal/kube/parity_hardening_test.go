package kube

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	wire "github.com/moby/spdystream/spdy"
	"golang.org/x/sys/unix"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// Isolate intentionally leaked baseline dependency workers and global stderr.
func hardeningChild(t *testing.T) bool {
	t.Helper()
	if os.Getenv("JEV_HARDENING_CHILD") == t.Name() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), "JEV_HARDENING_CHILD="+t.Name())
	if out, err := cmd.CombinedOutput(); err != nil {
		if len(out) > 12000 {
			out = out[:12000]
		}
		t.Fatalf("isolated regression: %v\n%s", err, out)
	}
	return false
}

func TestParityForwardWorkers(t *testing.T) {
	for _, mode := range []string{"no-ack", "error-fin-data-reset", "error-no-fin"} {
		t.Run(mode, func(t *testing.T) {
			if !hardeningChild(t) {
				return
			}
			a, b := net.Pipe()
			defer b.Close()
			reached, peerDone := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(peerDone)
				r := bufio.NewReader(b)
				if _, err := http.ReadRequest(r); err != nil {
					return
				}
				io.WriteString(b, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\nX-Stream-Protocol-Version: portforward.k8s.io\r\n\r\n")
				f, _ := wire.NewFramer(b, r)
				frame, err := f.ReadFrame()
				if err != nil {
					return
				}
				syn := frame.(*wire.SynStreamFrame)
				if mode == "no-ack" {
					close(reached)
					io.Copy(io.Discard, b)
					return
				}
				f.WriteFrame(&wire.SynReplyFrame{StreamId: syn.StreamId, Headers: http.Header{}})
				// Read the client's error FIN and data SYN before writing to avoid pipe deadlock.
				for {
					frame, err = f.ReadFrame()
					if err != nil {
						return
					}
					if _, ok := frame.(*wire.SynStreamFrame); ok {
						break
					}
				}
				data := frame.(*wire.SynStreamFrame)
				if mode == "error-fin-data-reset" {
					f.WriteFrame(&wire.DataFrame{StreamId: syn.StreamId, Data: []byte("attacker-secret"), Flags: wire.DataFlagFin})
					f.WriteFrame(&wire.RstStreamFrame{StreamId: data.StreamId, Status: wire.RefusedStream})
					close(reached)
					io.Copy(io.Discard, b)
				} else {
					f.WriteFrame(&wire.SynReplyFrame{StreamId: data.StreamId, Headers: http.Header{}})
					close(reached)
					// No FIN, and no cancellation until the application enforces its bound.
					for i := 0; i < 1024; i++ {
						if f.WriteFrame(&wire.DataFrame{StreamId: syn.StreamId, Data: []byte(strings.Repeat("x", 8192))}) != nil {
							return
						}
					}
					io.Copy(io.Discard, b)
				}
			}()
			cfg := &rest.Config{Host: "http://fixture.invalid", Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }, Dial: func(context.Context, string, string) (net.Conn, error) { return a, nil }}
			tunnel, err := (SPDYForwarder{Config: cfg}).Open(context.Background(), "local", "pod", 9091)
			if err != nil {
				t.Fatal(err)
			}
			defer tunnel.Close()
			u, _ := url.Parse(tunnel.BaseURL())
			local, err := net.Dial("tcp", u.Host)
			if err != nil {
				t.Fatal(err)
			}
			defer local.Close()
			select {
			case <-reached:
			case <-time.After(time.Second):
				t.Fatal("fixture not reached")
			}
			if mode == "error-no-fin" {
				select {
				case <-peerDone:
				case <-time.After(time.Second):
					t.Error("unbounded error stream kept consuming without FIN")
				}
			}
			closed := make(chan struct{})
			go func() { tunnel.Close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("Close did not join promptly")
			}
			local.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			var p [1]byte
			if _, err := local.Read(p[:]); err != io.EOF {
				t.Errorf("local socket survives Close: %v", err)
			}
			// Close's contract is completion, not eventual goroutine retirement.
			stacks := make([]byte, 1<<20)
			stacks = stacks[:runtime.Stack(stacks, true)]
			for _, name := range []string{"portforward.(*PortForwarder).handleConnection", "(*Stream).WaitTimeout", "(*tunnel).handleConnection"} {
				if strings.Contains(string(stacks), name) {
					t.Errorf("forwarding worker survives Close: %s", name)
				}
			}
			b.Close()
			<-peerDone
		})
	}
}

func TestParityMetadataByteBounds(t *testing.T) {
	for _, kind := range []string{"list", "get", "error", "log-error"} {
		t.Run(kind, func(t *testing.T) {
			// A single oversized annotation defeats object-count limits.
			prefix, suffix := `{"kind":"Pod","apiVersion":"v1","metadata":{"name":"pod","namespace":"local","annotations":{"large":"`, `"}},"status":{"phase":"Running"}}`
			if kind == "list" {
				prefix = `{"kind":"PodList","apiVersion":"v1","items":[` + prefix
				suffix += `]}`
			}
			body := &largeUpgradeBody{remaining: 16 << 20}
			status := 200
			if strings.Contains(kind, "error") {
				status = 403
				prefix = ""
				suffix = ""
			}
			config := &rest.Config{Host: "http://fixture.invalid", Transport: fileRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(io.MultiReader(strings.NewReader(prefix), body, strings.NewReader(suffix))), Request: r}, nil
			})}
			c, err := New(config, Config{Namespace: "local"})
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "get":
				_, err = SelectPod(context.Background(), c.API, Config{Namespace: "local", Pod: "pod"})
			case "log-error":
				_, err = c.OpenLog(context.Background(), "local", "pod", nil)
			default:
				_, err = c.list(context.Background())
			}
			if err == nil {
				t.Error("oversized response accepted")
			}
			cap := int64(8 << 20)
			if strings.Contains(kind, "error") {
				cap = 64 << 10
			}
			if body.read > cap+1 {
				t.Errorf("consumed %d bytes; cap=%d", body.read, cap)
			}
			if err != nil && (len(err.Error()) > 512 || strings.Contains(err.Error(), "xxx")) {
				t.Error("raw response escaped in diagnostic")
			}
		})
	}
}

func TestParityFullStderrCancellation(t *testing.T) {
	if !hardeningChild(t) {
		return
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd := int(w.Fd())
	unix.SetNonblock(fd, true)
	fill := []byte(strings.Repeat("z", 4096))
	filled := 0
	for {
		n, writeErr := unix.Write(fd, fill)
		err = writeErr
		if n > 0 {
			filled += n
		}
		if err == unix.EAGAIN {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	unix.SetNonblock(fd, false)
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	partial := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"kind":"Pod","metadata":{"annotations":{"secret":"synthetic-secret`)
		w.(http.Flusher).Flush()
		close(partial)
		<-r.Context().Done()
	}))
	defer server.Close()
	read := make(chan struct{})
	c, err := New(&rest.Config{Host: server.URL, WrapTransport: func(base http.RoundTripper) http.RoundTripper {
		return fileRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			res, err := base.RoundTrip(req)
			if err == nil {
				res.Body = &noticeBody{ReadCloser: res.Body, read: read}
			}
			return res, err
		})
	}}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); c.API.Pods("local").Get(ctx, "pod", metav1.GetOptions{}) }()
	<-partial
	<-read
	cancel()
	drained := make(chan []byte, 1)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("client-go diagnostic blocks cancellation on full stderr")
	}
	go func() { b, _ := io.ReadAll(r); drained <- b }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request did not join after drain")
	}
	os.Stderr = old
	w.Close()
	output := <-drained
	if len(output) != filled || strings.Trim(string(output), "z") != "" {
		t.Error("dependency diagnostics escaped to stderr")
	}

}

// Notify only after the successful response entered client-go's body reader.
type noticeBody struct {
	io.ReadCloser
	once sync.Once
	read chan struct{}
}

func (b *noticeBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.once.Do(func() { close(b.read) })
	}
	return n, err
}

func TestParityResponseLimitBoundariesAndLogStreaming(t *testing.T) {
	for _, status := range []int{200, 403} {
		for _, path := range []string{"/prefix/api/v1/namespaces/local/pods/pod", "/prefix/api/v1/namespaces/local/pods/pod/log"} {
			limit := int64(maxMetadataBytes)
			if status == 403 {
				limit = maxAPIErrorBytes
			}
			for _, size := range []int64{limit, limit + 1} {
				t.Run(fmt.Sprintf("%d/%s/%d", status, path, size), func(t *testing.T) {
					body := &largeUpgradeBody{remaining: size}
					tr := responseTransport{fileRoundTripFunc(func(r *http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: status, Body: body, Request: r}, nil
					})}
					req, _ := http.NewRequest("GET", "http://fixture.invalid"+path, nil)
					res, err := tr.RoundTrip(req)
					if err != nil {
						t.Fatal(err)
					}
					n, err := io.Copy(io.Discard, res.Body)
					res.Body.Close()
					unlimited := status == 200 && strings.HasSuffix(path, "/log")
					if (size > limit && !unlimited) != (err != nil) {
						t.Fatalf("limit contract: bytes=%d err=%v", n, err)
					}
					if !body.closed {
						t.Error("underlying body not closed")
					}
				})
			}
		}
	}
}

func TestParityUpgradeLateSocketPublication(t *testing.T) {
	// A custom dialer may return after cancellation. Force shutdown before socket
	// publication, then prove Open joins and the late socket is already closed.
	client, peer := net.Pipe()
	defer peer.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	cfg := &rest.Config{Host: "http://fixture.invalid", Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }, Dial: func(context.Context, string, string) (net.Conn, error) { close(entered); <-release; return client, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt, up, err := spdyTransport(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", cfg.Host, nil)
	done := make(chan error, 1)
	go func() { _, err := rt.RoundTrip(req); done <- err }()
	<-entered
	cancel()
	up.close()
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Error("canceled handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("late dial did not join")
	}
	peer.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := peer.Read(b[:]); err != io.EOF {
		t.Fatalf("late socket remained live: %v", err)
	}
	// Publication is ordered by RoundTrip completion; these reads may race only
	// with close, which does not mutate socket/conn. Exercise repeated closes.
	var closes sync.WaitGroup
	for i := 0; i < 8; i++ {
		closes.Add(1)
		go func() { defer closes.Done(); up.close() }()
	}
	closes.Wait()
}

func TestParityLateForwardErrorAfterDataEOF(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	sendError, peerDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(peerDone)
		reader := bufio.NewReader(peer)
		if _, err := http.ReadRequest(reader); err != nil {
			return
		}
		io.WriteString(peer, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\nX-Stream-Protocol-Version: portforward.k8s.io\r\n\r\n")
		f, _ := wire.NewFramer(peer, reader)
		frame, err := f.ReadFrame()
		if err != nil {
			return
		}
		errorID := frame.(*wire.SynStreamFrame).StreamId
		f.WriteFrame(&wire.SynReplyFrame{StreamId: errorID, Headers: http.Header{}})
		var dataID wire.StreamId
		for {
			frame, err = f.ReadFrame()
			if err != nil {
				return
			}
			if syn, ok := frame.(*wire.SynStreamFrame); ok {
				dataID = syn.StreamId
				break
			}
		}
		f.WriteFrame(&wire.SynReplyFrame{StreamId: dataID, Headers: http.Header{}})
		f.WriteFrame(&wire.DataFrame{StreamId: dataID, Data: []byte("response"), Flags: wire.DataFlagFin})
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			for {
				if _, err := f.ReadFrame(); err != nil {
					return
				}
			}
		}()
		<-sendError
		f.WriteFrame(&wire.DataFrame{StreamId: errorID, Data: []byte("synthetic-late-error"), Flags: wire.DataFlagFin})
		<-drained
	}()
	cfg := &rest.Config{Host: "http://fixture.invalid", Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }, Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	opened, err := (SPDYForwarder{Config: cfg}).Open(context.Background(), "local", "pod", 9091)
	if err != nil {
		t.Fatal(err)
	}
	owner := opened.(*tunnel)
	defer func() { owner.Close(); peer.Close(); <-peerDone }()
	u, _ := url.Parse(owner.BaseURL())
	local, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	local.SetReadDeadline(time.Now().Add(time.Second))
	body, err := io.ReadAll(local)
	if err != nil || string(body) != "response" {
		t.Fatalf("data forwarding failed: %v", err)
	}
	// Let data cleanup settle before the peer emits its final error status.
	time.Sleep(20 * time.Millisecond)
	close(sendError)
	select {
	case <-owner.done:
	case <-time.After(time.Second):
		t.Error("late error status was discarded after data EOF")
	}
}
