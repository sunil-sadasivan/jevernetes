package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
)

var hexID = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

func ValidID(id string) bool { return hexID.MatchString(id) }

type Incident struct {
	ID       string  `json:"id"`
	Count    int     `json:"recurrence_count"`
	Level    int     `json:"escalation_level"`
	Sequence int64   `json:"notification_sequence"`
	Decision *string `json:"last_decision"`
	Touched  int64   `json:"touched_at"`
}
type Status struct {
	Schema        int               `json:"schema"`
	Sampled       int64             `json:"sampled_at"`
	Ready         bool              `json:"ready"`
	Complete      bool              `json:"coverage_complete"`
	Metrics       map[string]uint64 `json:"metrics"`
	IncidentCount int               `json:"incident_count"`
	Pending       int               `json:"outbox_pending"`
	Dead          int               `json:"outbox_dead"`
	Recent        []Incident        `json:"recent_incidents"`
}
type Detail struct {
	Schema         int           `json:"schema"`
	Incident       Incident      `json:"incident"`
	Notification   *Notification `json:"last_notification"`
	DeliveryStatus *string       `json:"delivery_status"`
	Attempts       *int          `json:"delivery_attempts"`
}

func scanIncident(row interface{ Scan(...any) error }) (Incident, error) {
	var i Incident
	err := row.Scan(&i.ID, &i.Count, &i.Level, &i.Sequence, &i.Decision, &i.Touched)
	if err != nil {
		return i, err
	}
	if !ValidID(i.ID) || i.Count < 0 || i.Level < 0 || i.Sequence < 0 {
		return i, ErrState
	}
	return i, err
}
func (s *Store) Status(ctx context.Context) (Status, error) {
	out := Status{Schema: 1, Recent: []Incident{}}
	if !s.mu.TryLock() {
		return out, ErrBusy
	}
	defer s.mu.Unlock()
	if s.db.QueryRowContext(ctx, "SELECT count(*) FROM (SELECT 1 FROM incidents LIMIT 10001)").Scan(&out.IncidentCount) != nil || out.IncidentCount > 10000 {
		return out, ErrState
	}
	var total int
	if s.db.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(status='pending'),0),coalesce(sum(status='dead'),0) FROM (SELECT status FROM outbox LIMIT 10001)").Scan(&total, &out.Pending, &out.Dead) != nil || total > 10000 {
		return out, ErrState
	}
	rows, err := s.db.QueryContext(ctx, "SELECT substr(key,1,65),count,level,sequence,substr(last_decision,1,16),touched FROM incidents ORDER BY touched DESC LIMIT 20")
	if err != nil {
		return out, ErrState
	}
	defer rows.Close()
	for rows.Next() {
		i, err := scanIncident(rows)
		if err != nil {
			return out, ErrState
		}
		out.Recent = append(out.Recent, i)
	}
	if rows.Err() != nil {
		return out, ErrState
	}
	return out, nil
}
func (s *Store) Detail(ctx context.Context, id string) (*Detail, error) {
	if !ValidID(id) {
		return nil, errors.New("invalid incident id")
	}
	if !s.mu.TryLock() {
		return nil, ErrBusy
	}
	defer s.mu.Unlock()
	i, err := scanIncident(s.db.QueryRowContext(ctx, "SELECT key,count,level,sequence,last_decision,touched FROM incidents WHERE key=?", id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, ErrState
	}
	d := &Detail{Schema: 1, Incident: i}
	var raw, status string
	var attempts int
	err = s.db.QueryRowContext(ctx, "SELECT substr(payload,1,131073),status,attempts FROM outbox WHERE id IN (?,?) ORDER BY (id=?) DESC LIMIT 1", notificationID(id, i.Sequence), event.Hash([]any{id, i.Sequence}), notificationID(id, i.Sequence)).Scan(&raw, &status, &attempts)
	if err == sql.ErrNoRows {
		return d, nil
	}
	if err != nil || len(raw) > 131072 {
		return nil, ErrState
	}
	var n Notification
	if json.Unmarshal([]byte(raw), &n) != nil || !ValidID(n.ID) || !ValidID(n.IncidentID) {
		return nil, ErrState
	}
	d.Notification = &n
	d.DeliveryStatus = &status
	d.Attempts = &attempts
	return d, nil
}
