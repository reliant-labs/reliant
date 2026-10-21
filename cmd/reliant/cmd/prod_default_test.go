package cmd

import (
	"testing"

	forgeconfig "github.com/reliant-labs/forge/pkg/config"
	"google.golang.org/protobuf/proto"

	configv1 "github.com/reliant-labs/reliant/gen/config/v1"
	"github.com/reliant-labs/reliant/pkg/config"
)

// A plain `go install github.com/reliant-labs/reliant/cmd/reliant@latest` build
// — no ldflags, no environment — MUST talk to production.
//
// CLIConfig's proto defaults carry the hosted endpoints, so this holds as long
// as nothing in the resolution path prefers an empty or loopback value.
func TestGoInstallBuildDefaultsToProduction(t *testing.T) {
	isolateCLI(t)

	conn, err := resolveWithArgs(t)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := conn.ServerURL, "https://api.reliantapi.com"; got != want || conn.ServerSource != sourceDefault {
		t.Errorf("server = %q (source %d), want the production API %q as the default", got, conn.ServerSource, want)
	}
	if got, want := conn.GatewayURL, "https://gateway.reliantapi.com"; got != want || conn.GatewaySource != sourceDefault {
		t.Errorf("gateway = %q (source %d), want the production gateway %q as the default", got, conn.GatewaySource, want)
	}
}

// Dev overrides still work, and they are the ONLY way to leave production.
// This is the property that makes "default to prod" safe to rely on: pointing
// at a dev stack is an explicit, visible act.
func TestExplicitEnvOverridesProduction(t *testing.T) {
	isolateCLI(t)
	t.Setenv(envServerURL, "http://localhost:8151")
	t.Setenv(envGatewayURL, "http://localhost:29190")

	conn, err := resolveWithArgs(t)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := conn.ServerURL, "http://localhost:8151"; got != want || conn.ServerSource != sourceEnv {
		t.Errorf("server = %q (source %d), want the explicit override %q from the environment", got, conn.ServerSource, want)
	}
	if got, want := conn.GatewayURL, "http://localhost:29190"; got != want || conn.GatewaySource != sourceEnv {
		t.Errorf("gateway = %q (source %d), want the explicit override %q from the environment", got, conn.GatewaySource, want)
	}
}

// A release stamps its endpoints into pkg/config.BuildDefaults with -ldflags
// -X (.github/workflows/release.yml). The stamped values are the build's
// default: they replace the proto defaults, still lose to the environment,
// and are reported as the default rather than as an override.
func TestStampedBuildDefaultsAreTheDefault(t *testing.T) {
	isolateCLI(t)
	const stamp = "RELIANT_SERVER_URL=https://api.stamped.example&RELIANT_GATEWAY_URL=https://gateway.stamped.example"
	stampBuildDefaults(t, stamp)

	conn, err := resolveWithArgs(t)
	if err != nil {
		t.Fatal(err)
	}
	if conn.ServerURL != "https://api.stamped.example" || conn.ServerSource != sourceDefault {
		t.Errorf("server = %q (source %d), want the stamped default", conn.ServerURL, conn.ServerSource)
	}
	if conn.GatewayURL != "https://gateway.stamped.example" || conn.GatewaySource != sourceDefault {
		t.Errorf("gateway = %q (source %d), want the stamped default", conn.GatewayURL, conn.GatewaySource)
	}

	t.Setenv(envServerURL, "http://localhost:8151")
	conn, err = resolveWithArgs(t)
	if err != nil {
		t.Fatal(err)
	}
	if conn.ServerURL != "http://localhost:8151" || conn.ServerSource != sourceEnv {
		t.Errorf("server = %q (source %d), want RELIANT_SERVER_URL over the stamp", conn.ServerURL, conn.ServerSource)
	}
	if conn.GatewayURL != "http://localhost:8151" || conn.GatewaySource != sourceDerived {
		t.Errorf("gateway = %q (source %d), want it derived from the overriding server", conn.GatewayURL, conn.GatewaySource)
	}
}

// stampBuildDefaults installs stamp as this binary's build-time defaults for
// one test, the way -ldflags -X and pkg/config's init do.
func stampBuildDefaults(t *testing.T, stamp string) {
	t.Helper()
	msgs := []proto.Message{&configv1.CLIConfig{}, &configv1.ReliantConfig{}}
	previous := config.BuildDefaults
	config.BuildDefaults = stamp
	forgeconfig.SetBuildDefaults(stamp, msgs...)
	if err := forgeconfig.BuildDefaultsStampError(); err != nil {
		t.Fatalf("stamp refused: %v", err)
	}
	t.Cleanup(func() {
		config.BuildDefaults = previous
		forgeconfig.SetBuildDefaults(previous, msgs...)
	})
}
