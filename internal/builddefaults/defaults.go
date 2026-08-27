// Package builddefaults centralizes the runtime-config defaults the reliant
// binary falls back to when no environment override is present.
//
// Hosted-by-default contract
// --------------------------
// The source defaults ARE Reliant's hosted endpoints, so a binary built
// straight from this repo — `go install github.com/reliant-labs/reliant/cmd/reliant@latest`,
// `go build ./cmd/reliant`, `make build` — works out of the box against the
// hosted platform with no flags and no environment.
//
// Pointing at a different backend is an explicit, one-value act:
//
//	reliant daemon start --server https://my-stack.example.com
//	RELIANT_SERVER_URL=http://localhost:8080 reliant daemon start
//
// Both outrank everything here (see Value).
//
// THIS REVERSES THE EARLIER "OSS-clean" CONTRACT, deliberately. That contract
// left these vars empty so an un-injected build fell through to
// http://localhost:8080, on the reasoning that a self-hoster's binary must
// never silently point at Reliant's control plane. The cost landed on the
// common case instead: `go install` is documented as a supported install path
// (docs/getting-started/installation.mdx), and it produced a binary that dialed
// a localhost port nothing was listening on, failing with an error that never
// mentioned the hosted platform existed. Secrecy was never the argument —
// these hostnames are already committed in this public repo in
// electron/release.config.json.
//
// Precedence (see Value): explicit env var > compiled-in -X override > the
// source default below.
//
// WHERE THESE VALUES COME FROM. They are a PROJECTION of control-plane's
// deploy/kcl/lib/env.k (reliant_endpoints), the single declaration that also
// produces the hosted SPA's build env, the packaged desktop app's renderer and
// main-process config, and the -X flags in .github/workflows/release.yml. Go
// cannot import KCL, so this is a hand-synced copy — and
// TestSourceDefaultsMatchGeneratedReleaseConfig pins it against the generated,
// drift-gated electron/release.config.json so the two cannot diverge silently.
// To change an endpoint: edit the KCL, regenerate that file, then update these.
//
// forge:exclude-contract
//
// ServerURL/GatewayURL/AuthURL/AuthKey must remain package-level string vars:
// `-ldflags -X` can only write one of those. Behind a getter or in a struct the
// linker flag silently does nothing and a release build ships whatever is
// written here — with no build error to catch it. Value() is the accessor; the
// vars are the injection site. (A non-empty initializer does NOT block -X;
// the linker overwrites it, which is how preprod builds retarget these.)
package builddefaults

import "os"

// NeutralServerURL is the loopback default a caller can pass to Value as an
// explicit self-host fallback. It is NO LONGER what an un-injected build
// resolves to — ServerURL below carries the hosted endpoint — but it remains
// the canonical "your own local stack" address for callers that want it.
const NeutralServerURL = "http://localhost:8080"

// The hosted defaults, projected from control-plane's KCL (see package doc).
// A release build overwrites them via -X to retarget an environment (preprod);
// prod injects the same values these already carry.
var (
	// ServerURL is the hosted API server: the daemon's --server target and the
	// origin daemon credentials are keyed by.
	ServerURL = "https://api.reliantapi.com"

	// GatewayURL is the daemon-gateway, a DIFFERENT process from the API
	// server: ToolsDaemonService (the daemon's bidi stream) is hosted only by
	// the gateway, so a daemon pointed at the API server gets a 404 on connect.
	//
	// Declared explicitly and never derived. deriveGatewayURL prefixes the
	// server's leading label, which for prod's `api.` host would invent
	// `gateway-api.reliantapi.com` — a host that does not exist. That shipped
	// once; see cmd/reliant/commands/connection.go.
	GatewayURL = "https://gateway.reliantapi.com"

	// AuthURL and AuthKey are the OAuth provider origin and its PUBLIC
	// publishable key (sb_publishable_*) — the browser-facing anon identity,
	// not a service-role secret, and already public in this repo. Without them
	// a `go install` build cannot complete an interactive login.
	AuthURL = "https://dash.reliantlabs.io"
	AuthKey = "sb_publishable_KKiB3B0EdEv7nguwKfEE5A_iY9rVXod"
)

func Value(envKey, compiledDefault, fallback string) string {
	if value := os.Getenv(envKey); value != "" {
		return value
	}
	if compiledDefault != "" {
		return compiledDefault
	}
	return fallback
}
