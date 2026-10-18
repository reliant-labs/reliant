// Copyright (c) 2025 Reliant Labs
package ratelimit

import (
	"strconv"
	"strings"
	"time"
)

// ParseResetTime reads a provider reset timestamp defensively: RFC 3339 (the
// Anthropic API's `anthropic-ratelimit-*-reset`), or a Unix epoch in seconds
// or milliseconds (the unified subscription header and Codex `*-reset-at`).
// A number too small to be a plausible epoch is rejected rather than guessed.
func ParseResetTime(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UTC(), true
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 1e9 {
		return time.Time{}, false
	}
	if f >= 1e12 {
		return time.UnixMilli(int64(f)).UTC(), true
	}
	return time.Unix(int64(f), 0).UTC(), true
}

// parseResetDuration reads a relative reset: a Go-style duration ("6m0s",
// "20ms", the OpenAI form) or plain seconds.
func parseResetDuration(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d, true
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
		return time.Duration(f * float64(time.Second)), true
	}
	return 0, false
}

func parseFloat(v string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return f, err == nil && f == f && f-f == 0 // finite
}

func parseInt(v string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return n, err == nil
}

// windowLength reads a duration out of a window name like "5h" or "7d".
func windowLength(name string) time.Duration {
	if len(name) < 2 {
		return 0
	}
	n, err := strconv.Atoi(name[:len(name)-1])
	if err != nil || n <= 0 {
		return 0
	}
	switch name[len(name)-1] {
	case 'm':
		return time.Duration(n) * time.Minute
	case 'h':
		return time.Duration(n) * time.Hour
	case 'd':
		return 24 * time.Hour * time.Duration(n)
	}
	return 0
}
