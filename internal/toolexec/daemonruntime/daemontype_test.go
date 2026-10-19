// Copyright (c) 2025 Reliant Labs

package daemonruntime

import "testing"

// The regression this guards: managed workspaces used to be identified by
// their transport, so when they moved to dialing OUT they started registering
// as "local" — self_hosted — and the control plane then refused to resume them
// ("cannot resume external daemon"). A suspended workspace became permanently
// unwakeable, and the connector consent screen could not tell a disposable
// sandbox from someone's laptop.
func TestResolveDaemonTypeFromPlatform(t *testing.T) {
	cases := []struct {
		name     string
		declared string
		fallback string
		want     string
		why      string
	}{
		{
			name:     "managed pod dialing out",
			declared: "managed",
			fallback: "local",
			want:     "managed",
			why:      "the platform's statement must beat the transport's guess",
		},
		{
			name:     "explicit self_hosted",
			declared: "self_hosted",
			fallback: "cloud",
			want:     "self_hosted",
			why:      "a daemon told it is personal must not claim to be managed",
		},
		{
			name:     "unset falls back to the transport",
			declared: "",
			fallback: "local",
			want:     "local",
			why:      "a user's own daemon sets nothing and must stay self-hosted",
		},
		{
			name:     "unset in server mode stays cloud",
			declared: "",
			fallback: "cloud",
			want:     "cloud",
			why:      "dial-in is still only used by managed pods",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveDaemonType(tc.declared, tc.fallback); got != tc.want {
				t.Fatalf("resolveDaemonType(%q, %q) = %q, want %q — %s",
					tc.declared, tc.fallback, got, tc.want, tc.why)
			}
		})
	}
}

func TestHostManaged(t *testing.T) {
	for declared, want := range map[string]bool{"managed": true, "self_hosted": false, "": false} {
		if got := (Host{DaemonType: declared}).Managed(); got != want {
			t.Errorf("Host{DaemonType: %q}.Managed() = %v, want %v", declared, got, want)
		}
	}
}
