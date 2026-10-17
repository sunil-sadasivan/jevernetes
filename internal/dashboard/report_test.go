package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sunil-sadasivan/jevernetes/internal/inspect"
)

func TestAllEvidencePagedWithEscapedByteBound(t *testing.T) {
	for _, schema := range []int{1, 2} {
		t.Run(fmt.Sprint(schema), func(t *testing.T) {
			events := []any{}
			for i := 0; i < 301; i++ {
				events = append(events, map[string]any{"id": fmt.Sprint(i), "text": fmt.Sprintf("event %d ", i) + strings.Repeat("<", 16000), "importance": "important"})
			}
			raw, _ := json.Marshal(map[string]any{"schema_version": schema, "summary": map[string]any{"events": 301}, "events": events, "important_groups": events})
			v, err := LoadReport(raw)
			if err != nil {
				t.Fatal(err)
			}
			h := (&Dashboard{Report: v}).Handler("127.0.0.1:8765")
			seen := map[string]bool{}
			offset := 0
			for {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:8765/api/report?offset=%d&limit=200", offset), nil))
				if w.Code != 200 || w.Body.Len() > inspect.MaxResponse {
					t.Fatal(w.Code, w.Body.Len())
				}
				var page struct {
					Events []struct{ ID, Text string }
					Page   struct {
						Next     *int `json:"next_offset"`
						Matched  int  `json:"matched_events"`
						Complete bool `json:"coverage_complete"`
					}
				}
				if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
					t.Fatal(err)
				}
				if page.Page.Matched != 301 || page.Page.Complete {
					t.Fatal(page.Page)
				}
				for _, e := range page.Events {
					if seen[e.ID] || !strings.HasSuffix(e.Text, strings.Repeat("<", 16000)) {
						t.Fatal("lost/duplicate evidence")
					}
					seen[e.ID] = true
				}
				if page.Page.Next == nil {
					break
				}
				if *page.Page.Next <= offset {
					t.Fatal("pagination stuck")
				}
				offset = *page.Page.Next
			}
			if len(seen) != 301 {
				t.Fatal(len(seen))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8765/api/report?q=event%200%20", nil))
			if !strings.Contains(w.Body.String(), `"matched_events":1`) {
				t.Fatal(w.Body.String()[:200])
			}
		})
	}
}
func TestReportQueriesRejectCommandsAndBadBounds(t *testing.T) {
	h := (&Dashboard{}).Handler("127.0.0.1:8765")
	for _, query := range []string{"url=http://example.invalid", "command=kubectl", "offset=-1", "limit=201", "limit=0", "q=a&q=b", "offset=1", "q=%zz"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8765/api/report?"+query, nil))
		if w.Code != 400 {
			t.Fatal(query, w.Code)
		}
	}
}
func TestOversizedImportedEvidenceIsExplicit(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"schema_version": 1, "summary": map[string]int{"events": 1}, "events": []any{map[string]string{"text": strings.Repeat("x", 300000)}}})
	v, err := LoadReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	v.page(w, httptest.NewRequest("GET", "http://local/api/report", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"unavailable_events":1`) || !strings.Contains(w.Body.String(), `"all_matches_in_page":false`) {
		t.Fatal(w.Code)
	}
}
func TestReportValidation(t *testing.T) {
	for _, raw := range []string{`{}`, `{"schema_version":1,"summary":{},"events":[{}]}`, `{"schema_version":1,"summary":{},"events":null}`, `{"schema_version":1,"schema_version":2,"summary":{},"events":[]}`} {
		if _, err := LoadReport([]byte(raw)); err == nil {
			t.Fatal("malformed accepted")
		}
	}
}

func TestSummaryCannotEscapePageByteBudgetOrClaimLegacyCompleteness(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"schema_version": 1, "summary": map[string]any{"complete_within_window": true}, "events": []any{}})
	v, err := LoadReport(raw)
	if err != nil || v.complete {
		t.Fatal("legacy completeness inferred", err)
	}
	// A compact input may expand six-fold under HTML-safe JSON serialization.
	raw = []byte(`{"schema_version":2,"summary":{"note":"` + strings.Repeat("<", 10000) + `"},"events":[]}`)
	if _, err := LoadReport(raw); err == nil {
		t.Fatal("escaped summary exceeded reserved page space")
	}
}
