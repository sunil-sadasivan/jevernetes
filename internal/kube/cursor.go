package kube

import "time"

const cursorCapacity = 4096

// Cursor contains only raw-line SHA-256 digests at the latest transport time.
// Counts distinguish repeated physical lines at the same timestamp. It is never
// persisted and never uses redacted or parsed event identity.
type cursor struct {
	last           *time.Time
	replaying      bool
	counts, replay map[string]uint64
	gap            func(string)
}

func newCursor(gap func(string)) *cursor { return &cursor{counts: map[string]uint64{}, gap: gap} }
func (c *cursor) reconnect() {
	c.replaying = c.last != nil
	c.replay = make(map[string]uint64, len(c.counts))
	for k, v := range c.counts {
		c.replay[k] = v
	}
}
func (c *cursor) accept(t *time.Time, digest string, partial bool) bool {
	if t == nil {
		c.gap("cursor_untimestamped")
		return true
	}
	if partial {
		c.gap("cursor_truncated")
		return true
	}
	if c.last != nil && t.Before(*c.last) {
		// SinceTime serializes to whole seconds. While catching up, earlier
		// lines in the requested second necessarily precede our saved cursor.
		if c.replaying && !t.Before(c.last.Truncate(time.Second)) {
			return false
		}
		// Clock regressions and out-of-order transport cannot prove replay.
		c.gap("cursor_out_of_order")
		return true
	}
	if c.last == nil || t.After(*c.last) {
		stamp := *t
		c.last = &stamp
		c.counts = map[string]uint64{}
		c.replay = nil
		c.replaying = false
	}
	if c.replay[digest] > 0 {
		c.replay[digest]--
		if c.replay[digest] == 0 {
			delete(c.replay, digest)
		}
		if len(c.replay) == 0 {
			c.replaying = false
		}
		return false
	}
	if _, ok := c.counts[digest]; !ok && len(c.counts) >= cursorCapacity {
		c.gap("cursor_overflow")
		return true
	}
	if c.counts[digest] == ^uint64(0) {
		c.gap("cursor_overflow")
		return true
	}
	c.counts[digest]++
	return true
}
