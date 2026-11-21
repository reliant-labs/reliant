package forgetest

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The functions that read the authenticated caller off the request context,
// each marked //forge:resolves-caller so the pinned forge's unscoped_auth
// audit counts a handler that calls one as resolving its caller. reliant
// reads the caller through these, not forge's middleware.GetUser, so without
// the markers the audit reports every authenticated rpc as unscoped.
var wantCallerResolvers = []string{
	"github.com/reliant-labs/reliant/internal/auth.GetCredentialOrgFromContext",
	"github.com/reliant-labs/reliant/internal/auth.GetDaemonIDFromContext",
	"github.com/reliant-labs/reliant/internal/auth.GetDaemonOrgIDFromContext",
	"github.com/reliant-labs/reliant/internal/auth.GetUserEmailFromContext",
	"github.com/reliant-labs/reliant/internal/auth.GetUserIDFromContext",
	"github.com/reliant-labs/reliant/internal/auth.MachineTokenFromContext",
	"github.com/reliant-labs/reliant/internal/auth.MustGetUserID",
	"github.com/reliant-labs/reliant/internal/connectionrpc.Caller",
	"github.com/reliant-labs/reliant/internal/daemoncmd.UserID",
}

// The authenticated rpcs the audit may still report as never resolving the
// caller: each is a stub that answers CodeUnimplemented and touches no data.
// They are deliberately not marked forge:auth-unscoped-ok, because a marker
// on a stub would stay when someone implements the rpc and hide a real gap.
// Implementing one takes it off this list (the audit then sees it resolve
// its caller, or reports it, and this test fails until it does).
var allowedUnscopedRPCs = map[string]bool{
	"PlanService/ListPlansByProject":              true,
	"SettingsService/DeleteGlobalSkill":           true,
	"SettingsService/GetInstalledSkillDefinition": true,
	"SettingsService/InstallSkill":                true,
	"SettingsService/ListInstalledSkills":         true,
	"SettingsService/ListRecommendedSkills":       true,
	"SettingsService/SetSkillEnabled":             true,
	"SettingsService/TrackOnboardingEvent":        true,
	"TaskService/AddTaskDependency":               true,
	"TaskService/ListReadyTasks":                  true,
	"TaskService/RemoveTaskDependency":            true,
	"ToolsDaemonService/ConnectGateway":           true,
	"ToolsDaemonService/ReportToolResult":         true,
}

// The pinned forge's audit, run over this repository, recognizes exactly the
// declared caller resolvers, clears every authenticated rpc that calls one,
// and still reports the handlers that resolve no caller. Removing a marker
// drops its resolver from the set and unscopes every handler that reads the
// caller only through it, so this fails.
func TestUnscopedAuthAuditRecognisesTheCallerResolvers(t *testing.T) {
	forge := Pinned(t) // forge's audit is a subprocess over the whole module: slow.
	t.Setenv("FORGE_LEDGER", "machine")
	t.Setenv("FORGE_LEDGER_HOME", t.TempDir())
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	cmd := forge.Command(t.Context(), "project", "audit", "--json")
	cmd.Dir = root
	// The audit exits non-zero only for a category at ERROR; unscoped_auth
	// warns here (no forge:owner table), and other categories are not this
	// test's business, so read the document either way.
	out, _ := cmd.Output()
	var audit struct {
		Categories struct {
			UnscopedAuth struct {
				Details struct {
					CallerResolverMarker string   `json:"caller_resolver_marker"`
					CallerResolvers      []string `json:"caller_resolvers"`
					AuthenticatedRPCs    int      `json:"authenticated_rpcs"`
					ScopedRPCs           int      `json:"scoped_rpcs"`
					UnscopedRPCs         []struct {
						Service string `json:"service"`
						Method  string `json:"method"`
					} `json:"unscoped_rpcs"`
				} `json:"details"`
			} `json:"unscoped_auth"`
		} `json:"categories"`
	}
	require.NoError(t, json.Unmarshal(out, &audit), "forge project audit --json: %s", out)
	got := audit.Categories.UnscopedAuth.Details

	require.Equal(t, "forge:resolves-caller", got.CallerResolverMarker)
	assert.ElementsMatch(t, wantCallerResolvers, got.CallerResolvers,
		"the audit's caller resolvers are the //forge:resolves-caller declarations")

	require.Positive(t, got.AuthenticatedRPCs)
	var unscoped []string
	for _, rpc := range got.UnscopedRPCs {
		name := fmt.Sprintf("%s/%s", rpc.Service, rpc.Method)
		unscoped = append(unscoped, name)
		assert.True(t, allowedUnscopedRPCs[name],
			"%s resolves no caller: scope it with one of the caller resolvers, or say why it is global with // forge:auth-unscoped-ok: <reason>", name)
	}
	// A handler that never resolves its caller is still reported: the
	// markers clear callers, they do not switch the audit off. (When the
	// last stub is implemented, this witness goes with it.)
	assert.NotEmpty(t, unscoped, "the audit reports no unscoped rpc at all; the stubs above should be")
	assert.LessOrEqual(t, len(unscoped), len(allowedUnscopedRPCs))
	assert.GreaterOrEqual(t, got.ScopedRPCs, got.AuthenticatedRPCs-len(allowedUnscopedRPCs)-4, // the four acknowledged CatalogService rpcs
		"every authenticated rpc but the stubs and the acknowledged global ones resolves its caller")
}
