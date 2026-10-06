package controller

import (
	"database/sql"
	"encoding/json"

	"github.com/sunil-sadasivan/jevernetes/internal/event"
	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

func migrateNotifications(tx *sql.Tx) error {
	rows, err := tx.Query("SELECT id,incident,substr(payload,1,131073) FROM outbox LIMIT 10001")
	if err != nil {
		return ErrState
	}
	type saved struct{ id, incident, raw string }
	values := []saved{}
	for rows.Next() {
		var v saved
		if rows.Scan(&v.id, &v.incident, &v.raw) != nil {
			rows.Close()
			return ErrState
		}
		values = append(values, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(values) > 10000 {
		return ErrState
	}
	for _, v := range values {
		if !ValidID(v.id) || !ValidID(v.incident) || len(v.raw) > 131072 {
			return ErrState
		}
		var prior struct {
			Schema   int            `json:"schema"`
			ID       string         `json:"notification_id"`
			Incident string         `json:"incident_id"`
			Decision string         `json:"decision"`
			Count    int            `json:"recurrence_count"`
			At       int64          `json:"observed_at"`
			Source   event.Source   `json:"source"`
			Judgment event.Judgment `json:"judgment"`
		}
		if json.Unmarshal([]byte(v.raw), &prior) != nil || prior.ID != v.id || prior.Incident != v.incident || (prior.Decision != "notify" && prior.Decision != "review") || prior.Count < 0 {
			return ErrState
		}
		j := prior.Judgment
		if provider.Validate(j) != nil {
			j = event.Unknown("migrated judgment requires review")
		}
		n := Notification{Schema: 2, ID: v.id, IncidentID: v.incident, EventID: event.Hash([]any{v.id, prior.At})[:20], SourceID: event.Hash(prior.Source), Decision: prior.Decision, Judgment: j, Occurrences: prior.Count, Created: prior.At}
		raw, _ := json.Marshal(n)
		if _, err = tx.Exec("UPDATE outbox SET payload=? WHERE id=?", string(raw), v.id); err != nil {
			return ErrState
		}
	}
	return nil
}
