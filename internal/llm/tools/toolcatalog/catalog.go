// Copyright (c) 2025 Reliant Labs

// Package toolcatalog is the generated, dependency-free description of which
// tool parameters a human is allowed to bind.
//
// It exists as its own package for one structural reason: internal/llm/tools
// already imports internal/workflow/yaml, so the workflow layer cannot import
// the tool registry back to ask what a tool's parameters are. This package
// imports nothing from either side, so both can depend on it.
//
// The catalog is GENERATED from the live registry — see
// ./cmd/toolconfiggen — rather than hand-maintained. Two lists that must agree
// and are separately edited will eventually disagree, and the failure mode is
// silent: a workflow binds a parameter, nothing rejects it, and the setting
// quietly does nothing.
package toolcatalog

import (
	"sort"
	"sync/atomic"
	"testing"
)

// ToolParams describes one tool's bindable surface.
type ToolParams struct {
	// Bindable is the set of parameter names a human may bind, keyed for
	// lookup. It is the tool's full parameter schema minus Unbindable.
	Bindable map[string]struct{}
	// Unbindable is the set of parameters that exist on the tool but are
	// deliberately not configurable. Held separately from "does not exist"
	// so the error can say WHY rather than claiming the parameter is
	// misspelled — see UnbindableReason.
	Unbindable map[string]string
}

// Tools returns the tool names in the catalog, sorted, for deterministic
// error messages and test output.
func Tools() []string {
	names := make([]string, 0, len(generatedToolParams))
	for name := range generatedToolParams {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Tags returns the tool-tag vocabulary — every tag a tools_config filter
// entry (`tag:X`) may name — sorted, for deterministic error messages and
// test output.
//
// It is the whole vocabulary, not the set of tags some tool currently
// carries: `tag:mcp` expands against the chat's connected MCP servers at
// run time and matches no statically registered tool, yet naming it is
// correct. A tag outside this list matches nothing and never will, which is
// what the validation layer reports.
func Tags() []string {
	tags := make([]string, len(generatedKnownTags))
	copy(tags, generatedKnownTags)
	return tags
}

// Lookup returns the bindable surface of one tool.
func Lookup(toolName string) (ToolParams, bool) {
	params, ok := generatedToolParams[toolName]
	return params, ok
}

// IsBindable reports whether paramName may be bound on toolName.
func IsBindable(toolName, paramName string) bool {
	params, ok := generatedToolParams[toolName]
	if !ok {
		return false
	}
	_, bindable := params.Bindable[paramName]
	return bindable
}

// UnbindableReason returns why a parameter that DOES exist on the tool may not
// be bound. The second return distinguishes "exists but is not bindable" from
// "does not exist at all", which are different mistakes and deserve different
// messages.
func UnbindableReason(toolName, paramName string) (string, bool) {
	params, ok := generatedToolParams[toolName]
	if !ok {
		return "", false
	}
	reason, unbindable := params.Unbindable[paramName]
	return reason, unbindable
}

// BindableNames returns a tool's bindable parameter names, sorted. Used to
// suggest alternatives when a binding names something that does not exist.
func BindableNames(toolName string) []string {
	params, ok := generatedToolParams[toolName]
	if !ok {
		return nil
	}
	names := make([]string, 0, len(params.Bindable))
	for name := range params.Bindable {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// featureGatedOverride replaces generatedFeatureGated for the length of a test
// (GateForTest); nil uses the generated map.
var featureGatedOverride atomic.Pointer[map[string]string]

func featureGated() map[string]string {
	if m := featureGatedOverride.Load(); m != nil {
		return *m
	}
	return generatedFeatureGated
}

// FeatureGated reports whether a tool exists only for callers entitled to a
// feature (tools.FeatureGate), and which. Such a tool still validates by its
// exact name, so an entitled owner's workflow stays valid, but it is never
// offered as a suggestion: to anyone else it does not exist.
func FeatureGated(toolName string) (feature string, gated bool) {
	feature, gated = featureGated()[toolName]
	return feature, gated
}

// FeatureGatedTools is every gated tool name with its feature, a copy.
func FeatureGatedTools() map[string]string {
	out := make(map[string]string, len(featureGated()))
	for name, feature := range featureGated() {
		out[name] = feature
	}
	return out
}

// GateForTest marks toolName as gated behind feature for the length of a
// test. It swaps process-wide state, so the test must not run in parallel.
// tools.GateToolForTest calls it, so the registry and the catalog agree.
func GateForTest(tb testing.TB, toolName, feature string) {
	tb.Helper()
	next := FeatureGatedTools()
	next[toolName] = feature
	prev := featureGatedOverride.Load()
	featureGatedOverride.Store(&next)
	tb.Cleanup(func() { featureGatedOverride.Store(prev) })
}
