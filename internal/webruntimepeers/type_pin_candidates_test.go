package webruntimepeers_test

import (
	"testing"

	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

// TestTypePinPath_HoistedLayout is the reproduction.
//
// Forge's dev bridge makes the project an npm WORKSPACE ROOT
// (internal/generator/frontend_webruntime_devlink.go), and npm hoists a
// workspace member's dependencies to the ROOT node_modules. In such a project
// frontends/<name>/node_modules does not exist at all, so a pin naming
// ./node_modules/<pkg> points at nothing. tsc does not report that as an
// error: the pin silently no-ops, resolution falls back to the ordinary upward
// walk, and it finds the LINKED runtime's own private copies first —
//
//	src/lib/mock-transport_gen.ts(54,3): error TS2322: Type
//	'…/forge/web-runtime/node_modules/@connectrpc/connect/…'.Transport is not
//	assignable to type '…/<project>/node_modules/@connectrpc/connect/…'.Transport
//
// Measured in a scaffolded bridged project: frontend-local pin → 2 TS2322
// errors; hoisted pin → 0, with no tsc setting relaxed.
func TestTypePinPath_HoistedLayout(t *testing.T) {
	t.Parallel()

	got := webruntimepeers.TypePinPath("@connectrpc/connect", true, "")
	if want := "../../node_modules/@connectrpc/connect"; got != want {
		t.Errorf("TypePinPath(hoisted) = %q, want %q", got, want)
	}
}

// The ordinary standalone frontend, which is what a non-bridged project and
// every CI run has. Its own node_modules is the right answer and must stay so.
func TestTypePinPath_LocalLayout(t *testing.T) {
	t.Parallel()

	got := webruntimepeers.TypePinPath("@connectrpc/connect", false, "")
	if want := "./node_modules/@connectrpc/connect"; got != want {
		t.Errorf("TypePinPath(local) = %q, want %q", got, want)
	}
}

// The @types/ redirect must survive the layout choice in BOTH directions:
// react is typed by a separate package, and pointing either layout at the
// implementation directory gives tsc a dir with no .d.ts, failing every .tsx
// with TS7016.
func TestTypePinPath_HonoursTheTypesRedirect(t *testing.T) {
	t.Parallel()

	if got, want := webruntimepeers.TypePinPath("react", false, ""), "./node_modules/@types/react"; got != want {
		t.Errorf("TypePinPath(react, local) = %q, want %q", got, want)
	}
	if got, want := webruntimepeers.TypePinPath("react", true, ""), "../../node_modules/@types/react"; got != want {
		t.Errorf("TypePinPath(react, hoisted) = %q, want %q", got, want)
	}
}

// Every pin must resolve to a non-empty path under both layouts — a name that
// fell out of the target map would render `["./node_modules/"]`, which
// resolves to nothing and silently disables the pin.
func TestTypePinPath_EveryPinHasAPathInBothLayouts(t *testing.T) {
	t.Parallel()

	for _, pkg := range webruntimepeers.TypePins() {
		for _, hoisted := range []bool{false, true} {
			got := webruntimepeers.TypePinPath(pkg, hoisted, "")
			if got == "./node_modules/" || got == "../../node_modules/" || got == "" {
				t.Errorf("TypePinPath(%q, hoisted=%v) = %q — resolves to nothing", pkg, hoisted, got)
			}
		}
	}
}
