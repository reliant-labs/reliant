// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/version"
)

// A registry miss is ALWAYS version skew. Every command type is registered from
// an init() in this package, so the set a daemon serves is fixed at build time;
// the server only asks for commands it knows about. A miss therefore means the
// server is newer than this daemon.
//
// The error that reached a production user said only:
//
//	unknown daemon command type: "auth.open_oauth_helper"
//
// which names the symptom and hides the cause. These tests pin the properties
// that make the message actionable, not its exact wording.
func TestUnknownCommandErrorExplainsVersionSkew(t *testing.T) {
	r := NewCommandRegistry()

	_, err := r.Handle(context.Background(), "auth.open_oauth_helper", nil)
	if err == nil {
		t.Fatal("Handle on an unregistered command returned nil error")
	}
	msg := err.Error()

	// Leads with the cause the user can act on.
	if !strings.Contains(msg, "older version") {
		t.Errorf("error must say the daemon is out of date, got: %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "update") {
		t.Errorf("error must tell the user to update, got: %q", msg)
	}

	// Keeps the command name, so a bug report stays debuggable.
	if !strings.Contains(msg, "auth.open_oauth_helper") {
		t.Errorf("error must name the command, got: %q", msg)
	}

	// Reports the version it is, so "update it" can be checked against what is
	// installed rather than guessed at.
	if !strings.Contains(msg, version.Get().Version) {
		t.Errorf("error must report the daemon's version %q, got: %q", version.Get().Version, msg)
	}

	// The raw registry-lookup phrasing is what made the original report
	// unreadable; it must not be what the user sees.
	if strings.Contains(msg, "unknown daemon command type") {
		t.Errorf("error still leads with the raw registry miss, got: %q", msg)
	}
}

// A registered command must still dispatch normally — the new error path is
// only for misses.
func TestHandleDispatchesRegisteredCommand(t *testing.T) {
	r := NewCommandRegistry()
	r.Register("test.echo", func(_ context.Context, payload []byte) ([]byte, error) {
		return payload, nil
	})

	out, err := r.Handle(context.Background(), "test.echo", []byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("Handle on a registered command failed: %v", err)
	}
	if string(out) != `{"ok":true}` {
		t.Errorf("payload round-trip = %q, want %q", out, `{"ok":true}`)
	}
}
