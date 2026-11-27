// Copyright (c) 2026 Reliant Labs
package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/llm/tools/toolcatalog"
)

// These gate websearch for the length of each test (GateToolForTest), so the
// machinery is tested on a tool whose tags and placement they pick; the
// shipped GTM tools are covered in gtm_tools_test.go. That swaps process-wide
// state, so none of them runs in parallel; Go runs every parallel test after
// them.

// gatedTool is the registry tool the tests gate. It carries the web tag,
// which fetch also carries, so a tag load shows one tool hidden and one not.
const gatedTool = ToolWebSearch

// unknownTool is a name no registry or MCP tool has.
const unknownTool = "zz_no_such_tool"

// asUnknown is what a refusal of unknownTool says, spoken about name.
func asUnknown(refusal, name string) string {
	return strings.ReplaceAll(refusal, unknownTool, name)
}

func gateForTest(t *testing.T) {
	t.Helper()
	GateToolForTest(t, gatedTool, featuregate.GTM)
}

// entitledCaps resolves a "*"-loadable node that preloads view and grants,
// with entitled as the owner's entitlements.
func entitledCaps(entitled map[string]bool, preloaded []string, grants ...string) *Capabilities {
	return ResolveCapabilities(CapabilityInputs{
		Access:           ResolveToolAccess(preloaded, []string{LoadableWildcard}, nil),
		Permission:       PermissionMutating,
		Grants:           grants,
		EntitledFeatures: entitled,
	})
}

func TestFeatureGated_NotEntitledToolDoesNotExist(t *testing.T) {
	gateForTest(t)

	for name, entitled := range map[string]map[string]bool{
		"no entitlements":          nil,
		"another feature":          {"other": true},
		"the feature, named false": {string(featuregate.GTM): false},
	} {
		t.Run(name, func(t *testing.T) {
			caps := entitledCaps(entitled, []string{ToolView, gatedTool, ToolFetch})

			assert.False(t, caps.Offers(gatedTool), "a preloaded gated tool is not offered")
			assert.True(t, caps.Offers(ToolFetch))
			assert.False(t, caps.CanLoad(gatedTool))
			assert.False(t, caps.Searchable(gatedTool))
			assert.NotContains(t, caps.Deferred(), gatedTool, "load_tool's advertised list never names it")
			assert.NotContains(t, caps.EntitledFeatures, string(featuregate.GTM))

			// Every refusal is byte-for-byte the unknown tool's.
			assert.Equal(t, asUnknown(caps.LoadRefusal(unknownTool), gatedTool), caps.LoadRefusal(gatedTool))
			assert.Equal(t, asUnknown(caps.Explain(unknownTool), gatedTool), caps.Explain(gatedTool))

			for _, query := range []string{gatedTool, "web", "search"} {
				for _, r := range SearchTools(query, caps) {
					assert.NotEqual(t, gatedTool, r.Name, "search %q listed the gated tool", query)
				}
			}

			ctx := toolCtxWithCaps(t, caps)
			tool := &loadToolTool{}

			byName, err := tool.Execute(ctx, LoadToolParams{Name: gatedTool})
			require.NoError(t, err)
			unknown, err := tool.Execute(ctx, LoadToolParams{Name: unknownTool})
			require.NoError(t, err)
			assert.True(t, byName.IsError)
			assert.Equal(t, asUnknown(unknown.Content, gatedTool), byName.Content)
			assert.Empty(t, grantsOf(t, byName))

			byTag, err := tool.Execute(ctx, LoadToolParams{Name: "tag:" + string(TagWeb)})
			require.NoError(t, err)
			assert.NotContains(t, byTag.Content, gatedTool, "a tag load neither loads nor refuses it by name")
			assert.NotContains(t, grantsOf(t, byTag), gatedTool)

			search, err := tool.Execute(ctx, LoadToolParams{Query: "web"})
			require.NoError(t, err)
			assert.NotContains(t, search.Content, gatedTool)
		})
	}
}

// A tag every one of whose tools is hidden reads as a tag no tool carries.
func TestFeatureGated_TagOfOnlyHiddenTools(t *testing.T) {
	gateForTest(t)
	GateToolForTest(t, ToolFetch, featuregate.GTM)

	caps := entitledCaps(nil, []string{ToolView})
	resp, err := (&loadToolTool{}).Execute(toolCtxWithCaps(t, caps), LoadToolParams{Name: "tag:" + string(TagWeb)})
	require.NoError(t, err)
	assert.Equal(t, "Tag 'web' is known but no built-in tools carry it, so nothing was loaded.", resp.Content)
}

func TestFeatureGated_EntitledToolLoadsSearchesAndIsOffered(t *testing.T) {
	gateForTest(t)
	entitled := map[string]bool{string(featuregate.GTM): true}

	preloaded := entitledCaps(entitled, []string{ToolView, gatedTool})
	assert.True(t, preloaded.Offers(gatedTool))
	assert.Equal(t, []string{string(featuregate.GTM)}, preloaded.EntitledFeatures)

	caps := entitledCaps(entitled, []string{ToolView})
	assert.False(t, caps.Offers(gatedTool))
	assert.True(t, caps.CanLoad(gatedTool))
	assert.True(t, caps.Searchable(gatedTool))
	assert.Contains(t, caps.Deferred(), gatedTool)

	found := false
	for _, r := range SearchTools(gatedTool, caps) {
		found = found || r.Name == gatedTool
	}
	assert.True(t, found, "search lists the entitled tool")

	ctx := toolCtxWithCaps(t, caps)
	resp, err := (&loadToolTool{}).Execute(ctx, LoadToolParams{Name: gatedTool})
	require.NoError(t, err)
	assert.False(t, resp.IsError, resp.Content)
	assert.Equal(t, []string{gatedTool}, grantsOf(t, resp))

	byTag, err := (&loadToolTool{}).Execute(ctx, LoadToolParams{Name: "tag:" + string(TagWeb)})
	require.NoError(t, err)
	assert.Contains(t, grantsOf(t, byTag), gatedTool)
}

// A grant made while entitled does not outlive a revocation: the next turn's
// set, resolved without the entitlement, offers nothing it granted.
func TestFeatureGated_GrantDroppedAfterRevocation(t *testing.T) {
	gateForTest(t)

	granted := entitledCaps(map[string]bool{string(featuregate.GTM): true}, []string{ToolView}, gatedTool)
	require.True(t, granted.Offers(gatedTool))

	revoked := entitledCaps(nil, []string{ToolView}, gatedTool)
	assert.False(t, revoked.Offers(gatedTool))
	assert.Equal(t, asUnknown(revoked.Explain(unknownTool), gatedTool), revoked.Explain(gatedTool))
}

// execute_tools on any worker decides from the set on the wire, so the
// entitlement must survive the round trip — and a set that predates the field
// entitles nothing.
func TestFeatureGated_EntitlementsRoundTripTheWire(t *testing.T) {
	gateForTest(t)

	caps := entitledCaps(map[string]bool{string(featuregate.GTM): true, "beta": true, "off": false}, []string{ToolView, gatedTool})
	wire := caps.Proto()
	assert.Equal(t, []string{"beta", string(featuregate.GTM)}, wire.GetEntitledFeatures())

	back := CapabilitiesFromProto(wire)
	require.NotNil(t, back)
	assert.Equal(t, caps, back)
	assert.True(t, back.Offers(gatedTool))
	assert.True(t, back.CanLoad(gatedTool))

	old := CapabilitiesFromProto(&reliantv1.ToolCapabilities{Permission: PermissionMutating, LoadableAll: true})
	require.NotNil(t, old)
	assert.False(t, old.CanLoad(gatedTool), "a set with no entitled_features reaches no gated tool")

	legacy := LegacyCapabilities(false)
	assert.False(t, legacy.CanLoad(gatedTool), "the legacy policy entitles nothing")
	assert.Equal(t, asUnknown(legacy.Explain(unknownTool), gatedTool), legacy.Explain(gatedTool))
}

func TestReachedGatedFeatures(t *testing.T) {
	gateForTest(t)

	assert.Equal(t, []featuregate.Feature{featuregate.GTM}, ReachedGatedFeatures(ResolveToolAccess(nil, []string{LoadableWildcard}, nil)))
	assert.Equal(t, []featuregate.Feature{featuregate.GTM}, ReachedGatedFeatures(ResolveToolAccess([]string{gatedTool}, []string{ToolView}, nil)))
	assert.Equal(t, []featuregate.Feature{featuregate.GTM}, ReachedGatedFeatures(ResolveToolAccess(nil, []string{"tag:web"}, nil)))
	assert.Empty(t, ReachedGatedFeatures(ResolveToolAccess([]string{ToolView}, []string{ToolEdit}, nil)))
}

// The test hook gates the tool in the workflow layer's catalog too, so every
// surface agrees; and the shipped catalog's gated set is the registry's.
func TestFeatureGated_CatalogAgrees(t *testing.T) {
	t.Run("shipped", func(t *testing.T) {
		want := map[string]string{}
		for name, f := range FeatureGatedTools() {
			want[name] = string(f)
		}
		assert.Equal(t, want, toolcatalog.FeatureGatedTools(), "run `go generate ./internal/llm/tools/`")
	})
	t.Run("under the test hook", func(t *testing.T) {
		gateForTest(t)
		feature, gated := toolcatalog.FeatureGated(gatedTool)
		assert.True(t, gated)
		assert.Equal(t, string(featuregate.GTM), feature)
	})
	_, gated := toolcatalog.FeatureGated(gatedTool)
	assert.False(t, gated, "the hook is undone when its test ends")
}

// Guards on the shipped gated map: a gated tool has no surface the capability
// set does not govern. None is node-exposed (an invoke_tool node would run it
// with no turn's entitlement), and every one is server-placed, so no machine
// requirement or no-machine refusal ever names it.
func TestFeatureGatedTools_HaveNoUngovernedSurface(t *testing.T) {
	registry := registryIndex()
	for name := range FeatureGatedTools() {
		_, registered := registry[name]
		assert.True(t, registered, "gated tool %q is not in the registry", name)
		assert.False(t, IsNodeExposedTool(name), "gated tool %q is node-exposed", name)
		for _, exposure := range NodeExposedTools() {
			assert.NotEqual(t, name, exposure.Tool, "gated tool %q is node-exposed", name)
		}
		placement, err := PlacementOf(name)
		require.NoError(t, err, name)
		assert.Equal(t, PlacementServer, placement, "gated tool %q is not server-placed", name)
		assert.False(t, NeedsMachine(name), "gated tool %q needs a machine", name)
	}
}
