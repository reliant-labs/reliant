package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
)

type bundleSend struct {
	command string
	payload map[string]any
}

// bundleRouter records every command sent and answers it with reply.
type bundleRouter struct {
	sends []bundleSend
	reply func(command string) ([]byte, error)
}

func (r *bundleRouter) SendDaemonCommandToDaemon(_ context.Context, _, _, command string, payload []byte, _ int32) ([]byte, error) {
	var p map[string]any
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	r.sends = append(r.sends, bundleSend{command: command, payload: p})
	return r.reply(command)
}

func (*bundleRouter) PlacementDaemonID(_ context.Context, _, daemonID string) (string, error) {
	return daemonID, nil
}

const outdatedInstallSkillBundle = `this machine is running an older version of Reliant (v1.0.0) that doesn't support "mcp.install_skill_bundle" — update it to continue`

func installBundle(t *testing.T, router *bundleRouter, scope reliantv1.ConfigScope, targetScope string) error {
	t.Helper()
	svc := NewTestMCP(t, WithMCPDeps(Deps{Router: router}))
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	rec := &config.RecommendedMCP{Name: "x", Bundles: &config.RecommendedBundle{Skills: []config.RecommendedSkillBundle{{
		Name:        "x",
		TargetScope: targetScope,
		Files:       []config.RecommendedSkillFile{{Path: "x/SKILL.md", Content: "# x"}},
	}}}}
	_, err := svc.installBundledSkillsForRecommended(ctx, projectcheckout.Target{DaemonID: "daemon-1", Path: "/work"}, scope, rec)
	return err
}

// The global skills directory is under the user config dir of the machine
// holding the checkout, which only that daemon knows. The server names the
// scope and never a directory of its own.
func TestInstallBundledSkills_GlobalScopeIsResolvedByTheDaemon(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name        string
		scope       reliantv1.ConfigScope
		targetScope string
		want        string
	}{
		{name: "inherit global", scope: reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL, want: "global"},
		{name: "explicit user", scope: reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT, targetScope: "user", want: "global"},
		{name: "inherit project", scope: reliantv1.ConfigScope_CONFIG_SCOPE_UNSPECIFIED, want: "project"},
		{name: "explicit project_local", scope: reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL, targetScope: "project_local", want: "local"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router := &bundleRouter{reply: func(string) ([]byte, error) { return []byte(`{"installed":1}`), nil }}
			require.NoError(t, installBundle(t, router, tt.scope, tt.targetScope))
			require.Len(t, router.sends, 1)
			got := router.sends[0]
			assert.Equal(t, "mcp.install_skill_bundle", got.command)
			assert.Equal(t, tt.want, got.payload["scope"])
			assert.Equal(t, "/work", got.payload["project_path"])
			assert.NotContains(t, got.payload, "target_dir", "the server must not name a directory on the daemon's machine")
		})
	}
}

// A daemon older than mcp.install_skill_bundle still gets a project-relative
// bundle, as an absolute directory under the checkout. A global one is
// refused rather than written under this process's home.
func TestInstallBundledSkills_OlderDaemon(t *testing.T) {
	t.Parallel()
	reply := func(command string) ([]byte, error) {
		if command == "mcp.install_skill_bundle" {
			return nil, errors.New(outdatedInstallSkillBundle)
		}
		return []byte(`{"installed":1}`), nil
	}

	t.Run("project scope falls back to target_dir", func(t *testing.T) {
		t.Parallel()
		router := &bundleRouter{reply: reply}
		require.NoError(t, installBundle(t, router, reliantv1.ConfigScope_CONFIG_SCOPE_PROJECT_LOCAL, ""))
		require.Len(t, router.sends, 2)
		assert.Equal(t, "mcp.install_bundle", router.sends[1].command)
		assert.Equal(t, "/work/"+config.ReliantLocalDir+"/skills", router.sends[1].payload["target_dir"])
	})

	t.Run("global scope is refused", func(t *testing.T) {
		t.Parallel()
		router := &bundleRouter{reply: reply}
		err := installBundle(t, router, reliantv1.ConfigScope_CONFIG_SCOPE_GLOBAL, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "older version of Reliant")
		require.Len(t, router.sends, 1, "no legacy install with a server-side home directory")
	})
}
