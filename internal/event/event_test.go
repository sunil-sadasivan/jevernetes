package event

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func parse(t *testing.T, s string, limit int64) ([]Event, error) {
	t.Helper()
	var out []Event
	err := Read(context.Background(), strings.NewReader(s), Source{"type": "file", "path": "synthetic"}, limit, func(e Event) error { out = append(out, e); return nil })
	return out, err
}
func TestParserMultilineTimestampAndIDs(t *testing.T) {
	s := "2026-01-01T00:00:00Z ERROR failed\n  at demo.run\n2026-01-01T00:00:01Z  INFO ready\n"
	a, err := parse(t, s, MaxInput)
	if err != nil || len(a) != 2 || a[0].LineCount != 2 || a[1].LineStart != 3 || !a[0].Baseline.Important {
		t.Fatalf("bad framing: %#v %v", a, err)
	}
	b, _ := parse(t, s, MaxInput)
	if a[0].ID != b[0].ID {
		t.Fatal("unstable IDs")
	}
}
func TestRedaction(t *testing.T) {
	credentialURL := strings.Join([]string{"http://", "user", ":", "synthetic-secret", "@example.invalid/"}, "")
	for _, s := range []string{`password=synthetic-secret`, `{"nested":{"api_key":"synthetic-secret"}}`, `Authorization: Bearer synthetic-secret`, credentialURL} {
		safe, changed := Redact(s)
		if !changed || strings.Contains(safe, "synthetic-secret") {
			t.Fatalf("redaction failed: %q", safe)
		}
	}
}
func TestKeySuppressionAcrossChunksAndOversizedDiscard(t *testing.T) {
	p := NewParser(Source{})
	s := strings.Repeat("x", MaxLine+4) + "-----BEGIN PRIVATE KEY-----\nsynthetic-private-material\n-----END PRIVATE KEY-----\nINFO ready\n"
	var events []Event
	for i := 0; i < len(s); i += 7 {
		events = append(events, p.Feed([]byte(s[i:min(i+7, len(s))]))...)
	}
	events = append(events, p.Finish(false)...)
	if len(events) != 4 || !events[0].Truncated || !events[1].Sensitive || strings.Contains(events[1].Text, "synthetic-private") {
		t.Fatal("private key escaped framing")
	}
}
func TestBoundsAndUTF8(t *testing.T) {
	a, err := parse(t, strings.Repeat("é", MaxLine)+"\n", MaxInput)
	if err != nil || len(a) != 1 || !a[0].Truncated || len(a[0].Text) > MaxEvent || !utf8.ValidString(a[0].Text) {
		t.Fatal("bounds failure")
	}
	b, err := parse(t, "INFO abcdef", 8)
	if !errors.Is(err, ErrLimit) || len(b) != 1 || !b[0].Truncated {
		t.Fatal("byte limit must mark fragment")
	}
}
func TestCancellationAndBackpressure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Read(ctx, strings.NewReader("x"), Source{}, 32, func(Event) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	want := errors.New("stop")
	err := Read(context.Background(), strings.NewReader("a\nb\n"), Source{}, 32, func(Event) error { return want })
	if err != want {
		t.Fatal(err)
	}
}
func TestSourceRedactedAndConsoleSafe(t *testing.T) {
	s := SafeSource(Source{"authorization": "fixture", "nested": map[string]any{"password": "fixture"}})
	if strings.Contains(Hash(s), "fixture") {
		t.Fatal("invalid hash")
	}
	if s["authorization"] != "[REDACTED]" {
		t.Fatal(s)
	}
	if Console("\x1b[31mA\nB") != "A B" {
		t.Fatal("unsafe console")
	}
}
func TestConservativeOffline(t *testing.T) {
	for _, s := range []string{"INFO ready", "WARN queue", "status=503", "ERROR failure"} {
		e := Event{Text: s, Baseline: Rules(s)}
		j := Offline(e)
		if j.Reusable() || j.ImportanceConfidence != nil {
			t.Fatal("offline claims confidence")
		}
	}
}
func TestSensitiveSourceCannotSeedReuse(t *testing.T) {
	p := NewParser(Source{"type": "file", "authorization": "synthetic"})
	p.Feed([]byte("INFO ready\n"))
	events := p.Finish(false)
	if len(events) != 1 || !events[0].Sensitive || events[0].Source["authorization"] != "[REDACTED]" {
		t.Fatal("source privacy not propagated")
	}
}

func TestKubernetesTimestampedMultilineEveryChunkBoundary(t *testing.T) {
	raw := "2026-01-01T00:00:00Z ERROR failed\n2026-01-01T00:00:01Z   at demo.run\n2026-01-01T00:00:02Z Caused by: broken\n2026-01-01T00:00:03Z INFO ready\n"
	for split := 1; split < len(raw); split++ {
		p := NewParser(Source{"type": "kubernetes"})
		out := p.Feed([]byte(raw[:split]))
		out = append(out, p.Feed([]byte(raw[split:]))...)
		out = append(out, p.Finish(false)...)
		if len(out) != 2 || out[0].LineCount != 3 || *out[0].Timestamp != "2026-01-01T00:00:00Z" || out[0].Text != "ERROR failed\n  at demo.run\nCaused by: broken" || out[0].ParseUncertain {
			t.Fatalf("split %d: %#v", split, out)
		}
	}
}
func TestParseUncertaintyAndOversizedEmptyLine(t *testing.T) {
	for _, raw := range []string{"invalid transport\n", "2026-01-01T00:00:00Z \xff\n"} {
		p := NewParser(Source{"type": "kubernetes"})
		out := p.Feed([]byte(raw))
		out = append(out, p.Finish(false)...)
		if len(out) != 1 || !out[0].ParseUncertain {
			t.Fatal("missing uncertainty")
		}
	}
	p := NewParser(Source{})
	out := p.Feed([]byte(strings.Repeat("\r", MaxLine+10) + "\n"))
	out = append(out, p.Finish(false)...)
	if len(out) != 1 || !out[0].Truncated {
		t.Fatal("empty oversized physical line disappeared")
	}
}
