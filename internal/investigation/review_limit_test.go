package investigation

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestReviewLimitPreservesReadableFile(t *testing.T) {
	s := setup(t)
	ids := make([]string, 400000)
	for i := range ids {
		ids[i] = strings.Repeat("a", 32)
	}
	rules := object{"version": 1, "rules": []any{object{"id": "fixture", "pattern": "synthetic expected event", "scope": object{"type": "file", "path": "synthetic.log"}, "match": "contains", "enabled": true}}, "acknowledged": object{"history.json": ids}}
	before, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := json.MarshalIndent(rules, "", "  ")
	if err != nil || len(before) > 16<<20 || len(formatted)+1 <= 16<<20 {
		t.Fatal("fixture must fit compactly but overflow when written")
	}
	if err := os.WriteFile(s.rulesPath, before, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rules(); err != nil {
		t.Fatal(err)
	}
	w := request(s, "POST", "/api/review", object{"action": "toggle", "rule_id": "fixture", "enabled": false}, s.token)
	if w.Code != 400 {
		t.Fatalf("oversized write accepted: HTTP %d", w.Code)
	}
	after, err := os.ReadFile(s.rulesPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected write replaced existing review state")
	}
	if _, err := s.rules(); err != nil {
		t.Fatal("existing rules became unreadable", err)
	}
	if w := request(s, "GET", "/api/state", nil, ""); w.Code != 200 {
		t.Fatalf("dashboard unavailable after rejected write: %d", w.Code)
	}
}
