package webruntimepeers_test

import (
	"testing"

	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

// A tsconfig `paths` entry OVERRIDES module resolution rather than hinting at
// it: once "react" maps to ./node_modules/react, tsc looks there and nowhere
// else, so it finds index.js, finds no bundled typings, and never consults
// @types/react the way it would have unaided. Every .tsx in the project then
// fails with TS7016 "Could not find a declaration file for module 'react'".
//
// That shipped: adding react to the derived pin list (correct — it IS a
// runtime peer, and two copies really do split its types) silently pointed
// the pin at a directory with no .d.ts.
func TestTypePinTarget_ReactResolvesToItsTypesPackage(t *testing.T) {
	t.Parallel()
	if got := webruntimepeers.TypePinTarget("react"); got != "@types/react" {
		t.Fatalf("TypePinTarget(react) = %q, want @types/react — pinning the implementation dir fails every .tsx with TS7016", got)
	}
}

// The redirect must stay narrow. Every other pinned package bundles its own
// typings, so sending one to a non-existent @types/ directory would break the
// resolution this list exists to fix.
func TestTypePinTarget_SelfTypedPackagesAreUnchanged(t *testing.T) {
	t.Parallel()
	for _, pkg := range []string{
		"@connectrpc/connect",
		"@bufbuild/protobuf",
		"@tanstack/react-query",
		"@tanstack/query-core",
		"@opentelemetry/api",
	} {
		if got := webruntimepeers.TypePinTarget(pkg); got != pkg {
			t.Errorf("TypePinTarget(%q) = %q, want it unchanged — this package ships its own typings", pkg, got)
		}
	}
}

// Every pin must have a target: a name that fell out of the map entirely would
// render `"paths": { "x": ["./node_modules/"] }`, which resolves to nothing.
func TestTypePinTarget_EveryPinHasATarget(t *testing.T) {
	t.Parallel()
	for _, pkg := range webruntimepeers.TypePins() {
		if webruntimepeers.TypePinTarget(pkg) == "" {
			t.Errorf("TypePinTarget(%q) is empty", pkg)
		}
	}
}
