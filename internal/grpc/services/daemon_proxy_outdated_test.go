// Copyright (c) 2025 Reliant Labs
package services

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

// The production report was:
//
//	{"code":"internal","message":"open OAuth helper: daemon command
//	 \"auth.open_oauth_helper\" failed: unknown daemon command type:
//	 \"auth.open_oauth_helper\""}
//
// `internal` says Reliant is broken and invites a bug report. Nothing was
// broken: the user's daemon predated the PR that added the handler. These tests
// pin the classification that makes the difference visible to a client.

// outdatedDaemonErr is what SendDaemonCommand returns once the daemon's own
// message has crossed the transport and been wrapped by the NATS router. Built
// from the real wrapper text so the test would catch a change on either side of
// the string contract.
func outdatedDaemonErr() error {
	return errors.New(
		`daemon command "auth.open_oauth_helper" failed: this machine is running an ` +
			`older version of Reliant (v1.7.2) that doesn't support "auth.open_oauth_helper" ` +
			`— update it to continue`)
}

func TestIsDaemonOutdatedError(t *testing.T) {
	if !IsDaemonOutdatedError(outdatedDaemonErr()) {
		t.Error("a version-skew error was not recognised as one")
	}
	if IsDaemonOutdatedError(errors.New("working dir does not exist: /gone")) {
		t.Error("a missing-directory error was misread as version skew")
	}
	if IsDaemonOutdatedError(errors.New("no daemon connected")) {
		t.Error("a daemon-pending error was misread as version skew")
	}
	if IsDaemonOutdatedError(nil) {
		t.Error("nil was misread as version skew")
	}
}

// FailedPrecondition, specifically. Internal invites a bug report for something
// that is not a defect; Unavailable invites a retry that cannot succeed until
// the machine is updated.
func TestOutdatedDaemonMapsToFailedPrecondition(t *testing.T) {
	got := mapDaemonDispatchError("auth.open_oauth_helper", outdatedDaemonErr())

	if code := connect.CodeOf(got); code != connect.CodeFailedPrecondition {
		t.Errorf("code = %v, want %v", code, connect.CodeFailedPrecondition)
	}
	if code := connect.CodeOf(got); code == connect.CodeInternal {
		t.Error("version skew still reports as internal, the code users saw")
	}

	msg := got.Error()
	if !strings.Contains(msg, "older version") {
		t.Errorf("message lost the actionable cause: %q", msg)
	}
	if !strings.Contains(msg, "auth.open_oauth_helper") {
		t.Errorf("message lost the command name: %q", msg)
	}
	// The plumbing wrapper is not added on top of a message that already
	// explains itself.
	if strings.Contains(msg, "daemon command auth.open_oauth_helper failed") {
		t.Errorf("message was re-wrapped in plumbing vocabulary: %q", msg)
	}
}

// Other failure classes must keep the codes they had.
func TestNonSkewDispatchErrorsKeepTheirCodes(t *testing.T) {
	notFound := mapDaemonDispatchError("pkg.list_commands",
		errors.New("working dir does not exist: /daemon/workspace/gone"))
	if code := connect.CodeOf(notFound); code != connect.CodeNotFound {
		t.Errorf("missing dir: code = %v, want %v", code, connect.CodeNotFound)
	}

	unavailable := mapDaemonDispatchError("pkg.list_commands", errors.New("no daemon connected"))
	if code := connect.CodeOf(unavailable); code != connect.CodeUnavailable {
		t.Errorf("daemon pending: code = %v, want %v", code, connect.CodeUnavailable)
	}
}
