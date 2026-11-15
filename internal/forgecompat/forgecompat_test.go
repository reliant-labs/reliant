package forgecompat

import "testing"

// TestDecide is the decision table. It is a pure function, so
// every row is deterministic — no module graph, no network, no toolchain.
func TestDecide(t *testing.T) {
	// Decide honours the operator override; a developer shell that sets it
	// (Reliant does) must not change these verdicts.
	t.Setenv(SkewOverrideEnv, "")
	const binary = "v0.1.16"
	cases := []struct {
		name    string
		binary  string
		project string
		raw     string // buildinfo.Version(): what the binary calls itself
		local   bool
		want    Verdict
	}{
		{"pin equals binary", binary, "v0.1.16", "", false, OK},
		// A pin NEWER than the binary used to be OK on the grounds that
		// older templates calling a newer library is the ordinary upgrade
		// order. That holds for the Go half and not for KCL: forge's KCL
		// schema module is embedded in the binary, so an older binary
		// renders the project's KCL against an older schema and blames the
		// project's own line number. See skew.go and BinaryBehindPin.
		{"pin newer than binary", binary, "v0.1.17", "", false, BehindPin},
		{"pin older than binary", binary, "v0.1.15", "", false, StalePin},
		{"pin older by patch", binary, "v0.1.16-rc.1", "", false, StalePin},

		// A pseudo-version from `go install ...@main` is a real, orderable
		// version: it sorts after the tag it builds on and before the next
		// one, which is exactly what commit-pinning mode needs.
		{"binary is a pseudo-version, pin is the tag it follows",
			"v0.1.16-0.20260916085636-c01e07ec6ef2", "v0.1.15", "", false, StalePin},
		{"binary is a pseudo-version, pin is the next tag",
			"v0.1.16-0.20260916085636-c01e07ec6ef2", "v0.1.16", "", false, OK},

		// The regression this design exists for: an unreleasable binary
		// (dirty tree, plain `go build`) against a published pin.
		{"unreleasable binary, published pin", "", "v0.1.15", "", false, UnreleasableNoBridge},
		{"unreleasable binary, newer published pin", "", "v9.9.9", "", false, UnreleasableNoBridge},

		// A local resolution is the supported pairing for an unreleasable
		// binary, and is fine for a released one too.
		{"unreleasable binary, bridged", "", "", "", true, OK},
		{"released binary, bridged", binary, "", "", true, OK},

		// A BUILD THAT CANNOT VOUCH FOR ITSELF, against a project that
		// already resolves to exactly it. InstallableVersion() is "" for any
		// working-tree build, but the project's own `go list -m` answering
		// with this version proves the proxy served it — so refusing would be
		// a false positive whose message names one version as both "what this
		// forge is" and "the published version" it conflicts with.
		{"unreleasable binary, project resolves to this very build",
			"", "v0.1.17-0.20260918232310-3cfebb459c15", "v0.1.17-0.20260918232310-3cfebb459c15", false, OK},
		// Same shape, DIFFERENT commit: no proof, so the refusal stands.
		{"unreleasable binary, project resolves to a different build",
			"", "v0.1.17-0.20260918232310-3cfebb459c15", "v0.1.17-0.20260918144654-6b3a0a0050cd", false, UnreleasableNoBridge},

		// Unknown beats guessing.
		{"unknown project version", binary, "", "", false, OK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Decide(c.binary, c.raw, c.project, c.local); got != c.want {
				t.Errorf("Decide(%q, %q, %q, %v) = %v, want %v",
					c.binary, c.raw, c.project, c.local, got, c.want)
			}
		})
	}
}
