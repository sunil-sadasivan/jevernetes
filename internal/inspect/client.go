package inspect

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/controller"
	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/kube"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

type Target struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
}
type Envelope struct {
	Sampled    int64  `json:"sampled_at"`
	Target     Target `json:"target"`
	Controller any    `json:"controller"`
}
type Client struct {
	http    *http.Client // Optional package-private transport for deterministic protocol tests.
	API     kube.API
	Forward kube.Forwarder
	Config  kube.Config
	Port    int
}

func (c *Client) Sample(ctx context.Context, id string) (Envelope, error) {
	out := Envelope{}
	if id != "" && !controller.ValidID(id) {
		return out, errors.New("invalid incident id")
	}
	if err := c.Config.Validate(); err != nil {
		return out, err
	}
	if c.Port < 1 || c.Port > 65535 {
		return out, errors.New("invalid inspection port")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	p, err := kube.SelectPod(ctx, c.API, c.Config)
	if err != nil {
		return out, err
	}
	t, err := c.Forward.Open(ctx, p.Namespace, p.Name, c.Port)
	if err != nil {
		return out, errors.New("controller connection failed")
	}
	defer t.Close()
	u, err := url.Parse(t.BaseURL())
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		return out, errors.New("invalid inspection tunnel")
	}
	path := "/v1/status"
	if id != "" {
		path = "/v1/incidents/" + strings.ToLower(id)
	}
	client := c.http
	if client == nil {
		client = provider.HTTPClient(20 * time.Second)
	}
	defer client.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String()+path, nil)
	resp, err := client.Do(req)
	if err != nil {
		return out, errors.New("controller inspection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return out, errors.New("controller inspection unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if err != nil || len(raw) > MaxResponse {
		return out, errors.New("controller response exceeds limit")
	}
	value, err := Validate(raw, id)
	if err != nil {
		return out, err
	}
	return Envelope{time.Now().Unix(), Target{p.Namespace, p.Name}, value}, nil
}

var metricName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)

func Validate(raw []byte, id string) (any, error) {
	bad := errors.New("invalid controller response")
	if len(raw) > MaxResponse {
		return nil, bad
	}
	validIncident := func(i controller.Incident) bool {
		return controller.ValidID(i.ID) && i.Count >= 0 && i.Count <= 1000000000 && i.Level >= 0 && i.Level <= 32 && i.Sequence >= 0 && (i.Decision == nil || *i.Decision == "notify" || *i.Decision == "review")
	}
	if id != "" {
		var d controller.Detail
		if (!provider.Required(raw, "schema", "incident")) || provider.Strict(raw, &d) != nil || d.Schema != 1 || !validIncident(d.Incident) || !strings.EqualFold(d.Incident.ID, id) {
			return nil, bad
		}
		if d.Notification != nil {
			n := d.Notification
			if n.Schema != 2 || !controller.ValidID(n.ID) || n.IncidentID != d.Incident.ID || !controller.ValidID(n.SourceID) || !eventID.MatchString(n.EventID) || n.Decision != "notify" && n.Decision != "review" || !validJudgment(n.Judgment) {
				return nil, bad
			}
		}
		if d.DeliveryStatus != nil && *d.DeliveryStatus != "pending" && *d.DeliveryStatus != "delivered" && *d.DeliveryStatus != "dead" {
			return nil, bad
		}
		if d.Attempts != nil && (*d.Attempts < 0 || *d.Attempts > 8) {
			return nil, bad
		}
		return d, nil
	}
	var s controller.Status
	if (!provider.Required(raw, "schema", "sampled_at", "ready", "coverage_complete", "metrics", "incident_count", "outbox_pending", "outbox_dead", "recent_incidents")) || provider.Strict(raw, &s) != nil || s.Schema != 1 || s.Complete || s.IncidentCount < 0 || s.IncidentCount > 10000 || s.Pending < 0 || s.Pending > 10000 || s.Dead < 0 || s.Dead > 10000 || len(s.Recent) > 20 || len(s.Metrics) > 256 {
		return nil, bad
	}
	for k := range s.Metrics {
		if !metricName.MatchString(k) {
			return nil, bad
		}
	}
	for _, i := range s.Recent {
		if !validIncident(i) {
			return nil, bad
		}
	}
	return s, nil
}

var eventID = regexp.MustCompile(`^(?:[a-f0-9]{20}|[a-f0-9]{64})$`)

func validJudgment(j event.Judgment) bool {
	allowed := func(s string, items ...string) bool {
		for _, item := range items {
			if item == s {
				return true
			}
		}
		return false
	}
	if !allowed(j.Importance, "important", "routine", "uncertain", "unknown") || !allowed(j.Severity, "noise", "info", "degraded", "impact", "outage", "unknown") || !allowed(j.Category, "deploy", "capacity", "dependency", "security", "fraud", "data", "config", "transient", "routine", "unknown") {
		return false
	}
	if !allowed(j.AnalysisError, "", "provider analysis unavailable", "invalid provider judgment", "stale template generation", "group verdict requires independent review", "analysis cancelled", "migrated judgment requires review") {
		return false
	}
	for _, c := range []*float64{j.ImportanceConfidence, j.SeverityConfidence, j.CategoryConfidence} {
		if c != nil && !(*c >= 0 && *c <= 1) {
			return false
		}
	}
	return true
}
