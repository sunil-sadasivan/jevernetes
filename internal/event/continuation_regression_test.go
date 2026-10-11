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

func TestTimestampedStackContext(t *testing.T) {
	fixtures := [][]string{
		{"ERROR request failed", "Traceback (most recent call last):", `  File "app.py", line 7, in run`, "    do_work()", "ValueError: failed"},
		{"ERROR request failed", "Traceback (most recent call last):", `  File "app.py", line 7, in run`, "    do_work()", "    ^^^^^^^^^", "ValueError: failed"},
		{"ERROR request failed", "java.lang.RuntimeException: failed", "  at app.Main.run(Main.java:7)", "  Suppressed: java.io.IOException: failed", "    ... 2 more"},
		{"panic: failed", "goroutine 1 [running]:", "main.run()", "\t/app/main.go:7 +0x20", "main.main()", "\t/app/main.go:10 +0x30"},
		{"ERROR request failed", "System.InvalidOperationException: failed", "   at App.Run() in /app/App.cs:line 7", "   --- End of inner exception stack trace ---"},
	}
	for _, source := range []string{"file", "kubernetes"} {
		for n, lines := range fixtures {
			t.Run(fmt.Sprintf("%s/%d", source, n), func(t *testing.T) {
				raw := ""
				for i, line := range append(append([]string{}, lines...), " INFO unrelated", "INFO ready") {
					raw += fmt.Sprintf("2026-01-01T00:00:%02dZ %s\n", i, line)
				}
				for split := 1; split < len(raw); split++ {
					p := NewParser(Source{"type": source})
					out := p.Feed([]byte(raw[:split]))
					out = append(out, p.Feed([]byte(raw[split:]))...)
					out = append(out, p.Finish(false)...)
					if len(out) != 3 || out[0].LineCount != uint64(len(lines)) || out[0].LineStart != 1 || out[0].LineEnd != uint64(len(lines)) || out[1].LineCount != 1 || out[2].LineCount != 1 || out[0].Text != strings.Join(lines, "\n") {
						t.Fatalf("split=%d: %+v", split, out)
					}
					if out[0].Timestamp == nil || *out[0].Timestamp != "2026-01-01T00:00:00Z" {
						t.Fatal("header timestamp lost")
					}
				}
			})
		}
	}
}

func TestPythonSourceSafetyAndContextReset(t *testing.T) {
	for _, source := range []string{"file", "kubernetes"} {
		for _, body := range []string{"    do_work(password=synthetic-secret)", "    do_work(\xff)", "    " + strings.Repeat("x", MaxLine)} {
			raw := "2026-01-01T00:00:00Z ERROR failed\n2026-01-01T00:00:01Z Traceback (most recent call last):\n2026-01-01T00:00:02Z   File \"app.py\", line 7, in run\n2026-01-01T00:00:03Z " + body + "\n"
			// Byte-at-a-time input exercises timestamp, UTF-8 and redaction boundaries.
			p := NewParser(Source{"type": source})
			var out []Event
			for i := range len(raw) {
				out = append(out, p.Feed([]byte(raw[i:i+1]))...)
			}
			out = append(out, p.Finish(false)...)
			if len(out) != 1 || out[0].LineCount != 4 || out[0].ParseUncertain != strings.Contains(body, "\xff") || out[0].Truncated != (len(body) > MaxLine) || out[0].Sensitive != strings.Contains(body, "password") || strings.Contains(out[0].Text, "synthetic-secret") {
				t.Fatal("source safety lost", source, out)
			}
		}
		for _, partial := range []bool{false, true} {
			p := NewParser(Source{"type": source})
			raw := "2026-01-01T00:00:00Z   File \"app.py\", line 7, in run\n2026-01-01T00:00:01Z     do_work()\n"
			out := p.Feed([]byte(raw))
			out = append(out, p.Finish(partial)...)
			if len(out) != 1 || out[0].LineCount != 2 || !out[0].ParseUncertain || out[0].Truncated != partial {
				t.Fatal("orphan/partial source safety lost", out)
			}
			out = p.Feed([]byte("2026-01-01T00:00:02Z     unrelated text\n2026-01-01T00:00:03Z INFO ready\n"))
			out = append(out, p.Finish(false)...)
			if len(out) != 2 {
				t.Fatal("stack context survived flush", out)
			}
		}
		p := NewParser(Source{"type": source})
		out := p.Feed([]byte("2026-01-01T00:00:00Z ERROR failed\n2026-01-01T00:00:01Z   File \"app.py\", line 7, in run\n2026-01-01T00:00:02Z     INFO unrelated\n2026-01-01T00:00:03Z     not a source line\n"))
		out = append(out, p.Finish(false)...)
		if len(out) != 3 {
			t.Fatal("unrelated indented normal lines merged", out)
		}
	}
}

func TestPythonExactTimestampedFixture(t *testing.T) {
	raw := "2026-01-01T00:00:00Z ERROR request failed\n2026-01-01T00:00:01Z Traceback (most recent call last):\n2026-01-01T00:00:02Z   File \"app.py\", line 7, in run\n2026-01-01T00:00:03Z     do_work()\n2026-01-01T00:00:04Z ValueError: failed\n2026-01-01T00:00:05Z INFO ready\n"
	for _, source := range []string{"file", "kubernetes"} {
		p := NewParser(Source{"type": source})
		out := p.Feed([]byte(raw))
		out = append(out, p.Finish(false)...)
		if len(out) != 2 || out[0].LineCount != 5 || out[1].LineCount != 1 {
			t.Fatal("expected [5,1]", source, out)
		}
	}
}
