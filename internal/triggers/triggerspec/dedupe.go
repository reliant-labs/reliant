// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// A webhook trigger may declare a dedupe_key: a template over `trigger` that
// names the real-world event a delivery is about. Senders with no idempotency
// header (Alertmanager re-sends a group on every repeat_interval) need it,
// because the default key, the body's hash within a one-minute bucket, treats
// the same alert re-sent ten minutes later as a new event.
//
//	dedupe_key: "{{ trigger.payload.body.groupKey }}:{{ trigger.payload.body.status }}"
//	dedupe_window: 4h
//
// Deliveries whose keys render the same within one window are one event.

const (
	// DefaultDedupeWindow holds a dedupe_key that names no window.
	DefaultDedupeWindow = time.Hour
	// MaxDedupeWindow bounds dedupe_window: a longer hold would suppress a
	// real recurrence for days.
	MaxDedupeWindow = 24 * time.Hour
	// MinDedupeWindow keeps a window from being smaller than the delivery
	// jitter it exists to absorb.
	MinDedupeWindow = time.Second
	// maxRenderedKey bounds a rendered key before it is hashed.
	maxRenderedKey = 4096
)

// DedupeError reports a dedupe_key or dedupe_window that cannot run.
type DedupeError struct{ Reason string }

func (e *DedupeError) Error() string { return "dedupe: " + e.Reason }

// ParseDedupeWindow reads dedupe_window; empty is DefaultDedupeWindow.
func ParseDedupeWindow(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultDedupeWindow, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, &DedupeError{Reason: fmt.Sprintf("dedupe_window %q is not a duration (e.g. 30m, 4h)", s)}
	}
	if d < MinDedupeWindow || d > MaxDedupeWindow {
		return 0, &DedupeError{Reason: fmt.Sprintf("dedupe_window %s is outside %s to %s", d, MinDedupeWindow, MaxDedupeWindow)}
	}
	return d, nil
}

// DedupeKeyExpressions returns the expressions in a dedupe_key template.
func DedupeKeyExpressions(key string) ([]string, error) { return InputExpressions(key) }

// CompileDedupe checks a dedupe_key template (every {{ }} compiles over
// `trigger`, activation included) and its window.
func CompileDedupe(key, window string) error {
	if strings.TrimSpace(key) == "" {
		if strings.TrimSpace(window) != "" {
			return &DedupeError{Reason: "dedupe_window needs a dedupe_key"}
		}
		return nil
	}
	if _, err := ParseDedupeWindow(window); err != nil {
		return err
	}
	exprs, err := DedupeKeyExpressions(key)
	if err != nil {
		return &DedupeError{Reason: err.Error()}
	}
	if len(exprs) == 0 {
		return &DedupeError{Reason: "dedupe_key has no {{ }} expression, so every delivery would share one key: name what identifies the event, e.g. {{ trigger.payload.body.groupKey }}"}
	}
	env, err := triggerEnv()
	if err != nil {
		return &DedupeError{Reason: "build environment: " + err.Error()}
	}
	for _, expr := range exprs {
		if _, issues := env.Compile(expr); issues != nil && issues.Err() != nil {
			return &DedupeError{Reason: fmt.Sprintf("{{ %s }}: %v", expr, issues.Err())}
		}
	}
	return nil
}

// RenderDedupeKey renders the template against the event and its activation.
// The result is the text the author's expressions produced, trimmed; empty is
// an error, because a blank key would make every delivery one event.
func RenderDedupeKey(key string, scope Scope) (string, error) {
	rendered, err := RenderPrompt(key, scope)
	if err != nil {
		return "", &DedupeError{Reason: strings.TrimPrefix(err.Error(), "prompt: ")}
	}
	rendered = strings.TrimSpace(rendered)
	if rendered == "" {
		return "", &DedupeError{Reason: "dedupe_key rendered empty"}
	}
	return rendered, nil
}

// DedupeToken is the stable, bounded form of a rendered key within the window
// holding now: a fixed clock bucket of that width (UTC epoch aligned), so the
// same key is the same token for the whole bucket and a new one after it.
func DedupeToken(rendered string, window time.Duration, now time.Time) string {
	if len(rendered) > maxRenderedKey {
		rendered = rendered[:maxRenderedKey]
	}
	bucket := now.UTC().Truncate(window).Unix()
	sum := sha256.Sum256([]byte(rendered))
	return fmt.Sprintf("dk:%d:%s", bucket, hex.EncodeToString(sum[:16]))
}
