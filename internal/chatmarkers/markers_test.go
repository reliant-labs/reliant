// Copyright (c) 2025 Reliant Labs
package chatmarkers

import (
	"strings"
	"testing"
)

// TestKindLiterals_DriftGuard hard-codes the wire-contract strings for every
// exported Kind*. The TS mirror in reliant/web/src/lib/chatMarkers.ts hard-codes
// the SAME strings in its own drift-guard test. If a rename slips through one
// side without the other, both this test and the vitest sibling fail loudly.
//
// DO NOT change these literals to reference the constants — the whole point is
// to catch accidental renames of the constants themselves.
func TestKindLiterals_DriftGuard(t *testing.T) {
	cases := []struct {
		name string
		got  Kind
		want string
	}{
		{"reliant managed quota exhausted", KindReliantManagedQuotaExhausted, "RELIANT_MANAGED_QUOTA_EXHAUSTED"},
		{"daemon offline halt", KindDaemonOfflineHalt, "RELIANT_DAEMON_OFFLINE_HALT"},
	}
	for _, tc := range cases {
		if string(tc.got) != tc.want {
			t.Errorf("%s: Kind literal drift: got %q, want %q (TS mirror in reliant/web/src/lib/chatMarkers.ts must match)",
				tc.name, string(tc.got), tc.want)
		}
	}
}

func TestWrap(t *testing.T) {
	cases := []struct {
		name    string
		kind    Kind
		payload string
		message string
		want    string
	}{
		{
			name:    "quota with URL payload",
			kind:    KindReliantManagedQuotaExhausted,
			payload: "/billing/plans",
			message: "Free tier exceeded",
			want:    "Free tier exceeded [RELIANT_MANAGED_QUOTA_EXHAUSTED:/billing/plans]",
		},
		{
			name:    "daemon offline with integer payload",
			kind:    KindDaemonOfflineHalt,
			payload: "3",
			message: "daemon offline for 3 consecutive turns",
			want:    "daemon offline for 3 consecutive turns [RELIANT_DAEMON_OFFLINE_HALT:3]",
		},
		{
			name:    "empty message produces bare marker",
			kind:    KindReliantManagedQuotaExhausted,
			payload: "/billing/plans",
			message: "",
			want:    "[RELIANT_MANAGED_QUOTA_EXHAUSTED:/billing/plans]",
		},
		{
			name:    "message whitespace is trimmed",
			kind:    KindDaemonOfflineHalt,
			payload: "3",
			message: "  trailing newline\n",
			want:    "trailing newline [RELIANT_DAEMON_OFFLINE_HALT:3]",
		},
		{
			name:    "empty payload still valid wire shape",
			kind:    KindDaemonOfflineHalt,
			payload: "",
			message: "halt",
			want:    "halt [RELIANT_DAEMON_OFFLINE_HALT:]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Wrap(tc.kind, tc.payload, tc.message)
			if got != tc.want {
				t.Errorf("Wrap = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtract(t *testing.T) {
	cases := []struct {
		name        string
		msg         string
		wantFound   bool
		wantKind    Kind
		wantPayload string
	}{
		{
			name:        "quota marker with URL",
			msg:         "Free tier exceeded [RELIANT_MANAGED_QUOTA_EXHAUSTED:/billing/plans]",
			wantFound:   true,
			wantKind:    KindReliantManagedQuotaExhausted,
			wantPayload: "/billing/plans",
		},
		{
			name:        "daemon halt marker with integer",
			msg:         "daemon offline for 3 consecutive turns; halting workflow. Reconnect the workspace and start a new turn. [RELIANT_DAEMON_OFFLINE_HALT:3]",
			wantFound:   true,
			wantKind:    KindDaemonOfflineHalt,
			wantPayload: "3",
		},
		{
			name:        "marker with full URL payload",
			msg:         "quota gone [RELIANT_MANAGED_QUOTA_EXHAUSTED:https://example.com/upgrade?x=1]",
			wantFound:   true,
			wantKind:    KindReliantManagedQuotaExhausted,
			wantPayload: "https://example.com/upgrade?x=1",
		},
		{
			name:      "no marker",
			msg:       "just a regular error message",
			wantFound: false,
		},
		{
			name:      "empty message",
			msg:       "",
			wantFound: false,
		},
		{
			name:        "marker at start of string",
			msg:         "[RELIANT_DAEMON_OFFLINE_HALT:5]",
			wantFound:   true,
			wantKind:    KindDaemonOfflineHalt,
			wantPayload: "5",
		},
		{
			name:        "first marker wins when two present",
			msg:         "a [RELIANT_MANAGED_QUOTA_EXHAUSTED:/p] b [RELIANT_DAEMON_OFFLINE_HALT:7]",
			wantFound:   true,
			wantKind:    KindReliantManagedQuotaExhausted,
			wantPayload: "/p",
		},
		{
			name:      "bracketed non-marker text ignored",
			msg:       "user typed [oops not a marker] in the prompt",
			wantFound: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, payload, found := Extract(tc.msg)
			if found != tc.wantFound {
				t.Fatalf("Extract found = %v, want %v", found, tc.wantFound)
			}
			if !tc.wantFound {
				return
			}
			if kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
			if payload != tc.wantPayload {
				t.Errorf("payload = %q, want %q", payload, tc.wantPayload)
			}
		})
	}
}

func TestStrip(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want string
	}{
		{
			name: "quota marker with URL",
			msg:  "Free tier exceeded [RELIANT_MANAGED_QUOTA_EXHAUSTED:/billing/plans]",
			want: "Free tier exceeded",
		},
		{
			name: "daemon halt marker",
			msg:  "daemon offline for 3 consecutive turns; halting workflow. Reconnect the workspace and start a new turn. [RELIANT_DAEMON_OFFLINE_HALT:3]",
			want: "daemon offline for 3 consecutive turns; halting workflow. Reconnect the workspace and start a new turn.",
		},
		{
			name: "no marker preserved",
			msg:  "just a regular error message",
			want: "just a regular error message",
		},
		{
			name: "empty stays empty",
			msg:  "",
			want: "",
		},
		{
			name: "leading whitespace before marker is also consumed",
			msg:  "message    [RELIANT_DAEMON_OFFLINE_HALT:1]",
			want: "message",
		},
		{
			name: "bare marker strips to empty",
			msg:  "[RELIANT_MANAGED_QUOTA_EXHAUSTED:/p]",
			want: "",
		},
		{
			name: "trailing whitespace trimmed",
			msg:  "  message [RELIANT_DAEMON_OFFLINE_HALT:1]   ",
			want: "message",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Strip(tc.msg)
			if got != tc.want {
				t.Errorf("Strip = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRoundTrip verifies Wrap → Extract → Strip composes correctly: the
// payload survives, and Strip recovers the original message body.
func TestRoundTrip(t *testing.T) {
	cases := []struct {
		kind    Kind
		payload string
		message string
	}{
		{KindReliantManagedQuotaExhausted, "/billing/plans", "Free tier exceeded"},
		{KindDaemonOfflineHalt, "3", "daemon offline"},
		{KindReliantManagedQuotaExhausted, "https://example.com/upgrade", "quota gone"},
	}
	for _, tc := range cases {
		wrapped := Wrap(tc.kind, tc.payload, tc.message)
		kind, payload, found := Extract(wrapped)
		if !found {
			t.Fatalf("Extract(Wrap(...)) returned !found for kind=%s payload=%q", tc.kind, tc.payload)
		}
		if kind != tc.kind {
			t.Errorf("round-trip kind = %q, want %q", kind, tc.kind)
		}
		if payload != tc.payload {
			t.Errorf("round-trip payload = %q, want %q", payload, tc.payload)
		}
		stripped := Strip(wrapped)
		if !strings.Contains(stripped, strings.TrimSpace(tc.message)) {
			t.Errorf("Strip = %q, want it to contain %q", stripped, strings.TrimSpace(tc.message))
		}
		if strings.Contains(stripped, string(tc.kind)) {
			t.Errorf("Strip = %q, should not contain kind %q", stripped, tc.kind)
		}
	}
}
