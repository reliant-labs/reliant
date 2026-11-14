// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"slices"
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// stampManager is a manager whose project has servers "allowed" and
// "forbidden", and whose clientFactory records each server it starts.
func stampManager(t *testing.T) (*Manager, *[]string) {
	t.Helper()
	config.UseUserConfigDirForTest(t, t.TempDir())
	m := NewManager(RoleDaemon)
	t.Cleanup(func() { _ = m.Close() })
	started := &[]string{}
	m.clientFactory = func(name string, _ config.MCPServer) (Client, error) {
		*started = append(*started, name)
		return &fakeManagerClient{}, nil
	}
	m.SetProjectConfigResolver(func(context.Context, string) (*config.Config, error) {
		return &config.Config{MCPServers: withoutBuiltins(map[string]config.MCPServer{
			"allowed":   {Type: config.MCPStdio, Command: "allowed-bin", Enabled: true},
			"forbidden": {Type: config.MCPStdio, Command: "forbidden-bin", Enabled: true},
		})}, nil
	})
	return m, started
}

// A run whose org stamps an MCP allowlist starts only the servers it allows:
// the other is neither started nor reported failed.
func TestEnsureProjectServersLoaded_OrgStampRefusesSpawn(t *testing.T) {
	m, started := stampManager(t)
	stamp := &daemonpolicy.OrgStamp{OrgID: "org-a", MCP: &daemonpolicy.OrgStampMCP{AllowedServers: []string{"allowed"}}}
	result := m.EnsureProjectServersLoaded(daemonpolicy.WithOrgStamp(context.Background(), stamp), t.TempDir())

	if slices.Contains(*started, "forbidden") {
		t.Fatalf("a server the org does not allow was started: %v", *started)
	}
	if !slices.Contains(*started, "allowed") || !slices.Contains(result.LoadedServers, "allowed") {
		t.Fatalf("the allowed server must still start: started %v, loaded %v", *started, result.LoadedServers)
	}
	if slices.Contains(result.FailedServers, "forbidden") || slices.Contains(result.LoadedServers, "forbidden") {
		t.Fatalf("a refused server is neither loaded nor failed: %+v", result)
	}
}

// No stamp (or one with no MCP rule): every enabled server starts, as today.
func TestEnsureProjectServersLoaded_NoStampStartsEverything(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no stamp":         context.Background(),
		"credentials only": daemonpolicy.WithOrgStamp(context.Background(), &daemonpolicy.OrgStamp{OrgID: "org-a", Credentials: daemonpolicy.CredentialsNone}),
	} {
		t.Run(name, func(t *testing.T) {
			m, started := stampManager(t)
			m.EnsureProjectServersLoaded(ctx, t.TempDir())
			if !slices.Contains(*started, "allowed") || !slices.Contains(*started, "forbidden") {
				t.Fatalf("started %v, want both servers", *started)
			}
		})
	}
}
