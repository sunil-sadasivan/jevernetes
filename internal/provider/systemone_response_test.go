package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
)

// Captured from api.typesafe.ai (jev-1.13.0): every choice answer carries the
// full option distribution. Rejecting it silently turned every verdict unknown.
const liveSystemOneResponse = `{"model":"jev-1.13.0","answers":{"g0_category":{"type":"choice","choice":"unknown","confidence":0.69,"probabilities":{"capacity":0.01,"config":0.0,"data":0.01,"dependency":0.06,"deploy":0.0,"fraud":0.0,"routine":0.08,"security":0.0,"transient":0.12,"unknown":0.72}},"g0_importance":{"type":"choice","choice":"important","confidence":0.3,"probabilities":{"important":0.53,"routine":0.07,"uncertain":0.4}},"g0_severity":{"type":"choice","choice":"degraded","confidence":0.47,"probabilities":{"degraded":0.58,"impact":0.28,"info":0.06,"noise":0.02,"outage":0.06}}},"usage":{"input_tokens":1328,"output_tokens":188}}`

func TestDecodeAcceptsSystemOneProbabilities(t *testing.T) {
	out, err := Decode("typesafe", []byte(liveSystemOneResponse), 1)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Importance != "important" || out[0].Severity != "degraded" || out[0].Category != "unknown" || out[0].ImportanceConfidence == nil || *out[0].ImportanceConfidence != .3 {
		t.Fatalf("unexpected judgment %+v", out[0])
	}
	for _, bad := range []string{
		strings.Replace(liveSystemOneResponse, `"important":0.53`, `"important":1.53`, 1),
		strings.Replace(liveSystemOneResponse, `"confidence":0.3,`, ``, 1),
		strings.Replace(liveSystemOneResponse, `"probabilities":{"important"`, `"extra":1,"probabilities":{"important"`, 1),
	} {
		if _, err := Decode("typesafe", []byte(bad), 1); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestSearchAcceptsSystemOneProbabilities(t *testing.T) {
	c, err := New(Config{Kind: "typesafe", Model: "jev-latest", MaxCost: 1, MaxAttempts: 10}, "key")
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = searchTransport(func(*http.Request) (*http.Response, error) {
		body := `{"model":"jev-1.13.0","answers":{"e0_relevance":{"type":"choice","choice":"match","confidence":0.91,"probabilities":{"match":0.91,"possible":0.07,"unrelated":0.02}}},"usage":{"input_tokens":1,"output_tokens":1}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	out, err := c.Search(context.Background(), "errors", []event.Event{{ID: "a", Text: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Relevance != "match" {
		t.Fatalf("unexpected relevance %+v", out[0])
	}
}
