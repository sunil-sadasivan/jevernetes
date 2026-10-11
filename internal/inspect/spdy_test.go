package inspect

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	streamspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"

	"github.com/sunil-sadasivan/jevernetes/internal/kube"
)

// This uses the real client-go SPDY upgrade, port-forward streams, loopback
// listener, HTTP inspection client and server. Only the Kubernetes endpoint is fake.
func TestRealSPDYInspectionAndCancellationContract(t *testing.T) {
	state := NewState()
	state.Ready.Store(true)
	state.Add("received", 7)
	inspection := Handler(store(t), state)
	var upgrades, gets, dataStreams, closed atomic.Int32
	var streams sync.WaitGroup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const podPath = "/api/v1/namespaces/demo/pods/controller"
		if r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Error("Kubernetes credential not scoped to API")
		}
		if r.Method == "GET" && r.URL.Path == podPath {
			gets.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&v1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: "demo"}, Status: v1.PodStatus{Phase: v1.PodRunning}})
			return
		}
		if r.Method != "POST" || r.URL.Path != podPath+"/portforward" {
			t.Error("unexpected Kubernetes operation", r.Method, r.URL.Path)
			http.Error(w, "forbidden", 403)
			return
		}
		if _, err := httpstream.Handshake(r, w, []string{portforward.PortForwardProtocolV1Name}); err != nil {
			t.Error(err)
			return
		}
		conn := streamspdy.NewResponseUpgrader().UpgradeResponse(w, r, func(s httpstream.Stream, reply <-chan struct{}) error {
			if s.Headers().Get(v1.PortHeader) != "9091" {
				return fmt.Errorf("unexpected remote port")
			}
			streams.Add(1)
			go func() {
				defer streams.Done()
				defer s.Close()
				<-reply
				switch s.Headers().Get(v1.StreamType) {
				case v1.StreamTypeError:
					return
				case v1.StreamTypeData:
					dataStreams.Add(1)
					request, err := http.ReadRequest(bufio.NewReader(s))
					if err != nil {
						t.Error(err)
						return
					}
					if request.Method != "GET" || request.URL.Path != "/v1/status" || request.Header.Get("Authorization") != "" {
						t.Error("unsafe inspection request")
					}
					response := httptest.NewRecorder()
					inspection.ServeHTTP(response, request)
					result := response.Result()
					defer result.Body.Close()
					result.Close = true
					if err := result.Write(s); err != nil {
						t.Error(err)
					}
				default:
					t.Error("unexpected stream type")
				}
			}()
			return nil
		})
		if conn == nil {
			t.Error("upgrade failed")
			return
		}
		upgrades.Add(1)
		defer conn.Close()
		<-conn.CloseChan()
		closed.Add(1)
	}))
	defer server.Close()
	cfg := &rest.Config{Host: server.URL, BearerToken: "synthetic"}
	api, err := core.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	client := Client{API: api, Forward: kube.SPDYForwarder{Config: cfg}, Config: kube.Config{Namespace: "demo", Pod: "controller", MaxBytes: 1024, MaxStreams: 1}, Port: 9091}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value, err := client.Sample(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value.Controller)
	var status struct {
		Ready   bool
		Metrics map[string]uint64
	}
	if json.Unmarshal(raw, &status) != nil || !status.Ready || status.Metrics["received"] != 7 {
		t.Fatal(string(raw))
	}
	child, stop := context.WithCancel(ctx)
	tunnel, err := client.Forward.Open(child, "demo", "controller", 9091)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	joined := make(chan struct{})
	go func() { _ = tunnel.Close(); close(joined) }()
	select {
	case <-joined:
	case <-ctx.Done():
		t.Fatal("SPDY cancellation leaked")
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for closed.Load() != 2 {
		select {
		case <-deadline.C:
			t.Fatal("server connections leaked")
		case <-time.After(time.Millisecond):
		}
	}
	streams.Wait()
	if gets.Load() != 1 || upgrades.Load() != 2 || dataStreams.Load() != 1 {
		t.Fatal(gets.Load(), upgrades.Load(), dataStreams.Load())
	}
}
