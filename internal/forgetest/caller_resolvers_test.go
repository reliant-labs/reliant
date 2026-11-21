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

// The authenticated rpcs whose handlers are stubs: each answers
// CodeUnimplemented and touches no data, so the audit lists it under
// unimplemented_rpcs instead of judging it (forge #2431). They are
// deliberately not marked forge:auth-unscoped-ok, because a marker on a stub
// would stay when someone implements the rpc and hide a real gap.
// Implementing one takes it off the audit's stub list, so this test fails
// until it is taken off this one too, and from then on the audit judges it.
var stubRPCs = []string{
	"PlanService/ListPlansByProject",
	"SettingsService/DeleteGlobalSkill",
	"SettingsService/GetInstalledSkillDefinition",
	"SettingsService/InstallSkill",
	"SettingsService/ListInstalledSkills",
	"SettingsService/ListRecommendedSkills",
	"SettingsService/SetSkillEnabled",
	"SettingsService/TrackOnboardingEvent",
	"TaskService/AddTaskDependency",
	"TaskService/ListReadyTasks",
	"TaskService/RemoveTaskDependency",
	"ToolsDaemonService/ConnectGateway",
	"ToolsDaemonService/ReportToolResult",
}

// The pinned forge's audit, run over this repository, recognizes exactly the
// declared caller resolvers and clears every implemented authenticated rpc
// that calls one. Removing a marker drops its resolver from the set and
// unscopes every handler that reads the caller only through it, so this
// fails.
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
					CallerResolverMarker string     `json:"caller_resolver_marker"`
					CallerResolvers      []string   `json:"caller_resolvers"`
					AuthenticatedRPCs    int        `json:"authenticated_rpcs"`
					ScopedRPCs           int        `json:"scoped_rpcs"`
					UnscopedRPCs         []auditRPC `json:"unscoped_rpcs"`
					UnimplementedRPCs    []auditRPC `json:"unimplemented_rpcs"`
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
	for _, rpc := range got.UnscopedRPCs {
		assert.Fail(t, "an implemented authenticated rpc resolves no caller",
			"%s: scope it with one of the caller resolvers, or say why it is global with // forge:auth-unscoped-ok: <reason>", rpc)
	}
	var stubs []string
	for _, rpc := range got.UnimplementedRPCs {
		stubs = append(stubs, rpc.String())
	}
	assert.ElementsMatch(t, stubRPCs, stubs,
		"the stubs the audit does not judge are exactly the known ones")
	// The audit still judges everything else: without the resolvers it
	// would count none of these as scoped.
	assert.GreaterOrEqual(t, got.ScopedRPCs, got.AuthenticatedRPCs-len(stubRPCs)-4, // the four acknowledged CatalogService rpcs
		"every authenticated rpc but the stubs and the acknowledged global ones resolves its caller")
}

// auditRPC is one rpc in the audit's unscoped_auth details.
type auditRPC struct {
	Service string `json:"service"`
	Method  string `json:"method"`
}

func (r auditRPC) String() string { return fmt.Sprintf("%s/%s", r.Service, r.Method) }
