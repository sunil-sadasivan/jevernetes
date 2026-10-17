package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
)

func TestTypeSafeGroupAttributionAndDescriptiveCriteria(t *testing.T) {
	groups := []Dossier{{EvidenceID: "first", Source: "opaque-first", SourceMetadata: event.Source{"type": "file", "path": "first.log"}, Samples: []string{"INFO payment returned empty"}}, {EvidenceID: "second", Source: "opaque-second", Samples: []string{"ERROR retry recovered"}}}
	request := Request("typesafe", "fixture", groups).(map[string]any)
	questions := request["questions"].(map[string]any)
	for i := range groups {
		for _, name := range []string{"importance", "severity", "category"} {
			question := questions[fmt.Sprintf("g%d_%s", i, name)].(map[string]any)
			instruction := question["instructions"].(string)
			if !strings.Contains(instruction, fmt.Sprintf("only groups[%d]", i)) || !strings.Contains(instruction, "evidence_id="+groups[i].EvidenceID) || !strings.Contains(instruction, "Other groups are not evidence of its recovery") {
				t.Fatal(instruction)
			}
			for choice, description := range question["criteria"].(map[string]string) {
				if choice == description || len(description) < 10 {
					t.Fatal("non-descriptive criteria", choice)
				}
			}
		}
	}
	raw, _ := json.Marshal(request)
	if !strings.Contains(string(raw), "first.log") {
		t.Fatal("source attribution missing")
	}
}

// Exercise the actual HTTP serialization and response parser for all adapters.
// The injected transport changes only the network destination to this local
// server and asserts that production code still chose its fixed HTTPS endpoint.
func TestProviderHTTPContracts(t *testing.T) {
	for _, kind := range []string{"typesafe", "openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				endpoint, _ := url.Parse(endpoints[kind])
				if r.Method != "POST" || r.URL.Path != endpoint.Path || r.Header.Get("Content-Type") != "application/json" {
					t.Error("request contract")
				}
				if kind == "anthropic" {
					if r.Header.Get("x-api-key") != "synthetic" || r.Header.Get("anthropic-version") == "" {
						t.Error("anthropic headers")
					}
				} else if r.Header.Get("Authorization") != "Bearer synthetic" {
					t.Error("authorization contract")
				}
				raw, _ := io.ReadAll(io.LimitReader(r.Body, MaxResponse+1))
				var req map[string]any
				if json.Unmarshal(raw, &req) != nil || req["model"] != "fixture" || !strings.Contains(string(raw), "evidence-two") {
					t.Error("body contract")
				}
				if kind == "typesafe" {
					qs := req["questions"].(map[string]any)
					if len(qs) != 6 {
						t.Error("group count")
					}
					answers := map[string]any{}
					for i, importance := range []string{"important", "routine"} {
						for key, value := range map[string]string{"importance": importance, "severity": "info", "category": "routine"} {
							answers[fmt.Sprintf("g%d_%s", i, key)] = map[string]any{"type": "choice", "choice": value, "confidence": .95}
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers, "usage": map[string]int{"input_tokens": 10, "output_tokens": 10}})
				} else {
					good1, good2 := good(), good()
					good1.Importance = "important"
					typed, _ := json.Marshal(map[string]any{"judgments": []event.Judgment{good1, good2}})
					var env map[string]any
					_ = json.Unmarshal(envelope(kind, good()), &env)
					if kind == "openai" {
						env["output"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] = string(typed)
					} else {
						env["content"].([]any)[0].(map[string]any)["text"] = string(typed)
					}
					env["usage"] = map[string]int{"input_tokens": 10, "output_tokens": 10}
					_ = json.NewEncoder(w).Encode(env)
				}
			}))
			defer server.Close()
			c, err := New(Config{Kind: kind, Model: "fixture", MaxCost: 1, InputPrice: 1, OutputPrice: 1, MaxAttempts: 3}, "synthetic")
			if err != nil {
				t.Fatal(err)
			}
			local, _ := url.Parse(server.URL)
			transport := &http.Transport{Proxy: nil}
			defer transport.CloseIdleConnections()
			c.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != endpoints[kind] {
					t.Error("endpoint escaped")
				}
				copy := r.Clone(r.Context())
				copy.URL.Scheme = local.Scheme
				copy.URL.Host = local.Host
				return transport.RoundTrip(copy)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			js, err := c.Judge(ctx, []Dossier{{EvidenceID: "evidence-one", Samples: []string{"failure"}}, {EvidenceID: "evidence-two", Samples: []string{"recovered"}}})
			if err != nil || len(js) != 2 {
				t.Fatal(err)
			}
			if js[0].Importance != "important" || js[1].Importance != "routine" || calls != 1 || c.Usage().Metered != 1 {
				t.Fatal("response attribution or usage", js, c.Usage())
			}
		})
	}
}
