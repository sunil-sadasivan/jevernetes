package provider

import (
	"context"
	"encoding/json"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"io"
	"net/http"
	"strings"
	"testing"
)

type searchTransport func(*http.Request) (*http.Response, error)

func (f searchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSearchWireContractBudgetAndConservativeConfidence(t *testing.T) {
	client, err := New(Config{Kind: "typesafe", Model: "jev-latest", InputPrice: .042, MaxCost: .01, MaxAttempts: 1, MaxBatches: 1}, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	client.http = &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
		var value map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&value) != nil {
			t.Fatal("invalid request")
		}
		var questions map[string]json.RawMessage
		_ = json.Unmarshal(value["questions"], &questions)
		if len(questions) != 1 || questions["e0_relevance"] == nil {
			t.Fatal("missing question attribution")
		}
		if !strings.Contains(string(value["state"]), "first_seen") {
			t.Fatal("missing occurrence evidence")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"answers":{"e0_relevance":{"type":"choice","choice":"unrelated","confidence":0.4}},"usage":{"input_tokens":1,"output_tokens":1}}`)), Header: make(http.Header)}, nil
	})}
	v, err := client.Search(context.Background(), "database", []event.Event{{ID: "a", Text: "database timeout", SearchOccurrences: &event.Occurrences{Count: 2}}})
	if err != nil || len(v) != 1 || v[0].Relevance != "possible" {
		t.Fatal(v, err)
	}
	_, err = client.Search(context.Background(), "database", []event.Event{{ID: "b"}})
	if err == nil || client.Usage().Attempts != 1 {
		t.Fatal("budget escaped")
	}
}
