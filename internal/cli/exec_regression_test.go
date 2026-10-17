package cli

import (
	"bytes"
	"context"
	"fmt"
	"github.com/sunil-sadasivan/jevernetes/internal/dashboard"
	"github.com/sunil-sadasivan/jevernetes/internal/kube"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestCLIAuthHelper(t *testing.T) {
	if os.Getenv("JEV_CLI_AUTH_HELPER") != "1" {
		return
	}
	_ = os.WriteFile(os.Getenv("JEV_CLI_AUTH_MARKER"), []byte(strconv.Itoa(os.Getpid())), 0600)
	for {
		time.Sleep(time.Hour)
	}
}
func TestCLIExecAuthShutdown(t *testing.T) {
	for _, mode := range []string{"duration", "k8s-signal", "remote-signal", "dashboard-handler"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "helper.pid")
			path := filepath.Join(dir, "config")
			raw := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: local
clusters:
- name: local
  cluster:
    server: https://fixture.invalid
contexts:
- name: local
  context:
    cluster: local
    user: local
users:
- name: local
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: %q
      args: ["-test.run=^TestCLIAuthHelper$"]
      interactiveMode: Never
      env:
      - name: JEV_CLI_AUTH_HELPER
        value: "1"
      - name: JEV_CLI_AUTH_MARKER
        value: %q
`, os.Args[0], marker)
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}

			if mode == "dashboard-handler" {
				t.Setenv("KUBECONFIG", path)
				life, cancel := context.WithCancel(context.Background())
				defer cancel()
				remote, err := remoteClient(life, kube.Config{Context: "local", Namespace: "local", MaxBytes: 1024, MaxStreams: 1}, 9091)
				if err != nil {
					t.Fatal(err)
				}
				d := &dashboard.Dashboard{Remote: remote}
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest("GET", "http://127.0.0.1:12345/api/controller/status", nil)
				done := make(chan struct{})
				go func() { defer close(done); d.Handler("127.0.0.1:12345").ServeHTTP(recorder, request) }()
				var pid int
				for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
					raw, _ := os.ReadFile(marker)
					pid, _ = strconv.Atoi(string(raw))
					if pid > 0 {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if pid == 0 {
					t.Fatal("dashboard helper did not start")
				}
				defer syscall.Kill(pid, syscall.SIGKILL)
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("dashboard auth failed to join")
				}
				if recorder.Code != 503 || syscall.Kill(pid, 0) != syscall.ESRCH {
					t.Fatal("dashboard shutdown left live auth or valid cache")
				}
				return
			}
			args := []string{"k8s", "--namespace", "local", "--offline", "--json"}
			if mode == "duration" {
				args = append(args, "--duration", "1")
			}
			if mode == "remote-signal" {
				args = []string{"remote", "--namespace", "local", "--context", "local", "--json"}
			}
			outer, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(outer, os.Args[0], append([]string{"-test.run=^TestUnsafeOpenChild$", "--"}, args...)...)
			cmd.Env = []string{"JEV_OPEN_CHILD=1", "KUBECONFIG=" + path, "GORACE=atexit_sleep_ms=0"}
			var out, stderr bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			var pid int
			for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
				raw, _ := os.ReadFile(marker)
				pid, _ = strconv.Atoi(string(raw))
				if pid > 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if pid == 0 {
				_ = cmd.Process.Kill()
				<-done
				t.Fatal("helper did not start")
			}
			defer syscall.Kill(pid, syscall.SIGKILL)
			if mode != "duration" {
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = cmd.Process.Kill()
				_ = syscall.Kill(pid, syscall.SIGKILL)
				<-done
				t.Fatal("CLI failed to terminate and join helper")
			}
			if mode != "duration" && cmd.ProcessState.ExitCode() != 130 {
				t.Fatalf("signal exit=%d", cmd.ProcessState.ExitCode())
			}
			if syscall.Kill(pid, 0) != syscall.ESRCH {
				t.Fatal("helper survived CLI shutdown")
			}
			if bytes.Contains(stderr.Bytes(), []byte(marker)) {
				t.Fatal("helper details leaked to CLI logs")
			}
		})
	}
}
