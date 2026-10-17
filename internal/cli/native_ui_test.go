package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/kube"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompatibilityFlagsAndTUIValidation(t *testing.T) {
	for _, args := range [][]string{{"files", "-", "--offline", "--tui"}, {"files", "-", "--offline", "--workers", "17"}, {"k8s", "--offline", "-n", "demo", "-A"}, {"k8s", "--offline", "--discovery-interval", "0"}, {"k8s", "--offline", "--collect-workers", "33"}, {"dashboard", "--port", "65536"}} {
		code, _, _ := execute(t, args, "")
		if code == 0 {
			t.Fatal(args)
		}
	}
	code, v, err := execute(t, []string{"files", "-", "--offline", "--json", "--workers", "4", "--top", "5"}, "ERROR synthetic\n")
	if code != 0 || v["summary"].(map[string]any)["streams"] != float64(1) {
		t.Fatal(code, v, err)
	}
}
func TestDashboardScanHonorsHumanRulesAndMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic.log")
	_ = os.WriteFile(path, []byte("ERROR database unavailable\n"), 0600)
	rules := filepath.Join(dir, "rules.json")
	_ = os.WriteFile(rules, []byte(`{"version":1,"rules":[{"id":"rule","pattern":"database unavailable","match":"contains","enabled":true,"scope":{"type":"file","path":"synthetic.log"}}],"acknowledged":{}}`), 0600)
	v, err := scanDashboard(context.Background(), map[string]any{"kind": "files", "offline": true, "_review_rules": rules}, []string{path}, func(map[string]any) {})
	if err != nil {
		t.Fatal(err)
	}
	events := v["events"].([]event.Event)
	if len(events) != 1 || events[0].Review == nil || events[0].Importance != "routine" || events[0].Source["path"] != "synthetic.log" {
		t.Fatal(events)
	}
	files := v["scope"].(map[string]any)["files"].([]string)
	if len(files) != 1 || files[0] != "synthetic.log" {
		t.Fatal(files)
	}
}
func TestSchedulingFlagsReachCollector(t *testing.T) {
	var out bytes.Buffer
	seen := false
	cmd := newCommand(strings.NewReader(""), &out, io.Discard, runtimeDeps{collector: func(c kube.Config) (logCollector, error) {
		seen = c.SnapshotWorkers == 3 && c.DiscoveryInterval.Seconds() == 7 && c.Namespace == "demo" && c.Selector == "app=demo"
		return &emptyCollector{}, nil
	}})
	cmd.SetArgs([]string{"k8s", "--offline", "--json", "-n", "demo", "-l", "app=demo", "--collect-workers", "3", "--discovery-interval", "7"})
	_ = cmd.ExecuteContext(context.Background())
	var v map[string]any
	if !seen || json.Unmarshal(out.Bytes(), &v) != nil {
		t.Fatal("flags not propagated")
	}
}

type emptyCollector struct{}

func (*emptyCollector) Snapshot(context.Context, func(event.Event) error, func(event.Source, string)) error {
	return nil
}
func (*emptyCollector) Follow(context.Context, func(event.Event) error, func(event.Event) error, func(event.Source, string)) error {
	return nil
}
