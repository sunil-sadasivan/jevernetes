// Package event frames bounded input and removes recognizable secrets before analysis.
package event

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxLine = 65536
const MaxEvent = 16000
const MaxInput = 32 << 20

type Source map[string]any

type Judgment struct {
	Importance           string   `json:"importance"`
	Severity             string   `json:"severity"`
	Category             string   `json:"category"`
	ImportanceConfidence *float64 `json:"importance_confidence,omitempty"`
	SeverityConfidence   *float64 `json:"severity_confidence,omitempty"`
	CategoryConfidence   *float64 `json:"category_confidence,omitempty"`
	AnalysisError        string   `json:"analysis_error,omitempty"`
}

func Unknown(reason string) Judgment {
	return Judgment{Importance: "unknown", Severity: "unknown", Category: "unknown", AnalysisError: reason}
}
func Confidence(v float64) *float64 { return &v }
func (j Judgment) Reusable() bool {
	if j.AnalysisError != "" || (j.Importance != "routine" && j.Importance != "important") || j.Category == "unknown" || j.Category == "security" || j.Category == "fraud" {
		return false
	}
	for _, c := range []*float64{j.ImportanceConfidence, j.SeverityConfidence, j.CategoryConfidence} {
		if c == nil || !(*c >= 0.7 && *c <= 1) {
			return false
		}
	}
	return true
}

type Baseline struct {
	Important bool     `json:"important"`
	Signals   []string `json:"signals"`
}
type Assessment struct {
	Dispatched        bool   `json:"dispatched"`
	TemplateID        string `json:"template_id"`
	VersionID         string `json:"version_id"`
	WindowOccurrences int    `json:"window_occurrences"`
	SampleCount       int    `json:"sample_count"`
	EvidenceLimited   bool   `json:"evidence_limited"`
	SamplesLimited    bool   `json:"samples_limited"`
	Reason            string `json:"reason"`
}
type Event struct {
	ID             string  `json:"id"`
	Source         Source  `json:"source"`
	Timestamp      *string `json:"timestamp"`
	Text           string  `json:"text"`
	LineStart      uint64  `json:"line_start"`
	LineEnd        uint64  `json:"line_end"`
	LineCount      uint64  `json:"line_count"`
	Truncated      bool    `json:"truncated"`
	Sensitive      bool    `json:"sensitive"`
	ParseUncertain bool    `json:"parse_uncertain,omitempty"`
	GroupID        string  `json:"group_id"`
	Judgment
	Baseline         Baseline    `json:"baseline"`
	AnalysisReused   bool        `json:"analysis_reused"`
	Assessment       *Assessment `json:"group_assessment,omitempty"`
	RepresentativeID string      `json:"analysis_representative_id,omitempty"`
}

func Hash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

var ansi = regexp.MustCompile("\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\))")
var stamp = regexp.MustCompile(`^\d{4}-\d\d-\d\d[T ][\d:.]+(?:Z|[+-]\d\d:\d\d)?[ \t]`)
var continuation = regexp.MustCompile(`^(?:\s+\S|Caused by:|Suppressed:|Traceback |[\w.]+(?:Error|Exception):|\s*\.\.\. \d+ more)`)
var field = regexp.MustCompile(`(?i)password|passwd|secret|token|authorization|cookie|api[-_]?key|access[-_]?key|private[-_]?key|credential`)
var secret = regexp.MustCompile(`(?i)(["']?(?:password|passwd|secret|(?:access_|refresh_)?token|api[-_]?key|access[-_]?key(?:[-_]?id)?|private[-_]?key|authorization|cookie|credentials?)["']?\s*[:=]\s*)("(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'|[^\s,;]+)`)
var auth = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+`)
var urlAuth = regexp.MustCompile(`(\b[a-zA-Z][a-zA-Z0-9+.-]{0,31}://)[^\s/@:]+:[^\s/@]+@`)
var jwt = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
var pem = regexp.MustCompile(`-----(BEGIN|END) (?:[A-Z0-9]+ )*PRIVATE KEY-----`)
var risky = regexp.MustCompile(`(?i)\b(error|fatal|panic|exception|critical|oomkilled|out of memory|crashloopbackoff)\b`)
var warn = regexp.MustCompile(`(?i)\b(warn|warning)\b`)
var http5 = regexp.MustCompile(`(?i)(?:status["\s:=]+|HTTP/\d(?:\.\d)?["\s]+)5\d\d\b`)
var security = regexp.MustCompile(`(?i)auth|credential|password|permission|privilege|security|fraud|attack|denied|forbidden|token|secret|payment|role`)

func SecurityShaped(s string) bool { return security.MatchString(s) }
func Console(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.ReplaceAllString(s, ""))
}
func scrub(s string) string {
	s = auth.ReplaceAllString(s, "$1 [REDACTED]")
	s = urlAuth.ReplaceAllString(s, "${1}[REDACTED]@")
	s = jwt.ReplaceAllString(s, "[REDACTED]")
	return secret.ReplaceAllString(s, `${1}"[REDACTED]"`)
}
func clean(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if field.MatchString(k) {
				x[k] = "[REDACTED]"
			} else {
				x[k] = clean(v)
			}
		}
	case []any:
		for i, v := range x {
			x[i] = clean(v)
		}
	case string:
		return scrub(x)
	}
	return v
}
func Redact(s string) (string, bool) {
	s = ansi.ReplaceAllString(s, "")
	var v any
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	if d.Decode(&v) == nil {
		var extra any
		if d.Decode(&extra) == io.EOF {
			a, _ := json.Marshal(v)
			b, _ := json.Marshal(clean(v))
			return string(b), string(a) != string(b)
		}
	}
	t := scrub(s)
	return t, t != s
}
func SafeSource(s Source) Source {
	b, _ := json.Marshal(s)
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	return Source(clean(v).(map[string]any))
}
func Rules(s string) Baseline {
	b := Baseline{Signals: []string{}}
	for _, r := range []struct {
		name string
		rx   *regexp.Regexp
	}{{"error_or_failure_keyword", risky}, {"http_5xx", http5}, {"warning_level", warn}} {
		if r.rx.MatchString(s) {
			b.Signals = append(b.Signals, r.name)
		}
	}
	b.Important = len(b.Signals) > 0
	return b
}
func Offline(e Event) Judgment {
	if e.Baseline.Important {
		return Judgment{Importance: "important", Severity: "degraded", Category: "unknown"}
	}
	return Judgment{Importance: "uncertain", Severity: "info", Category: "unknown"}
}

// Parser is owned by one producer. Private-key state survives line and read boundaries.
type Parser struct {
	source             Source
	sourceSensitive    bool
	pending            *Event
	line               uint64
	inKey              bool
	buffer             []byte
	truncated, private bool
	marker             string
	transport          bool
	digest             hash.Hash
	// AcceptLine sees a full raw physical-line digest, including discarded bytes.
	AcceptLine func(timestamp *time.Time, digest string, partial bool) bool
}

func NewParser(s Source) *Parser {
	safe := SafeSource(s)
	return &Parser{source: safe, sourceSensitive: Hash(s) != Hash(safe), transport: s["type"] == "kubernetes", digest: sha256.New()}
}
func (p *Parser) Feed(data []byte) []Event {
	var out []Event
	for len(data) > 0 {
		n := strings.IndexByte(string(data), '\n')
		end := len(data)
		if n >= 0 {
			end = n + 1
		}
		chunk := data[:end]
		p.digest.Write(chunk)
		data = data[end:]
		scan := p.marker + string(chunk)
		p.private = p.private || p.inKey
		for _, m := range pem.FindAllStringSubmatch(scan, -1) {
			p.private = true
			p.inKey = m[1] == "BEGIN"
		}
		if len(scan) > 128 {
			scan = scan[len(scan)-128:]
		}
		p.marker = scan
		room := MaxLine - len(p.buffer)
		take := min(room, len(chunk))
		p.buffer = append(p.buffer, chunk[:take]...)
		p.truncated = p.truncated || take < len(chunk)
		if n >= 0 {
			if e := p.finishLine(false); e != nil {
				out = append(out, *e)
			}
		}
	}
	return out
}
func (p *Parser) finishLine(partial bool) *Event {
	p.line++
	invalid := !utf8.Valid(p.buffer)
	text := strings.ToValidUTF8(strings.TrimRight(string(p.buffer), "\r\n"), "�")
	text = ansi.ReplaceAllString(text, "")
	trunc, private := p.truncated || partial, p.private
	p.buffer = nil
	p.truncated = false
	p.private = p.inKey
	p.marker = ""
	var ts *string
	transportStamp := false
	if p.transport {
		if i := strings.IndexByte(text, ' '); i > 0 {
			if t, err := time.Parse(time.RFC3339Nano, text[:i]); err == nil {
				raw := text[:i]
				ts = &raw
				text = text[i+1:]
				transportStamp = true
				if p.AcceptLine != nil && !p.AcceptLine(&t, hex.EncodeToString(p.digest.Sum(nil)), partial) {
					p.digest.Reset()
					return nil
				}
			}
		}
		if !transportStamp && p.AcceptLine != nil {
			p.AcceptLine(nil, hex.EncodeToString(p.digest.Sum(nil)), partial)
		}
	}
	p.digest.Reset()
	if prefix := stamp.FindString(text); prefix != "" && !transportStamp {
		s := strings.TrimSpace(prefix)
		ts = &s
		text = text[len(prefix):]
	}
	if text == "" && !private && !trunc {
		return nil
	}
	safe, changed := Redact(text)
	if private {
		safe = "[REDACTED PRIVATE KEY]"
		changed = true
	}
	if (ts == nil || transportStamp) && continuation.MatchString(text) && p.pending != nil {
		e := p.pending
		e.LineEnd = p.line
		e.LineCount++
		e.Sensitive = e.Sensitive || changed
		e.ParseUncertain = e.ParseUncertain || partial || invalid || (p.transport && !transportStamp)
		e.Truncated = e.Truncated || trunc || len(e.Text)+1+len(safe) > MaxEvent
		e.Text = Clip(e.Text+"\n"+safe, MaxEvent)
		return nil
	}
	orphan := continuation.MatchString(text) && p.pending == nil
	prev := p.Flush()
	p.pending = &Event{Source: p.source, Timestamp: ts, Text: Clip(safe, MaxEvent), LineStart: p.line, LineEnd: p.line, LineCount: 1, Truncated: trunc || len(safe) > MaxEvent, Sensitive: changed || p.sourceSensitive, ParseUncertain: partial || invalid || orphan || (p.transport && !transportStamp), Judgment: Unknown("")}
	return prev
}
func (p *Parser) Flush() *Event {
	e := p.pending
	p.pending = nil
	if e != nil {
		e.Baseline = Rules(e.Text)
		e.ID = Hash([]any{e.Source, e.Timestamp, e.LineStart, e.Text})[:20]
		e.GroupID = Hash([]any{e.Source, e.Text})[:20]
	}
	return e
}
func (p *Parser) Finish(partial bool) []Event {
	var out []Event
	if len(p.buffer) > 0 {
		p.truncated = p.truncated || partial
		if e := p.finishLine(partial || p.transport); e != nil {
			out = append(out, *e)
		}
	}
	if e := p.Flush(); e != nil {
		out = append(out, *e)
	}
	return out
}

var ErrLimit = errors.New("input byte limit reached")

// Read does not own r. Callers must close blocking streams on context cancellation.
func Read(ctx context.Context, r io.Reader, source Source, maxBytes int64, emit func(Event) error) error {
	if maxBytes < 1 {
		return errors.New("invalid input byte limit")
	}
	p := NewParser(source)
	r = bufio.NewReader(r)
	buf := make([]byte, 8192)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := r.Read(buf[:min(int64(len(buf)), maxBytes-total+1)])
		if int64(n)+total > maxBytes {
			n = int(maxBytes - total)
			for _, e := range p.Feed(buf[:n]) {
				if err := emit(e); err != nil {
					return err
				}
			}
			for _, e := range p.Finish(true) {
				if err := emit(e); err != nil {
					return err
				}
			}
			return ErrLimit
		}
		total += int64(n)
		for _, e := range p.Feed(buf[:n]) {
			if err := emit(e); err != nil {
				return err
			}
		}
		if err != nil {
			for _, e := range p.Finish(err != io.EOF) {
				if err := emit(e); err != nil {
					return err
				}
			}
			if err == io.EOF {
				return nil
			}
			return errors.New("input read failed")
		}
	}
}
