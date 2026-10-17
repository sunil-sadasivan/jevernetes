package event

import (
	"strings"
	"testing"
)

func TestDuplicateJSONPreservesRedactedEvidence(t *testing.T) {
	for _, raw := range []string{
		`{"level":"ERROR","level":"INFO"}`,
		`{"nested":[{"level":"ERROR","le\u0076el":"INFO"}]}`,
		`{"level":"ERROR","level":"INFO","token":"first-secret","token":"second-secret"}`,
		`{"level":"ERROR","level":"INFO","to\u006ben":{"value":"first-secret"},"token":["second-secret"]}`,
		`{"message":"ERROR Bearer first-secret","message":"INFO","nested":{"password":"second-secret"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			p := NewParser(Source{"type": "file"})
			p.Feed([]byte(raw + "\n"))
			e := p.Finish(false)[0]
			if !e.ParseUncertain || !e.Baseline.Important || !strings.Contains(e.Text, "ERROR") || !strings.Contains(e.Text, "INFO") {
				t.Fatal("duplicate evidence lost", e)
			}
			if len(e.Text) > MaxEvent || strings.Contains(e.Text, "first-secret") || strings.Contains(e.Text, "second-secret") {
				t.Fatal("unsafe duplicate evidence", e)
			}
			if strings.Contains(raw, "secret") && !e.Sensitive {
				t.Fatal("redaction not recorded", e)
			}
			again, _ := Redact(e.Text)
			if again != e.Text {
				t.Fatal("downstream redaction erased duplicates", again)
			}
		})
	}
	// Identical member names in separate objects are not ambiguous.
	p := NewParser(Source{"type": "file"})
	p.Feed([]byte(`[{"level":"ERROR"},{"level":"INFO"}]` + "\n"))
	if p.Finish(false)[0].ParseUncertain {
		t.Fatal("independent keys treated as duplicates")
	}
}

func TestDuplicateJSONBoundsAndContinuation(t *testing.T) {
	p := NewParser(Source{})
	p.Feed([]byte("INFO header\n  {\"level\":\"ERROR\",\"level\":\"INFO\"}\n"))
	if e := p.Finish(false)[0]; !e.ParseUncertain || !e.Baseline.Important {
		t.Fatal(e)
	}
	p = NewParser(Source{})
	p.Feed([]byte(`{"level":"ERROR","level":"INFO","data":"` + strings.Repeat("x", MaxEvent) + `","token":"first-secret"}` + "\n"))
	if e := p.Finish(false)[0]; !e.Truncated || !e.ParseUncertain || !e.Sensitive || len(e.Text) > MaxEvent || strings.Contains(e.Text, "first-secret") {
		t.Fatal("unsafe bounded event")
	}
}
