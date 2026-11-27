// Package forgepinhook connects pkg/forgepin to the forge CLI that makes its
// decision, without pkg/forgepin importing the CLI.
//
// pkg/forgepin is the leaf a host imports for the forge a project pins. The
// decision behind it (which commands are handed to the pin, how a pin is
// compared with this forge, how the pinned forge is found or built) lives in
// internal/cli, whose import closure is the whole forge CLI: KCL, docker, the
// cloud SDKs, k8s. internal/cli sets Resolve when it is linked, so a host that
// embeds forge's CLI gets the real answer and a package that only names the
// types links none of that.
//
// This package must import only the standard library: pkg/forgepin's guard
// test walks into it.
package forgepinhook

import (
	"context"
	"io"
)

// Result is the pinned forge internal/cli resolved, every field of it.
// pkg/forgepin.Forge has the same fields in the same order, and each side
// converts the other with a plain conversion (forgepin.Forge(r)), so a field
// added to one and not the other fails to compile rather than being dropped
// on the way through. See forgepin.Forge for what each field means.
type Result struct {
	Path     string
	Key      string
	Want     string
	Source   string
	Args     []string
	Version  string
	Pin      string
	Checkout string
}

// Resolve is internal/cli's ResolvePinnedForge, set by its init. Nil when the
// forge CLI is not linked into the binary.
var Resolve func(ctx context.Context, projectRoot, command string, log io.Writer) (Result, bool, error)
