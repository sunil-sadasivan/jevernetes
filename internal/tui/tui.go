// Package tui implements an in-process terminal investigation view. The UI owns
// its terminal descriptor, search workers and raw-mode restoration.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/output"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
	"github.com/sunil-sadasivan/jevernetes/internal/search"
	"github.com/sunil-sadasivan/jevernetes/internal/terminal"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
	"golang.org/x/text/width"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var tabs = []string{"Important", "Routine", "Needs Review", "All", "Search"}

type row struct {
	Event event.Event
	Count int
	IDs   []string
}
type Model struct {
	SearchLabels                        map[string]string
	Cursor, SearchBudget                int
	FrozenDetail                        *row
	FrozenEvents                        []event.Event
	Events                              []event.Event
	Summary, Live                       map[string]any
	Tab, Selected, Scroll, DetailScroll int
	Grouped, Detail, Instances, Done    bool
	Outcome                             Completion
	Query, Editing, Message             string
	Results                             []event.Event
	Search                              *search.Run
	cache                               search.Cache
}

func New() *Model { return &Model{Grouped: true, SearchBudget: 16, Summary: map[string]any{}} }
func (m *Model) Update(v map[string]any) {
	selected := ""
	rows := m.Rows()
	if m.Selected >= 0 && m.Selected < len(rows) {
		selected = rows[m.Selected].Event.ID
	}
	defer func() {
		if selected != "" {
			for i, row := range m.Rows() {
				for _, id := range row.IDs {
					if id == selected {
						m.Selected = i
						return
					}
				}
			}
		}
	}()
	value := v["events"]
	if tail, ok := v["tail_events"]; ok {
		value = tail
	}
	raw, _ := json.Marshal(value)
	var events []event.Event
	if json.Unmarshal(raw, &events) == nil {
		m.Events = events
	}
	m.Summary, _ = v["summary"].(map[string]any)
	m.Live, _ = v["live"].(map[string]any)
}

// Pending counts retained events still awaiting a verdict.
func (m *Model) Pending() int {
	n := 0
	for _, e := range m.Events {
		if e.Importance == "pending" {
			n++
		}
	}
	return n
}

// emptyState explains an empty tab in terms of what collection is doing, so a
// live run never looks idle while streams open or verdicts are outstanding.
func (m *Model) emptyState() string {
	if m.Tab == 4 {
		return "No search results. Press / to ask Jev or f for exact text."
	}
	if len(m.Events) > 0 {
		msg := fmt.Sprintf("No %s events yet · %d retained", strings.ToLower(tabs[m.Tab]), len(m.Events))
		if pending := m.Pending(); pending > 0 {
			msg += fmt.Sprintf(" · %d awaiting verdict", pending)
		}
		return msg + " · press 4 for all events"
	}
	if !m.Done && m.Live != nil {
		return fmt.Sprintf("Opening log streams… %v active · waiting for the first log line", m.Live["active_streams"])
	}
	if !m.Done {
		return "Collecting… no events received yet"
	}
	return "No events in this view. Collection coverage may be partial."
}
func (m *Model) Rows() []row {
	events := m.Events
	if m.Tab == 4 {
		events = m.Results
	}
	out := []row{}
	by := map[string]int{}
	for _, e := range events {
		match := m.Tab >= 3 || m.Tab == 0 && e.Importance == "important" || m.Tab == 1 && e.Importance == "routine" || m.Tab == 2 && (e.Importance == "unknown" || e.Importance == "uncertain" || e.Importance == "pending")
		if !match {
			continue
		}
		key := event.Hash([]any{e.Source, e.Text, e.Importance, e.Severity, e.Category})
		if m.Grouped {
			if i, ok := by[key]; ok {
				out[i].Count++
				out[i].IDs = append(out[i].IDs, e.ID)
				continue
			}
			by[key] = len(out)
		}
		out = append(out, row{e, 1, []string{e.ID}})
	}
	return out
}
func cells(r rune) int {
	p := width.LookupRune(r).Kind()
	if p == width.EastAsianFullwidth || p == width.EastAsianWide {
		return 2
	}
	return 1
}
func fit(s string, n int) string {
	s = event.Console(s)
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := cells(r)
		if used+w > n {
			break
		}
		used += w
		b.WriteRune(r)
	}
	return b.String()
}
func wrap(s string, n int) []string {
	out := []string{}
	for _, line := range strings.Split(s, "\n") {
		line = event.Console(line)
		var b strings.Builder
		used := 0
		for _, r := range line {
			w := cells(r)
			if used+w > n {
				out = append(out, b.String())
				b.Reset()
				used = 0
			}
			b.WriteRune(r)
			used += w
		}
		out = append(out, b.String())
	}
	return out
}
func (m *Model) Render(w, h int) string {
	w = max(20, w)
	h = max(8, h)
	rows := m.Rows()
	m.Selected = max(0, min(m.Selected, len(rows)-1))
	lines := []string{"Jevernetes · Go · read-only log investigation"}
	status := "Collecting"
	if m.Done {
		switch {
		case errors.Is(m.Outcome.Err, context.Canceled):
			status = "Collection interrupted · browsing retained window"
		case m.Outcome.Err != nil:
			status = "Collection failed · press q for error"
		case m.Outcome.Partial:
			status = "Collection stopped · partial coverage"
		default:
			status = "Collection complete · browsing retained window"
		}
	}
	statusLine := fmt.Sprintf("%s | events=%v important=%v uncertain=%v unknown=%v", status, m.Summary["events"], m.Summary["important"], m.Summary["uncertain"], m.Summary["unknown"])
	if m.Live != nil && !m.Done {
		statusLine += fmt.Sprintf(" | streams=%v queue=%v dropped=%v", m.Live["active_streams"], m.Live["queue_depth"], m.Live["dropped"])
	}
	lines = append(lines, statusLine)
	labels := []string{}
	for i, t := range tabs {
		if i == m.Tab {
			t = "[" + t + "]"
		}
		labels = append(labels, fmt.Sprintf("%d %s", i+1, t))
	}
	lines = append(lines, strings.Join(labels, "  "), "Enter details · g group · i instances · / ask · f exact · x stop search · q quit")
	if m.Editing != "" {
		query := []rune(m.Query)
		m.Cursor = max(0, min(m.Cursor, len(query)))
		lines = append(lines, fmt.Sprintf("%s [%d batches · Tab mode · Ctrl+B budget · Ctrl+U clear]> %s█%s", m.Editing, m.SearchBudget, string(query[:m.Cursor]), string(query[m.Cursor:])))
	} else {
		lines = append(lines, m.Message)
	}
	height := h - 7
	if m.Detail && m.FrozenDetail != nil {
		selected := m.FrozenDetail
		e := selected.Event
		source, _ := json.Marshal(e.Source)
		title := fmt.Sprintf("%s/%s/%s · %d instances · %s", e.Importance, e.Severity, e.Category, selected.Count, e.ID)
		detail := append([]string{title, string(source)}, wrap(e.Text, w)...)
		if m.Instances {
			detail = append(detail, "Occurrence IDs:")
			ids := map[string]bool{}
			for _, id := range selected.IDs {
				ids[id] = true
			}
			events := m.FrozenEvents
			for _, instance := range events {
				if ids[instance.ID] {
					stamp := "no timestamp"
					if instance.Timestamp != nil {
						stamp = *instance.Timestamp
					}
					detail = append(detail, fmt.Sprintf("%s · %s · lines %d–%d", instance.ID, stamp, instance.LineStart, instance.LineEnd))
				}
			}
		}
		detail = append(detail, "Analysis: "+e.AnalysisError)
		m.DetailScroll = max(0, min(m.DetailScroll, max(0, len(detail)-height)))
		end := min(len(detail), m.DetailScroll+height)
		lines = append(lines, detail[m.DetailScroll:end]...)
	} else {
		m.Scroll = max(0, min(m.Scroll, m.Selected))
		if m.Selected >= m.Scroll+height {
			m.Scroll = m.Selected - height + 1
		}
		end := min(len(rows), m.Scroll+height)
		for i := m.Scroll; i < end; i++ {
			r := rows[i]
			mark := " "
			if i == m.Selected {
				mark = ">"
			}
			source := sourceLabel(r.Event.Source)
			label := r.Event.Importance
			if m.Tab == 4 {
				if value := m.SearchLabels[event.Hash([]any{r.Event.Source, r.Event.Text})]; value != "" {
					label = value
				}
			}
			lines = append(lines, fmt.Sprintf("%s ×%-4d %-12s %s %s", mark, r.Count, label, fit(source, min(30, w/3)), r.Event.Text))
		}
		if len(rows) == 0 {
			lines = append(lines, m.emptyState())
		}
	}
	for len(lines) < h-1 {
		lines = append(lines, "")
	}
	lines = append(lines, fmt.Sprintf("%d rows · retained %d · requests=%v cost=$%v · %s", len(rows), len(m.Events), m.Summary["api_requests"], m.Summary["total_estimated_cost_usd"], map[bool]string{true: "grouped", false: "instances"}[m.Grouped]))
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i, line := range lines[:min(len(lines), h)] {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString("\x1b[2K")
		b.WriteString(fit(line, w-1))
	}
	return b.String()
}

// Key updates navigation; submissions are returned to the asynchronous search owner.
func (m *Model) Key(key string) (quit, submit bool) {
	if m.Editing != "" {
		query := []rune(m.Query)
		m.Cursor = max(0, min(m.Cursor, len(query)))
		switch key {
		case "\x1b":
			m.Editing = ""
		case "\r", "\n":
			submit = true
		case "\x03":
			quit = true
		case "\t":
			if m.Editing == "Ask Jev" {
				m.Editing = "Exact text"
			} else {
				m.Editing = "Ask Jev"
			}
		case "\x15":
			query = nil
			m.Cursor = 0
		case "\x02":
			switch m.SearchBudget {
			case 4:
				m.SearchBudget = 16
			case 16:
				m.SearchBudget = 64
			default:
				m.SearchBudget = 4
			}
		case "\x1b[D":
			m.Cursor = max(0, m.Cursor-1)
		case "\x1b[C":
			m.Cursor = min(len(query), m.Cursor+1)
		case "\x1b[H", "\x1b[1~":
			m.Cursor = 0
		case "\x1b[F", "\x1b[4~":
			m.Cursor = len(query)
		case "\x7f", "\b":
			if m.Cursor > 0 {
				query = append(query[:m.Cursor-1], query[m.Cursor:]...)
				m.Cursor--
			}
		case "\x1b[3~":
			if m.Cursor < len(query) {
				query = append(query[:m.Cursor], query[m.Cursor+1:]...)
			}
		default:
			if len(query) < 500 && !strings.HasPrefix(key, "\x1b") && key >= " " {
				r := []rune(key)
				tail := append([]rune{}, query[m.Cursor:]...)
				query = append(query[:m.Cursor], r...)
				query = append(query, tail...)
				m.Cursor += len(r)
			}
		}
		m.Query = string(query)
		return
	}
	switch key {
	case "q", "Q", "\x03":
		return true, false
	case "\x1b":
		m.Detail = false
		m.Instances = false
	case "\t", "\x1b[C":
		m.Tab = (m.Tab + 1) % 5
		m.Selected = 0
		m.Detail = false
	case "\x1b[D", "\x1b[Z":
		m.Tab = (m.Tab + 4) % 5
		m.Selected = 0
		m.Detail = false
	case "1", "2", "3", "4", "5":
		m.Tab = int(key[0] - '1')
		m.Selected = 0
		m.Detail = false
	case "j", "\x1b[B":
		if m.Detail {
			m.DetailScroll++
		} else {
			m.Selected++
		}
	case "k", "\x1b[A":
		if m.Detail {
			m.DetailScroll = max(0, m.DetailScroll-1)
		} else {
			m.Selected = max(0, m.Selected-1)
		}
	case "\x1b[6~":
		if m.Detail {
			m.DetailScroll += 10
		} else {
			m.Selected += 10
		}
	case "\x1b[5~":
		if m.Detail {
			m.DetailScroll = max(0, m.DetailScroll-10)
		} else {
			m.Selected = max(0, m.Selected-10)
		}
	case "\r", "\n":
		m.Detail = !m.Detail
		if m.Detail {
			m.freezeDetail()
		}
		m.DetailScroll = 0
	case "\x1b[H", "\x1b[1~":
		m.Selected = 0
		m.DetailScroll = 0
	case "\x1b[F", "\x1b[4~":
		m.Selected = max(0, len(m.Rows())-1)
		m.DetailScroll = 1000000
	case "g":
		m.Grouped = !m.Grouped
		m.Selected = 0
	case "i":
		if !m.Detail {
			m.freezeDetail()
		}
		m.Detail = true
		m.Instances = !m.Instances
	case "f", "/", "?":
		m.Query = ""
		m.Cursor = 0
		m.Editing = "Exact text"
		if key == "/" || key == "?" {
			m.Editing = "Ask Jev"
		}
	}
	return
}

// Completion distinguishes a bounded/partial collection from an actual failure.
// Err is preserved for status classification; raw diagnostics are not rendered.
type Completion struct {
	Err     error
	Partial bool
}

type Options struct {
	Offline bool
	Client  func(context.Context, int) (search.Client, error)
}

func Run(ctx context.Context, tty *os.File, updates <-chan map[string]any, done <-chan Completion, options Options) (bool, error) {
	fd := int(tty.Fd())
	if !term.IsTerminal(fd) {
		return false, errors.New("--tui requires an interactive terminal")
	}
	var old *term.State
	session, err := terminal.Start(ctx, func() error {
		writeErr := output.Final(tty, func(w io.Writer) error {
			_, e := w.Write([]byte("\x1b[?1000l\x1b[?1006l\x1b[?25h\x1b[?1049l"))
			return e
		})
		var restoreErr error
		if old != nil {
			restoreErr = term.Restore(fd, old)
		}
		return errors.Join(writeErr, restoreErr)
	}, func() error {
		state, e := term.MakeRaw(fd)
		if e != nil {
			return errors.New("cannot enter terminal mode")
		}
		if old == nil {
			old = state
		}
		writeCtx, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		_, e = output.Write(writeCtx, tty, []byte("\x1b[?1049h\x1b[?25l\x1b[?1000h\x1b[?1006h"))
		return e
	})
	if err != nil {
		return false, err
	}
	defer session.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	defer workers.Wait()
	defer cancel()
	m := New()
	var stopSearch context.CancelFunc
	defer func() {
		if stopSearch != nil {
			stopSearch()
		}
	}()
	searchDone := make(chan *search.Run, 1)
	pending := ""
	lastInput := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case v, ok := <-updates:
			if ok {
				m.Update(v)
			} else {
				updates = nil
			}
		case outcome, ok := <-done:
			if ok {
				m.Outcome = outcome
				m.Done = true
			}
			done = nil
		case run := <-searchDone:
			if m.Search != run {
				continue
			}
			m.refreshSearch()
		default:
		}
		m.refreshSearch()
		err := session.Frame(ctx, func() error {
			w, h, err := term.GetSize(fd)
			if err != nil {
				w, h = 100, 30
			}
			writeCtx, stop := context.WithTimeout(ctx, time.Second)
			_, err = output.Write(writeCtx, tty, []byte(m.Render(w, h)))
			stop()
			if err != nil {
				return err
			}
			buf := make([]byte, 512)
			n, err := readInput(fd, buf)
			if err != nil {
				return err
			}
			if n > 0 {
				pending += string(buf[:n])
				lastInput = time.Now()
			} else {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(100 * time.Millisecond):
				}
			}
			return nil
		})
		if err != nil {
			return false, err
		}
		for pending != "" {
			key, rest, ok := nextKey(pending)
			if !ok {
				if time.Since(lastInput) > 150*time.Millisecond {
					key, rest, ok = "\x1b", pending[1:], true
				} else {
					break
				}
			}
			pending = rest
			if strings.HasPrefix(key, "\x1b[<") {
				var button, x, y int
				if strings.HasSuffix(key, "M") {
					_, _ = fmt.Sscanf(key, "\x1b[<%d;%d;%dM", &button, &x, &y)
					if button == 64 {
						m.Key("k")
					} else if button == 65 {
						m.Key("j")
					} else if button == 0 {
						if y == 3 {
							start := 1
							for i, label := range tabs {
								length := len(label) + 4
								if i == m.Tab {
									length += 2
								}
								if x >= start && x < start+length {
									m.Key(strconv.Itoa(i + 1))
									break
								}
								start += length
							}
						} else if y >= 6 {
							m.Selected = m.Scroll + y - 6
						}
					}
				}
				continue
			}
			if key == "\x03" {
				return false, context.Canceled
			}
			if key == "x" && m.Editing == "" && m.Tab == 4 && stopSearch != nil {
				stopSearch()
				m.Message = "Stopping search…"
				continue
			}
			quit, submit := m.Key(key)
			if quit {
				return true, nil
			}
			if submit {
				if stopSearch != nil {
					stopSearch()
				}
				mode := "literal"
				if m.Editing == "Ask Jev" {
					mode = "jev"
				}
				m.Editing = ""
				if mode == "jev" && options.Offline {
					m.Message = "Offline: press f for exact text search"
					continue
				}
				run, err := search.New(m.Events, m.Query, mode, m.SearchBudget, &m.cache)
				if err != nil {
					m.Message = err.Error()
					continue
				}
				var client search.Client
				if mode == "jev" && options.Client != nil {
					client, err = options.Client(ctx, m.SearchBudget)
					if err != nil {
						m.Message = err.Error()
						continue
					}
				}
				searchCtx, stop := context.WithCancel(ctx)
				stopSearch = stop
				m.Search = run
				m.Tab = 4
				m.Detail = false
				m.Results = nil
				m.Message = "Searching frozen retained window…"
				workers.Add(1)
				go func() {
					defer workers.Done()
					run.Execute(searchCtx, client)
					select {
					case searchDone <- run:
					case <-ctx.Done():
					}
				}()
			}
		}
	}
}

// readInput must run under the terminal frame lease. Readiness is only a hint:
// a blocking read after poll can stall rendering, cancellation and auth handoff.
// Use the syscall directly so os.File cannot turn EAGAIN into an unbounded wait,
// and restore descriptor flags before releasing the terminal to a login helper.
func readInput(fd int, buf []byte) (n int, err error) {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return 0, err
	}
	if err = unix.SetNonblock(fd, true); err != nil {
		return 0, err
	}
	defer func() {
		_, restoreErr := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags)
		err = errors.Join(err, restoreErr)
	}()
	n, err = unix.Read(fd, buf)
	if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
		return 0, nil
	}
	if n == 0 && err == nil {
		return 0, io.EOF
	}
	return n, err
}

func nextKey(s string) (string, string, bool) {
	if s[0] != 27 {
		_, n := utf8.DecodeRuneInString(s)
		if n == 1 && s[0] >= 128 && !utf8.FullRuneInString(s) {
			return "", s, false
		}
		return s[:n], s[n:], true
	}
	if len(s) == 1 {
		return "", s, false
	}
	if s[1] != '[' {
		return s[:1], s[1:], true
	}
	for i := 2; i < len(s); i++ {
		if s[i] >= 64 && s[i] <= 126 {
			return s[:i+1], s[i+1:], true
		}
	}
	if len(s) > 64 {
		return s[:1], s[1:], true
	}
	return "", s, false
}

func (m *Model) freezeDetail() {
	rows := m.Rows()
	if m.Selected < 0 || m.Selected >= len(rows) {
		return
	}
	r := rows[m.Selected]
	m.FrozenDetail = &r
	events := m.Events
	if m.Tab == 4 {
		events = m.Results
	}
	m.FrozenEvents = append([]event.Event{}, events...)
}

func (m *Model) refreshSearch() {
	run := m.Search
	if run == nil {
		return
	}
	snap := run.Snapshot(0)
	usage := snap["usage"].(provider.Usage)
	m.Message = fmt.Sprintf("Search: %s · %v/%v groups · %v cached · %d requests · $%.6f", snap["message"], snap["examined_groups"], snap["total_groups"], snap["cache_hits"], usage.Attempts, usage.Cost)
	m.SearchLabels = map[string]string{}
	labels := map[string]string{}
	m.Results = nil
	ids := map[string]bool{}
	for offset := 0; offset < snap["result_count"].(int); offset += 20 {
		for _, result := range run.Snapshot(offset)["results"].([]search.Result) {
			ids[result.EventID] = true
			label := result.Relevance.Relevance
			if result.Confidence != nil {
				label += fmt.Sprintf(" %.0f%%", *result.Confidence*100)
			}
			labels[result.EventID] = label
		}
	}
	keys := map[string]bool{}
	for _, e := range run.Events {
		if ids[e.ID] {
			keys[event.Hash([]any{e.Source, e.Text})] = true
			m.SearchLabels[event.Hash([]any{e.Source, e.Text})] = labels[e.ID]
		}
	}
	for _, e := range run.Events {
		if keys[event.Hash([]any{e.Source, e.Text})] {
			m.Results = append(m.Results, e)
		}
	}
}

func sourceLabel(source event.Source) string {
	if source["type"] == "kubernetes" {
		return fmt.Sprintf("%v/%v/%v", source["namespace"], source["pod"], source["container"])
	}
	if path, ok := source["path"].(string); ok {
		return filepath.Base(path)
	}
	return "unknown source"
}
