// Copyright (c) 2025 Reliant Labs
package commands

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
)

// TestManagedDaemonIsNeverInteractive is the second half of the 2026-09-30
// prod fix, and it is defence in depth rather than the root-cause fix.
//
// The root cause was a malformed credentials file (see
// internal/auth/managed_daemon_credentials_test.go). But what turned "the
// credential did not load" into a 30-minute user-visible outage was the
// FALLBACK: `daemon start` decided it was unregistered and ran the INTERACTIVE
// browser login inside a headless pod. It printed
//
//	Opening your browser to log in.
//	    https://admin.reliantapi.com/oauth/authorize?...
//
// into its container log, blocked 5 minutes on a browser that cannot exist,
// exited non-zero, and crash-looped — forever, because nothing about the
// situation changes on a restart.
//
// A managed daemon has no human at a keyboard and no browser, by construction.
// Interactive login is not merely unlikely to succeed there; it CANNOT succeed.
// So the platform-stated identity must imply non-interactive, and the existing
// await-credentials path (publish StreamAwaitingCredentials, poll disk) is the
// correct behaviour: it stays resident and recovers by itself the moment a
// valid credential is mounted, instead of burning a 5-minute timeout per
// restart and reporting nothing.
//
// This does NOT weaken the local CLI. `reliant daemon start` on a laptop sets
// no RELIANT_DAEMON_TYPE and keeps its interactive login.
func TestManagedDaemonIsNeverInteractive(t *testing.T) {
	cases := []struct {
		name         string
		daemonType   string
		explicitFlag bool // --non-interactive passed on the command line
		envFlag      string
		want         bool
	}{
		{
			name:       "managed pod implies non-interactive",
			daemonType: "managed",
			want:       true,
		},
		{
			name:       "cloud is the accepted synonym for managed",
			daemonType: "cloud",
			want:       true,
		},
		{
			name:       "a laptop keeps interactive login",
			daemonType: "",
			want:       false,
		},
		{
			name:       "self_hosted keeps interactive login",
			daemonType: "self_hosted",
			want:       false,
		},
		{
			name:       "managed cannot be forced interactive by env",
			daemonType: "managed",
			envFlag:    "false",
			want:       true,
		},
		{
			name:       "explicit --non-interactive still wins on a laptop",
			daemonType: "",
			envFlag:    "true",
			want:       true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.daemonType != "" {
				t.Setenv(daemonruntime.DaemonTypeEnvVar, tc.daemonType)
			}
			if tc.envFlag != "" {
				t.Setenv("RELIANT_DAEMON_NON_INTERACTIVE", tc.envFlag)
			}

			got := daemonNonInteractiveDefault()
			if got != tc.want {
				t.Errorf("daemonNonInteractiveDefault() = %v, want %v (daemon_type=%q env=%q)",
					got, tc.want, tc.daemonType, tc.envFlag)
			}
		})
	}
}
