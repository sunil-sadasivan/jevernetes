package kube

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

func TestSPDYPostUpgradeFrameBoundsJoin(t *testing.T) {
	// These bytes pass a genuine client-go 101 upgrade and reach the pinned
	// wire decoder, including bytes coalesced with the HTTP header terminator.
	var compressed bytes.Buffer
	z := zlib.NewWriter(&compressed)
	z.Write([]byte{0x10, 0, 0, 0}) // decompressed header map count
	z.Flush()
	headers := []byte{0x80, 3, 0, 2, 0, 0, 0, 0, 0, 0, 0, 1}
	binary.BigEndian.PutUint32(headers[4:8], uint32(4+compressed.Len()))
	headers = append(headers, compressed.Bytes()...)
	z.Close()
	cases := map[string][]byte{
		"settings-witness":  {0x80, 3, 0, 4, 0, 0, 0, 4, 0x10, 0, 0, 0},
		"oversized-data":    {0, 0, 0, 1, 0, 255, 255, 255},
		"oversized-control": {0x80, 3, 0, 1, 0, 255, 255, 255},
		"compressed-count":  headers,
	}
	for name, raw := range cases {
		for _, active := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/buffered", true: "/active-worker"}[active], func(t *testing.T) {
				client, peer := net.Pipe()
				defer client.Close()
				defer peer.Close()
				client.SetDeadline(time.Now().Add(5 * time.Second))
				peer.SetDeadline(time.Now().Add(5 * time.Second))
				peerDone := make(chan struct{})
				send := make(chan struct{})
				go func() {
					defer close(peerDone)
					r := bufio.NewReader(peer)
					if _, err := http.ReadRequest(r); err != nil {
						return
					}
					response := []byte("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\nX-Stream-Protocol-Version: portforward.k8s.io\r\n\r\n")
					if !active {
						response = append(response, raw...)
					}
					if _, err := peer.Write(response); err != nil {
						return
					}
					if active {
						<-send
						// Wait for a worker's SYN_STREAM; leave its acknowledgement pending.
						var frame [8]byte
						if _, err := io.ReadFull(r, frame[:]); err != nil {
							return
						}
						size := binary.BigEndian.Uint32(frame[4:]) & 0xffffff
						if _, err := io.CopyN(io.Discard, r, int64(size)); err != nil {
							return
						}
						if _, err := peer.Write(raw); err != nil {
							return
						}
					}
					io.Copy(io.Discard, r)
				}()
				cfg := &rest.Config{Host: "http://fixture.invalid", Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }, Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
				opened, err := (SPDYForwarder{Config: cfg}).Open(context.Background(), "local", "pod", 9091)
				if err != nil {
					t.Fatal(err)
				}
				owner := opened.(*tunnel)
				defer owner.Close()
				if active {
					u, _ := url.Parse(owner.BaseURL())
					local, err := net.Dial("tcp", u.Host)
					if err != nil {
						t.Fatal(err)
					}
					defer local.Close()
					close(send)
				}
				select {
				case <-owner.done:
				case <-time.After(2 * time.Second):
					t.Fatal("malformed frame did not close and join tunnel")
				}
				engine := owner.conn.(*ownedStreams)
				for _, done := range []<-chan struct{}{engine.serveDone, engine.pingDone, peerDone} {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("decoder failure leaked worker or socket")
					}
				}
				owner.mu.Lock()
				remaining := len(owner.locals)
				owner.mu.Unlock()
				if remaining != 0 {
					t.Fatal("local forwarding workers not joined")
				}
			})
		}
	}
}
