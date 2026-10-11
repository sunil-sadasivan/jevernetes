package event

import (
	"fmt"
	"strings"
	"testing"
)

func TestStackContinuationParity(t *testing.T) {
	for _, source := range []string{"file", "kubernetes"} {
		for _, stamped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/timestamped=%v", source, stamped), func(t *testing.T) {
				lines := []string{"ERROR failed", "  at demo.run", "Caused by: upstream timeout", "INFO ready"}
				for i := range lines {
					if stamped {
						lines[i] = fmt.Sprintf("2026-01-01T00:00:0%dZ %s", i, lines[i])
					}
				}
				raw := strings.Join(lines, "\n") + "\n"
				for split := 1; split < len(raw); split++ {
					p := NewParser(Source{"type": source})
					out := p.Feed([]byte(raw[:split]))
					out = append(out, p.Feed([]byte(raw[split:]))...)
					out = append(out, p.Finish(false)...)
					if len(out) != 2 || out[0].LineCount != 3 || out[1].LineCount != 1 || out[0].LineStart != 1 || out[0].LineEnd != 3 || out[1].LineStart != 4 || out[0].Text != "ERROR failed\n  at demo.run\nCaused by: upstream timeout" {
						t.Fatalf("split %d: %+v", split, out)
					}
					if out[0].ParseUncertain != (source == "kubernetes" && !stamped) || out[0].Truncated || out[0].Sensitive {
						t.Fatal("incorrect safety flags", out)
					}
					if stamped && (out[0].Timestamp == nil || *out[0].Timestamp != "2026-01-01T00:00:00Z") {
						t.Fatal("lost header timestamp")
					}
				}
			})
		}
	}
}

func TestTimestampedContinuationSafety(t *testing.T) {
	for _, source := range []string{"file", "kubernetes"} {
		for _, body := range []string{"  at demo.run password=synthetic-secret", "Caused by: \xff", "  at " + strings.Repeat("x", MaxLine)} {
			p := NewParser(Source{"type": source})
			out := p.Feed([]byte("2026-01-01T00:00:00Z ERROR failed\n2026-01-01T00:00:01Z " + body + "\n"))
			out = append(out, p.Finish(false)...)
			if len(out) != 1 || out[0].LineCount != 2 || out[0].ParseUncertain != strings.Contains(body, "\xff") || out[0].Truncated != (len(body) > MaxLine) || strings.Contains(out[0].Text, "synthetic-secret") {
				t.Fatal("continuation safety lost", source, out)
			}
			if strings.Contains(body, "password") && !out[0].Sensitive {
				t.Fatal("redaction flag lost")
			}
		}
		for _, tail := range []string{"\n", ""} {
			p := NewParser(Source{"type": source})
			out := p.Feed([]byte("2026-01-01T00:00:00Z ERROR failed\n2026-01-01T00:00:01Z   at demo.run" + tail))
			out = append(out, p.Finish(true)...)
			if len(out) != 1 || out[0].LineCount != 2 || !out[0].Truncated || !out[0].ParseUncertain {
				t.Fatal("partial stack treated as complete", out)
			}
		}

		p := NewParser(Source{"type": source})
		out := p.Feed([]byte("2026-01-01T00:00:00Z   at orphan\n2026-01-01T00:00:01Z INFO ready\n2026-01-01T00:00:02Z  INFO unrelated\n"))
		out = append(out, p.Finish(false)...)
		if len(out) != 3 || !out[0].ParseUncertain || out[1].ParseUncertain || out[2].ParseUncertain {
			t.Fatal("orphan/normal framing", source, out)
		}
	}
}
