// Copyright (c) 2025 Reliant Labs
package providerusage

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
)

// flexFloat decodes a JSON number or numeric string. Anything else (null, an
// object, text) decodes to nil so one odd field never fails the response.
type flexFloat struct{ v *float64 }

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	f.v = nil
	s := strings.TrimSpace(string(bytes.Trim(bytes.TrimSpace(b), `"`)))
	if s == "" || s == "null" {
		return nil
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) {
		f.v = &n
	}
	return nil
}

func (f *flexFloat) get() (float64, bool) {
	if f == nil || f.v == nil {
		return 0, false
	}
	return *f.v, true
}

// flexTime decodes an RFC 3339 string or a Unix epoch (seconds or ms, number or
// string); anything else is the zero time.
type flexTime struct{ t time.Time }

func (f *flexTime) UnmarshalJSON(b []byte) error {
	f.t = time.Time{}
	s := strings.TrimSpace(string(bytes.Trim(bytes.TrimSpace(b), `"`)))
	if s == "" || s == "null" {
		return nil
	}
	if t, ok := ratelimit.ParseResetTime(s); ok {
		f.t = t
	}
	return nil
}

func (f *flexTime) get() time.Time {
	if f == nil {
		return time.Time{}
	}
	return f.t
}

// fraction turns a 0..100 percentage into a 0..1 used fraction, clamped.
func fraction(percent float64) float64 {
	return math.Min(1, math.Max(0, percent/100))
}

func ptr[T any](v T) *T { return &v }

// slug lowercases s and turns anything outside [a-z0-9] into "_" runs, for use
// inside a window name ("Fable 5.1" -> "fable_5_1").
func slug(s string) string {
	var b strings.Builder
	underscore := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			underscore = false
			continue
		}
		if !underscore && b.Len() > 0 {
			b.WriteByte('_')
			underscore = true
		}
	}
	return strings.TrimRight(b.String(), "_")
}

// anyExhausted reports whether any window with a known fraction is at or past 1.
func anyExhausted(ws []ratelimit.Window) bool {
	for _, w := range ws {
		if u, ok := w.Used(); ok && u >= 1 {
			return true
		}
	}
	return false
}

// jsonObject unmarshals body into a key -> raw value map; ok is false when body
// is not a JSON object.
func jsonObject(body []byte) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}
