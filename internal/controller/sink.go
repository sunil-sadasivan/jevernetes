package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sunil-sadasivan/jevernetes/internal/provider"
)

type Sink interface {
	Send(context.Context, string, []byte) error
}
type StdoutSink struct{ Writer io.Writer }

func (s StdoutSink) Send(ctx context.Context, id string, payload []byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	_, err := s.Writer.Write(append(append([]byte{}, payload...), '\n'))
	return err
}

type Webhook struct {
	url    string
	client *http.Client
}

func NewWebhook(raw string) (*Webhook, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(raw) > 4096 || strings.ContainsAny(raw, "\r\n") {
		return nil, errors.New("webhook requires an HTTPS URL without userinfo or fragment")
	}
	return &Webhook{raw, provider.HTTPClient(10 * time.Second)}, nil
}
func (w *Webhook) Send(ctx context.Context, id string, payload []byte) error {
	if !ValidID(id) || len(payload) > 131072 {
		return errors.New("invalid notification")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", w.url, bytes.NewReader(payload))
	if err != nil {
		return errors.New("webhook request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", id)
	resp, err := w.client.Do(req)
	if err != nil {
		return errors.New("webhook delivery failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return errors.New("webhook delivery rejected")
	}
	return nil
}

type Pending struct {
	ID       string
	Payload  []byte
	Attempts int
}

// Pending records attempts before sending. A crash can repeat delivery, so sinks
// receive a stable idempotency key. One writer and delivery loop own each store.
func (s *Store) Pending(ctx context.Context, now int64) (*Pending, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrState
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id,substr(payload,1,131073),attempts FROM outbox WHERE status='pending' AND due<=? ORDER BY due,id LIMIT 1", now)
	if err != nil {
		return nil, ErrState
	}
	if !rows.Next() {
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, ErrState
		}
		return nil, nil
	}
	var p Pending
	if rows.Scan(&p.ID, &p.Payload, &p.Attempts) != nil {
		rows.Close()
		return nil, ErrState
	}
	rows.Close()
	if !ValidID(p.ID) || len(p.Payload) > 131072 || p.Attempts < 0 || p.Attempts > 8 {
		return nil, ErrState
	}
	var n Notification
	if json.Unmarshal(p.Payload, &n) != nil || n.ID != p.ID {
		return nil, ErrState
	}
	if p.Attempts == 8 {
		if _, err = tx.ExecContext(ctx, "UPDATE outbox SET status='dead',updated=? WHERE id=?", now, p.ID); err != nil {
			return nil, ErrState
		}
		if tx.Commit() != nil {
			return nil, ErrState
		}
		return nil, nil
	}
	p.Attempts++
	if _, err = tx.ExecContext(ctx, "UPDATE outbox SET attempts=?,due=?,updated=? WHERE id=?", p.Attempts, now+30, now, p.ID); err != nil {
		return nil, ErrState
	}
	if tx.Commit() != nil {
		return nil, ErrState
	}
	return &p, nil
}
func (s *Store) Delivered(ctx context.Context, p Pending, ok bool, now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := "pending"
	if ok {
		status = "delivered"
	} else if p.Attempts >= 8 {
		status = "dead"
	}
	delay := int64(1 << min(p.Attempts, 8))
	_, err := s.db.ExecContext(ctx, "UPDATE outbox SET status=?,due=?,updated=? WHERE id=? AND attempts=?", status, now+delay, now, p.ID, p.Attempts)
	if err != nil {
		return ErrState
	}
	return nil
}
func (s *Store) Deliver(ctx context.Context, sink Sink) error {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		p, err := s.Pending(ctx, time.Now().Unix())
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if p == nil {
			continue
		}
		err = sink.Send(ctx, p.ID, p.Payload)
		if ctx.Err() != nil {
			return nil
		}
		if e := s.Delivered(ctx, *p, err == nil, time.Now().Unix()); e != nil {
			return e
		}
	}
}
