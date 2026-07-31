// Copyright (c) 2025 Reliant Labs
package commands

import (
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/auth"
)

func TestDaemonCredsExpiringSoon(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { tt := now.Add(d); return &tt }

	cases := []struct {
		name  string
		creds *auth.DaemonCredentials
		want  bool
	}{
		{"nil creds", nil, false},
		{"no expiry (daemon-kind PAT)", &auth.DaemonCredentials{PAT: "rlnt_pat_x"}, false},
		{"already expired", &auth.DaemonCredentials{ExpiresAt: at(-time.Hour)}, true},
		{"expiring within window", &auth.DaemonCredentials{ExpiresAt: at(6 * time.Hour)}, true},
		{"exactly at window edge", &auth.DaemonCredentials{ExpiresAt: at(daemonPATRenewBefore)}, true},
		{"comfortably in the future", &auth.DaemonCredentials{ExpiresAt: at(90 * 24 * time.Hour)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := daemonCredsExpiringSoon(tc.creds, now); got != tc.want {
				t.Errorf("daemonCredsExpiringSoon = %v, want %v", got, tc.want)
			}
		})
	}
}
