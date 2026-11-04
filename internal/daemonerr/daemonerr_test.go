// Copyright (c) 2025 Reliant Labs
package daemonerr

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/toolexec"
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

func TestIsOutdated(t *testing.T) {
	if !IsOutdated(outdatedDaemonErr()) {
		t.Error("a version-skew error was not recognised as one")
	}
	if IsOutdated(errors.New("working dir does not exist: /gone")) {
		t.Error("a missing-directory error was misread as version skew")
	}
	if IsOutdated(errors.New("no daemon connected")) {
		t.Error("a daemon-pending error was misread as version skew")
	}
	if IsOutdated(nil) {
		t.Error("nil was misread as version skew")
	}
}

// FailedPrecondition, specifically. Internal invites a bug report for something
// that is not a defect; Unavailable invites a retry that cannot succeed until
// the machine is updated.
func TestOutdatedDaemonMapsToFailedPrecondition(t *testing.T) {
	got := DispatchConnectError("auth.open_oauth_helper", outdatedDaemonErr())

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
	notFound := DispatchConnectError("pkg.list_commands",
		errors.New("working dir does not exist: /daemon/workspace/gone"))
	if code := connect.CodeOf(notFound); code != connect.CodeNotFound {
		t.Errorf("missing dir: code = %v, want %v", code, connect.CodeNotFound)
	}

	unavailable := DispatchConnectError("pkg.list_commands", errors.New("no daemon connected"))
	if code := connect.CodeOf(unavailable); code != connect.CodeUnavailable {
		t.Errorf("daemon pending: code = %v, want %v", code, connect.CodeUnavailable)
	}
}

// A path the daemon reports missing on its own disk is NotFound and a user
// error, whatever command asked: prod logged one user's forge.topology and
// fs.get_tree polls for a folder only their laptop had as 70 ERROR issue
// events in two days (2026-10-23..25), because only pkg's marker was known.
func TestDaemonReportedMissingPathIsNotFoundAndUserClass(t *testing.T) {
	for _, report := range []error{
		toolexec.DaemonReportedFailure("forge.topology", "forge project dir does not exist: /Users/someone/src/app"),
		toolexec.DaemonReportedFailure("fs.get_tree", "read dir /Users/someone/src/app: open /Users/someone/src/app: no such file or directory"),
	} {
		if c := svcerr.Classify(report); c != svcerr.ClassUser {
			t.Errorf("%v: raw router error class = %v, want user", report, c)
		}
		cerr := DispatchConnectError("forge.topology", report)
		if code := connect.CodeOf(cerr); code != connect.CodeNotFound {
			t.Errorf("%v: DispatchConnectError code = %v, want NotFound", report, code)
		}
		if c := svcerr.Classify(cerr); c != svcerr.ClassUser {
			t.Errorf("%v: DispatchConnectError class = %v, want user", report, c)
		}
		if code := svcerr.Code(Error("daemon command forge.topology", report)); code != connect.CodeNotFound {
			t.Errorf("%v: Error code = %v, want NotFound", report, code)
		}
	}
}

// Every other daemon failure keeps its code-derived class: a daemon that
// could not start a program (fork/exec ENOENT) is our install, not the
// user's files, and an arbitrary failure says nothing about fault.
func TestDaemonReportedOtherFailuresStayServerClass(t *testing.T) {
	for _, report := range []error{
		toolexec.DaemonReportedFailure("search.grep", "fork/exec /usr/bin/rg: no such file or directory"),
		toolexec.DaemonReportedFailure("forge.topology", "forge command failed: exit status 1"),
	} {
		cerr := DispatchConnectError("x", report)
		if code := connect.CodeOf(cerr); code != connect.CodeUnavailable {
			t.Errorf("%v: code = %v, want Unavailable", report, code)
		}
		if c := svcerr.Classify(cerr); c != svcerr.ClassServer {
			t.Errorf("%v: class = %v, want server", report, c)
		}
	}
}
