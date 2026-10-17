package tui

import (
	"context"
	"errors"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"strings"
	"testing"
)

func TestTabsDetailsGroupingAndSafeRendering(t *testing.T) {
	m := New()
	m.Events = []event.Event{{ID: "a", Text: "ERROR database\n  trace\x1b]52;bad\a", Source: event.Source{"path": "fixture"}, Judgment: event.Judgment{Importance: "important"}}, {ID: "b", Text: "ERROR database\n  trace\x1b]52;bad\a", Source: event.Source{"path": "fixture"}, Judgment: event.Judgment{Importance: "important"}}, {ID: "c", Text: "INFO ready", Judgment: event.Judgment{Importance: "routine"}}}
	if len(m.Rows()) != 1 || m.Rows()[0].Count != 2 {
		t.Fatal("grouping")
	}
	m.Key("2")
	if len(m.Rows()) != 1 || m.Rows()[0].Event.ID != "c" {
		t.Fatal("routine tab")
	}
	m.Key("1")
	m.Key("\r")
	render := m.Render(100, 30)
	if !strings.Contains(render, "trace") || strings.Contains(render, "\x1b]52") {
		t.Fatal("unsafe/missing detail")
	}
	m.Key("f")
	m.Key("d")
	m.Key("b")
	_, submit := m.Key("\r")
	if !submit || m.Query != "db" {
		t.Fatal("search entry")
	}
}
func TestDetailsSurviveLiveEviction(t *testing.T) {
	m := New()
	m.Events = []event.Event{{ID: "original", Text: "original evidence", Judgment: event.Judgment{Importance: "important"}}}
	m.Key("\r")
	m.Update(map[string]any{"events": []event.Event{{ID: "replacement", Text: "replacement evidence", Judgment: event.Judgment{Importance: "important"}}}})
	render := m.Render(100, 30)
	if !strings.Contains(render, "original evidence") || strings.Contains(render, "replacement evidence") {
		t.Fatal("open detail changed on eviction")
	}
}
func TestUnicodeEditorModeBudgetAndDelete(t *testing.T) {
	m := New()
	m.Key("f")
	m.Key("a")
	m.Key("é")
	m.Key("c")
	m.Key("\x1b[D")
	m.Key("\x7f")
	m.Key("b")
	if m.Query != "abc" {
		t.Fatal(m.Query)
	}
	m.Key("\x1b[H")
	m.Key("\x1b[3~")
	if m.Query != "bc" {
		t.Fatal(m.Query)
	}
	m.Key("\t")
	m.Key("\x02")
	if m.Editing != "Ask Jev" || m.SearchBudget != 64 {
		t.Fatal(m.Editing, m.SearchBudget)
	}
	m.Key("\x15")
	if m.Query != "" || m.Cursor != 0 {
		t.Fatal("clear")
	}
}

func TestCollectionStatusSurvivesSearchMessages(t *testing.T) {
	for _, tc := range []struct {
		outcome Completion
		want    string
	}{
		{Completion{}, "Collection complete"},
		{Completion{Partial: true}, "Collection stopped · partial coverage"},
		{Completion{Err: context.Canceled}, "Collection interrupted"},
		{Completion{Err: errors.New("sensitive transport detail")}, "Collection failed"},
	} {
		m := New()
		m.Done = true
		m.Outcome = tc.outcome
		m.Message = "Search finished"
		rendered := m.Render(180, 30)
		if !strings.Contains(rendered, tc.want) || strings.Contains(rendered, "sensitive transport detail") {
			t.Fatal("incorrect collection status", rendered)
		}
		if tc.want != "Collection complete" && strings.Contains(rendered, "Collection complete") {
			t.Fatal("false success", rendered)
		}
	}
}
