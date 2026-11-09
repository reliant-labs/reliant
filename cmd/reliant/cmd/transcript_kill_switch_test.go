// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"errors"
	"os"
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
	// FORGE_BIN is the pinned forge the release gate builds; unset, forge on PATH.
	project := kcleval.Options{ProjectDir: "../../..", Binary: os.Getenv("FORGE_BIN")}
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
			err := kcleval.Select(context.Background(), fixture, tc.selector, &env, project)
			if errors.Is(err, kcleval.ErrForgeNotFound) {
				// The host has no forge (the Actions release gate puts none on
				// PATH): a missing resource skips, it does not fail.
				t.Skipf("no forge binary to evaluate %s: %v", fixture, err)
			}
			require.NoError(t, err)
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
