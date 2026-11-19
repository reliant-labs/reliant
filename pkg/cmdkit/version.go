package cmdkit

import (
	"fmt"
	"io"
	"runtime/debug"
)

// BuildInfo is what a binary knows about its own provenance.
//
// The three string fields are populated at LINK time, not here: the build
// stamps them with `-ldflags "-X <pkg>.version=..."`, which can only target a
// variable in the project's own package. That is why this is a plain struct a
// command fills in rather than something cmdkit reads for itself — a
// `-X github.com/reliant-labs/forge/pkg/cmdkit.version=...` would stamp the
// library for every project at once, so the variables must stay in the
// project's tree and the FORMATTING is the only part that can be shared.
type BuildInfo struct {
	// Name is the binary's own name, printed first.
	Name string
	// Version is the release version ("dev" when unstamped).
	Version string
	// Commit is the source revision ("none" when unstamped).
	Commit string
	// Date is the build timestamp ("unknown" when unstamped).
	Date string
}

// PrintVersion writes a human-readable version block for info to w.
//
// The Go toolchain version is read from the embedded build info rather than
// stamped, because the compiler already records it and a hand-passed value
// could disagree with the compiler that actually produced the binary. It is
// omitted entirely when unavailable (a binary built without module
// information) rather than printed as an empty or "unknown" line — a version
// command's whole job is to be trustworthy, so a field it cannot vouch for
// should be absent, not guessed.
func PrintVersion(w io.Writer, info BuildInfo) {
	fmt.Fprintf(w, "%s %s\n", info.Name, info.Version)
	fmt.Fprintf(w, "  commit: %s\n", info.Commit)
	fmt.Fprintf(w, "  built:  %s\n", info.Date)
	if bi, ok := debug.ReadBuildInfo(); ok && bi.GoVersion != "" {
		fmt.Fprintf(w, "  go:     %s\n", bi.GoVersion)
	}
}
