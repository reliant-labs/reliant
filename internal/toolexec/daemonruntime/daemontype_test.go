// Copyright (c) 2025 Reliant Labs

package daemonruntime

import "testing"

// The regression this guards: managed workspaces used to be identified by
// their transport, so when they moved to dialing OUT they started registering
// as "local" — self_hosted — and the control plane then refused to resume them
// ("cannot resume external daemon"). A suspended workspace became permanently
// unwakeable, and the connector consent screen could not tell a disposable
// sandbox from someone's laptop.
func TestResolveDaemonTypeFromEnvironment(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		fallback string
		want     string
		why      string
	}{
		{
			name:     "managed pod dialing out",
			env:      "managed",
			fallback: "local",
			want:     "managed",
			why:      "the platform's statement must beat the transport's guess",
		},
		{
			name:     "cloud spelling is accepted",
			env:      "cloud",
			fallback: "local",
			want:     "managed",
			why:      "the server normalizes both spellings; so must this",
		},
		{
			name:     "explicit self_hosted",
			env:      "self_hosted",
			fallback: "cloud",
			want:     "self_hosted",
			why:      "a daemon told it is personal must not claim to be managed",
		},
		{
			name:     "unset falls back to the transport",
			env:      "",
			fallback: "local",
			want:     "local",
			why:      "a user's own daemon sets nothing and must stay self-hosted",
		},
		{
			name:     "unset in server mode stays cloud",
			env:      "",
			fallback: "cloud",
			want:     "cloud",
			why:      "dial-in is still only used by managed pods",
		},
		{
			name:     "unrecognized value does not invent a third vocabulary",
			env:      "kubernetes",
			fallback: "local",
			want:     "local",
			why:      "the server would discard an unknown value, losing the type entirely",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(DaemonTypeEnvVar, tc.env)
			if got := resolveDaemonType(tc.fallback); got != tc.want {
				t.Fatalf("resolveDaemonType(%q) with %s=%q = %q, want %q — %s",
					tc.fallback, DaemonTypeEnvVar, tc.env, got, tc.want, tc.why)
			}
		})
	}
}
