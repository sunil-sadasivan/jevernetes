package event

import (
	"encoding/json"
	"errors"
	"strings"
)

// redactJSON walks valid JSON without maps: duplicate members (including
// escaped key aliases) remain evidence, and every secret value is redacted.
// The raw result is used only to detect redaction; it never leaves Redact.
func redactJSON(d *json.Decoder, depth int) (raw, safe string, duplicate bool, err error) {
	if depth > 128 {
		return "", "", false, errors.New("JSON nesting limit")
	}
	t, err := d.Token()
	if err != nil {
		return "", "", false, err
	}
	delim, container := t.(json.Delim)
	if !container {
		a, _ := json.Marshal(t)
		if s, ok := t.(string); ok {
			t = scrub(s)
		}
		b, _ := json.Marshal(t)
		return string(a), string(b), false, nil
	}
	var a, b strings.Builder
	a.WriteRune(rune(delim))
	b.WriteRune(rune(delim))
	seen := map[string]bool{}
	first := true
	for d.More() {
		if !first {
			a.WriteByte(',')
			b.WriteByte(',')
		}
		first = false
		secretField := false
		if delim == '{' {
			key, e := d.Token()
			if e != nil {
				return "", "", false, e
			}
			k := key.(string) // Caller has validated the complete JSON document.
			duplicate = duplicate || seen[k]
			seen[k] = true
			secretField = field.MatchString(k)
			encoded, _ := json.Marshal(k)
			a.Write(encoded)
			b.Write(encoded)
			a.WriteByte(':')
			b.WriteByte(':')
		}
		r, s, dup, e := redactJSON(d, depth+1)
		if e != nil {
			return "", "", false, e
		}
		duplicate = duplicate || dup
		if secretField {
			s = `"[REDACTED]"`
		}
		a.WriteString(r)
		b.WriteString(s)
	}
	end, err := d.Token()
	if err != nil {
		return "", "", false, err
	}
	a.WriteRune(rune(end.(json.Delim)))
	b.WriteRune(rune(end.(json.Delim)))
	return a.String(), b.String(), duplicate, nil
}
