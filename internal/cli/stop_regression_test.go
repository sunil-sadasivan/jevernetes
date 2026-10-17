package cli

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/controller"
	"github.com/sunil-sadasivan/jevernetes/internal/kube"
	"modernc.org/sqlite"
)

var transactionFixtureID atomic.Uint64

func privateState(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "state.db"), filepath.Join(dir, "report.json")
}

func assertDurableAdmissions(t *testing.T, state, report string, want int) {
	t.Helper()
	if got := len(savedEvents(t, report)); got != want {
		t.Errorf("reported admissions = %d, want %d", got, want)
	}
	db, err := sql.Open("sqlite", "file:"+state+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow("SELECT count(*) FROM seen").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("durable admissions = %d, want %d", got, want)
	}
	// Shutdown must also release the controller's exclusive writer lock.
	s, err := controller.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestControllerCancellationInsideFirstApplyDrains(t *testing.T) {
	state, report := privateState(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admitted := make(chan struct{})
	var calls atomic.Int32
	name := fmt.Sprintf("cancel_apply_%d", transactionFixtureID.Add(1))
	// The trigger executes inside Store.Apply's first INSERT transaction, after
	// runWith has selected its persistence context. No timing/sleep race is needed.
	if err := sqlite.RegisterScalarFunction(name, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		if calls.Add(1) == 1 {
			select {
			case <-admitted:
			case <-time.After(5 * time.Second):
				return nil, errors.New("admission stalled")
			}
			cancel()
		}
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	s, err := controller.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER cancel_first_apply BEFORE INSERT ON seen BEGIN SELECT " + name + "(); END"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	deps := runtimeDeps{collector: func(kube.Config) (logCollector, error) {
		return admittedCollector{quietCollector: quietCollector{count: 4}, admitted: admitted}, nil
	}}
	cmd := newCommand(strings.NewReader(""), io.Discard, io.Discard, deps)
	cmd.SetArgs([]string{"controller", "--offline", "--namespace", "demo", "--state", state, "--listen", "127.0.0.1:0", "--output", report, "--batch-size", "1", "--queue-size", "4"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel exit: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("persistence shutdown did not join")
	}
	if calls.Load() == 0 {
		t.Fatal("did not cancel inside transaction")
	}
	assertDurableAdmissions(t, state, report, 4)
}

func TestControllerEvidenceFailureDoesNotHold(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "synthetic")
	for _, failure := range []string{"failed", "blocked"} {
		t.Run(failure, func(t *testing.T) {
			state, report := privateState(t)
			admitted, started, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			deps := providerDeps(&delayedJudge{started: started, release: release})
			deps.collector = func(kube.Config) (logCollector, error) {
				return admittedCollector{quietCollector: quietCollector{count: 4}, admitted: admitted}, nil
			}
			var writer io.Writer = failedOutput{}
			blocked := &blockedEvidence{}
			if failure == "blocked" {
				writer = blocked
			}
			cmd := newCommand(strings.NewReader(""), io.Discard, writer, deps)
			cmd.SetArgs([]string{"controller", "--namespace", "demo", "--state", state, "--listen", "127.0.0.1:0", "--hold-after-stop", "--output", report, "--batch-size", "2", "--queue-size", "4"})
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- cmd.ExecuteContext(ctx) }()
			for _, ready := range []chan struct{}{admitted, started} {
				select {
				case <-ready:
				case <-ctx.Done():
					t.Fatal("fixture stalled")
				}
			}
			close(release)
			err := <-done
			if err == nil || !strings.Contains(err.Error(), "cannot write evidence output") || ctx.Err() != nil {
				t.Errorf("output failure entered hold or lost error: %v", err)
			}
			assertDurableAdmissions(t, state, report, 4)
			if failure == "blocked" && blocked.calls != 1 {
				t.Errorf("blocked writes = %d", blocked.calls)
			}
		})
	}
}

func TestControllerNotReadyWhileProviderDrains(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "synthetic")
	state, report := privateState(t)
	started, release, finish, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	deps := providerDeps(&delayedJudge{started: started, release: release})
	deps.collector = func(kube.Config) (logCollector, error) {
		return quietCollector{count: 1, stopped: stopped, delivered: finish}, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	cmd := newCommand(strings.NewReader(""), io.Discard, io.Discard, deps)
	cmd.SetArgs([]string{"controller", "--namespace", "demo", "--state", state, "--listen", address, "--output", report, "--batch-size", "1"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("provider not started")
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	status := func() int {
		r, e := client.Get("http://" + address + "/readyz")
		if e != nil {
			t.Fatal(e)
		}
		defer r.Body.Close()
		return r.StatusCode
	}
	if got := status(); got != http.StatusOK {
		t.Fatalf("active readiness = %d", got)
	}
	close(finish)
	<-stopped
	deadline := time.Now().Add(time.Second)
	for status() != http.StatusServiceUnavailable {
		if time.Now().After(deadline) {
			t.Fatal("readiness stayed true during provider drain")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
}
