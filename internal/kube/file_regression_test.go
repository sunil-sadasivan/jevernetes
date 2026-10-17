package kube

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"k8s.io/client-go/rest"
)

func TestKubeFileChild(t *testing.T) {
	if os.Getenv("JEV_KUBE_FILE_CHILD") != "1" {
		return
	}
	config, err := Load(cfg())
	if err == nil {
		_, err = rest.TransportFor(config)
	}
	if err == nil {
		os.Exit(3)
	}
	os.Exit(0)
}
func TestKubeFIFOFilesBounded(t *testing.T) {
	for _, kind := range []string{"config", "token", "ca", "certificate", "key"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			fifo := filepath.Join(dir, "fifo")
			path := fifo
			if err := unix.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			if kind != "config" {
				cluster := "server: https://127.0.0.1:1"
				auth := "token: synthetic"
				switch kind {
				case "token":
					auth = "tokenFile: " + fifo
				case "ca":
					cluster += "\n    certificate-authority: " + fifo
				case "certificate", "key":
					regular := filepath.Join(dir, "regular")
					if err := os.WriteFile(regular, []byte("synthetic"), 0600); err != nil {
						t.Fatal(err)
					}
					cert, key := fifo, regular
					if kind == "key" {
						cert, key = regular, fifo
					}
					auth = "client-certificate: " + cert + "\n    client-key: " + key
				}
				path = filepath.Join(dir, "config")
				raw := "apiVersion: v1\nkind: Config\ncurrent-context: local\nclusters:\n- name: local\n  cluster:\n    " + cluster + "\nusers:\n- name: local\n  user:\n    " + auth + "\ncontexts:\n- name: local\n  context:\n    cluster: local\n    user: local\n"
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKubeFileChild$")
			cmd.Env = []string{"JEV_KUBE_FILE_CHILD=1", "KUBECONFIG=" + path}
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil || err != nil {
				t.Fatalf("unsafe %s config open: %v %s", kind, err, out)
			}
		})
	}
}

func TestLocalConfigMergeAndSafeTokenRotation(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	raw := "apiVersion: v1\nkind: Config\ncurrent-context: local\nclusters:\n- name: local\n  cluster:\n    server: https://first.invalid\ncontexts:\n- name: local\n  context:\n    cluster: local\n    user: local\nusers:\n- name: local\n  user:\n    tokenFile: token\n"
	if err := os.WriteFile(first, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte(strings.ReplaceAll(raw, "first.invalid", "second.invalid")), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", first+string(os.PathListSeparator)+second)
	config, err := Load(cfg())
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "https://first.invalid" || config.BearerTokenFile != "" {
		t.Fatal("merge or unsafe file reference", config.Host)
	}
	calls := 0
	transport := config.WrapTransport(fileRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Authorization") != "Bearer second" {
			t.Fatal("token not refreshed")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	}))
	if err := os.WriteFile(token, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "https://fixture.invalid", nil)
	response, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if req.Header.Get("Authorization") != "" {
		t.Fatal("mutated caller request")
	}
	if err := os.Remove(token); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(token, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); err == nil || calls != 1 {
		t.Fatal("replacement FIFO reached transport")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LoadContext(ctx, cfg()); err == nil {
		t.Fatal("cancelled config load succeeded")
	}
}

type fileRoundTripFunc func(*http.Request) (*http.Response, error)

func (f fileRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
