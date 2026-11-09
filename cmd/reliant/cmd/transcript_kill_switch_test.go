// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/messageproto"
)

// TestTranscriptKillSwitchReachesTheAPIServer pins the rollback for lazy tool
// bodies end to end: transcript.tool_body_stubs = "off" in an env's KCL must
// come out of config_gen.workloadEnvMap for the api root as
// RELIANT_TRANSCRIPT_TOOL_BODY_STUBS=off, and `server api` loading that env
// must serve a STUB request FULL. The handler tests construct Config
// directly, so without this a dropped env-map entry, or the variable leaving
// the api root's reach, would leave the switch documented and inert.
func TestTranscriptKillSwitchReachesTheAPIServer(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))

	const fixture = "deploy/kcl/testdata/transcript_kill_switch.k"
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	for _, tc := range []struct {
		selector string
		wantEnv  string
		wantMode reliantv1.ToolBodyMode
	}{
		{"stubs_off", messageproto.ToolBodyStubsOff, reliantv1.ToolBodyMode_TOOL_BODY_MODE_FULL},
		{"stubs_default", "on", reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var env map[string]any
			require.NoError(t, kcleval.Select(context.Background(), fixture, tc.selector, &env, project))
			got, ok := env["RELIANT_TRANSCRIPT_TOOL_BODY_STUBS"]
			require.True(t, ok, "the api server's projected env has no RELIANT_TRANSCRIPT_TOOL_BODY_STUBS: %v", env)
			require.Equal(t, tc.wantEnv, got)

			// Boot `server api`'s config from the variable as projected.
			isolateServerAPIConfigEnv(t)
			t.Setenv("RELIANT_TRANSCRIPT_TOOL_BODY_STUBS", got.(string))
			cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
			require.NoError(t, err)
			cfg, err := loadRootConfig(cmd)
			require.NoError(t, err)
			assert.Equal(t, tc.wantEnv, cfg.GetTranscript().GetToolBodyStubs())
			assert.Equal(t, tc.wantMode, messageproto.ResolveToolBodyMode(
				reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB, cfg.GetTranscript().GetToolBodyStubs()))
		})
	}
}

// pinnedForge returns the forge this module pins: $FORGE_BIN when set (the
// Taskfile's own override), else github.com/reliant-labs/forge/cmd/forge built
// at go.mod's require into the test's temp dir. Never the forge on PATH: CI
// runners have none, and a developer's is routinely another version.
func pinnedForge(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("FORGE_BIN"); bin != "" {
		return bin
	}
	bin := filepath.Join(t.TempDir(), "forge")
	out, err := exec.Command("go", "build", "-trimpath", "-o", bin, "github.com/reliant-labs/forge/cmd/forge").CombinedOutput()
	require.NoError(t, err, "building the pinned forge: %s", out)
	return bin
}
