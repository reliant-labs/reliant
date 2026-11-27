// Copyright (c) 2026 Reliant Labs
package tools

import (
	"sort"
	"sync/atomic"
	"testing"

	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/llm/tools/toolcatalog"
)

// featureGatedTools maps every tool that exists only for callers entitled to
// a feature to that feature, as connectionGated maps an integration's tools
// to its manifest. To anyone not entitled, a gated tool does not exist: it is
// not offered, loadable, searchable or advertised by load_tool, not loaded by
// a tag, not listed by CatalogService.ListTools or the public tool docs, not
// suggested by workflow validation, and a call to it is refused as an unknown
// tool. Whether a caller is entitled is decided per turn (call_llm asks
// featuregate.Gate) and carried on the capability set, never baked into the
// registry, which is built caller-blind.
//
// The registry row of a gated tool is an ordinary one; only this map gates it.
// toolcatalog carries a generated copy of its keys for the workflow layer,
// which cannot import this package.
var featureGatedTools = map[string]featuregate.Feature{
	// GTM (gtm_tools.go): every GTM tool, reads and send alike.
	ToolListGtmIdeas:           featuregate.GTM,
	ToolSaveGtmIdea:            featuregate.GTM,
	ToolListGtmStrategies:      featuregate.GTM,
	ToolSaveGtmStrategy:        featuregate.GTM,
	ToolListGtmStrategyLibrary: featuregate.GTM,
	ToolListGtmCampaigns:       featuregate.GTM,
	ToolSaveGtmCampaign:        featuregate.GTM,
	ToolListGtmCalendar:        featuregate.GTM,
	ToolSaveGtmItem:            featuregate.GTM,
	ToolListGtmChannels:        featuregate.GTM,
	ToolSaveGtmChannel:         featuregate.GTM,
	ToolGetGtmPositioning:      featuregate.GTM,
	ToolCheckGtmClaims:         featuregate.GTM,
	ToolReadProductAnalytics:   featuregate.GTM,
	ToolAttachGtmAsset:         featuregate.GTM,
	ToolSendGtmEmail:           featuregate.GTM,
}

// featureGatedOverride replaces featureGatedTools for the length of a test
// (GateToolForTest); nil uses the map.
var featureGatedOverride atomic.Pointer[map[string]featuregate.Feature]

func featureGated() map[string]featuregate.Feature {
	if m := featureGatedOverride.Load(); m != nil {
		return *m
	}
	return featureGatedTools
}

// FeatureGate reports the feature a tool is gated behind, if it is gated.
func FeatureGate(name string) (featuregate.Feature, bool) {
	f, gated := featureGated()[name]
	return f, gated
}

// FeatureGatedTools is every gated tool name with its feature, a copy.
func FeatureGatedTools() map[string]featuregate.Feature {
	out := make(map[string]featuregate.Feature, len(featureGated()))
	for name, f := range featureGated() {
		out[name] = f
	}
	return out
}

// ReachedGatedFeatures lists, sorted, the features of the gated tools a
// declaration can reach at all — preloaded, loadable, or anything when it may
// load anything. These are the features worth asking the gate about; a
// declaration that reaches none never asks.
func ReachedGatedFeatures(access ToolAccess) []featuregate.Feature {
	seen := map[featuregate.Feature]bool{}
	for name, f := range featureGated() {
		if seen[f] {
			continue
		}
		if access.LoadableAll || containsName(access.Preloaded, name) || containsName(access.Loadable, name) {
			seen[f] = true
		}
	}
	out := make([]featuregate.Feature, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// GateToolForTest gates the registry tool name behind f for the length of a
// test, here and in toolcatalog, so the gating machinery can be tested on a
// tool whose tags and placement the test picks. It swaps
// process-wide state, so the test that calls it must not run in parallel.
func GateToolForTest(tb testing.TB, name string, f featuregate.Feature) {
	tb.Helper()
	next := FeatureGatedTools()
	next[name] = f
	prev := featureGatedOverride.Load()
	featureGatedOverride.Store(&next)
	tb.Cleanup(func() { featureGatedOverride.Store(prev) })
	toolcatalog.GateForTest(tb, name, string(f))
}

// entitledTo reports whether the set names feature f as entitled.
func (c *Capabilities) entitledTo(f featuregate.Feature) bool {
	return c != nil && containsSorted(c.EntitledFeatures, string(f))
}

// notEntitled reports whether name is a gated tool whose feature the set does
// not entitle.
func (c *Capabilities) notEntitled(name string) bool {
	f, gated := FeatureGate(name)
	return gated && !c.entitledTo(f)
}

// HidesTool reports whether name is a feature-gated tool this set does not
// entitle, so that to its caller the tool does not exist. A nil set (a batch
// that predates capability sets, or a path that carries none) entitles
// nothing, so it hides every gated tool.
func (c *Capabilities) HidesTool(name string) bool { return c.notEntitled(name) }

// entitledFeatureNames is the sorted names of the features in, true only.
func entitledFeatureNames(in map[string]bool) []string {
	var names []string
	for name, entitled := range in {
		if entitled && name != "" {
			names = append(names, name)
		}
	}
	return sortedUnique(names)
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
