// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/version"
)

// stampBuildIdentity writes the running build onto a registration, in both
// transports (the dial-out stream and the gateway-dials-in server mode), so a
// managed pod reports the same fields a desktop daemon does. An unstamped value
// is left empty rather than sent as the literal "unknown": the wire treats
// empty as "unknown build", and a server that compares builds must not have to
// special-case a sentinel string.
func stampBuildIdentity(reg *reliantv1.DaemonRegister) {
	b := version.Get()
	reg.Version = knownOrEmpty(b.Version)
	reg.Commit = knownOrEmpty(b.Commit)
	reg.CommitTimeUnix = b.CommitTimeUnix
	reg.ForgeVersion = knownOrEmpty(b.Forge)
}

func knownOrEmpty(s string) string {
	if s == "unknown" {
		return ""
	}
	return s
}
