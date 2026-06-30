// Package builddefaults centralizes the runtime-config defaults the reliant
// binary falls back to when no environment override is present.
//
// OSS-clean contract
// ------------------
// The OSS binary must ship NO Reliant-hosted-specific config. A build with no
// env and no compiled-in default resolves to a NEUTRAL localhost default — a
// self-hoster's binary points at their own local stack, never silently at
// Reliant's hosted control plane. The commercial RC opts INTO the hosted
// endpoints by injecting the compiled-in `var` defaults below at build time
// via linker flags, e.g.
//
//	go build -ldflags "\
//	  -X github.com/reliant-labs/reliant/internal/builddefaults.ServerURL=https://reliantapi.com \
//	  -X github.com/reliant-labs/reliant/internal/builddefaults.AuthURL=https://<project>.supabase.co \
//	  -X github.com/reliant-labs/reliant/internal/builddefaults.AuthKey=<publishable-anon-key>"
//
// Precedence (see Value): explicit env var > compiled-in default (-X) > neutral
// fallback. This mirrors the web model — the OSS source is config-agnostic; the
// closed control-plane / commercial build supplies the hosted values.
package builddefaults

import "os"

// NeutralServerURL is the OSS fallback when neither RELIANT_SERVER_URL nor a
// compiled-in ServerURL is set. A localhost default keeps an un-injected OSS
// build pointed at a local self-hosted stack instead of Reliant's hosted API.
const NeutralServerURL = "http://localhost:8080"

var (
	// ServerURL, GatewayURL, AuthURL, AuthKey are the build-time-injectable
	// hosted defaults. They are empty in the OSS source and set via -X ldflags
	// only for commercial RCs (see package doc). Empty here → Value falls
	// through to the env var or the neutral fallback.
	ServerURL  string
	GatewayURL string
	AuthURL    string
	AuthKey    string
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
