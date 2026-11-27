// Package forgepin names the forge a project pins, for a host that embeds
// forge's CLI and runs the project's forge itself (reliant reading the pinned
// forge's skills and memory).
//
// It is a leaf on purpose: it imports only the standard library, so a host
// package that passes a pinned forge around, or takes Resolve as a dependency,
// does not link the forge CLI (KCL, docker, the cloud SDKs, k8s) into every
// test binary that reaches it. The decision itself is the forge CLI's, and is
// made only when that CLI is linked into the binary:
//
//	import _ "github.com/reliant-labs/forge/cli" // in the host's main, which embeds forge anyway
//
// Without it there is no embedded forge to compare a pin against, and Resolve
// returns ErrNotEmbedded.
package forgepin

import (
	"context"
	"errors"
	"io"

	"github.com/reliant-labs/forge/internal/forgepinhook"
)

// SourceSelf is Forge.Source when this forge is the project's.
const SourceSelf = "self"

// Forge is the forge a project pins, resolved and ready to run. Its fields
// match internal/forgepinhook.Result's, in order, so the CLI's answer converts
// to it whole.
type Forge struct {
	// Path is the executable.
	Path string
	// Key identifies exactly which forge it is, for a cache of what it
	// prints: the pinned version ("pin:", a module version that never
	// changes), a linked checkout and its contents, or for a +dirty pin the
	// binary file that reports it and that file's size and mtime.
	Key string
	// Want says which forge the project wants, for a message.
	Want string
	// Source says where Path came from: SourceSelf (this forge is the
	// project's), or the project's tools directory, the build cache, or a
	// build made just now.
	Source string
	// Args are the arguments that come before forge's own when running Path:
	// ["forge"] when Path is this forge's executable and forge is embedded
	// in it (`reliant forge`), else none. Set by the forge CLI's
	// ResolvePinnedForgePath.
	Args []string
	// Version is the version Path reports. Set by ResolvePinnedForgePath.
	Version string
	// Pin is forge.yaml's forge_version, and Checkout the local forge
	// checkout the project is linked to, when it is. Set by Resolve and
	// ResolvePinnedForgePath.
	Pin, Checkout string
}

// ErrNotEmbedded is Resolve's error when the binary does not link forge's CLI
// (github.com/reliant-labs/forge/cli), so no forge is embedded to decide.
var ErrNotEmbedded = errors.New("forgepin: the forge CLI is not linked into this binary (import github.com/reliant-labs/forge/cli)")

// Resolve makes the embedded forge's hand-off decision for the forge command
// command ("skill load", "skill list") in projectRoot, and resolves the
// project's forge without running it. ok is false when the embedded forge is
// the one to run command there; err, with the reason, when the project's
// forge is wanted and cannot be obtained — a host must then not substitute
// the embedded forge's answer. log receives the one-time build notice.
//
// The Forge is the forge CLI's answer passed through whole, every field the
// CLI set: today Path, Key, Want, Source, Pin and Checkout (Resolve does not
// run the forge, so Version is not probed).
func Resolve(ctx context.Context, projectRoot, command string, log io.Writer) (Forge, bool, error) {
	resolve := forgepinhook.Resolve
	if resolve == nil {
		return Forge{}, false, ErrNotEmbedded
	}
	r, ok, err := resolve(ctx, projectRoot, command, log)
	if err != nil || !ok {
		return Forge{}, ok, err
	}
	return Forge(r), true, nil
}
