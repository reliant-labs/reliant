// Copyright (c) 2025 Reliant Labs
package tools_test

import (
	"sort"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/llm/tools/toolcatalog"
)

// This is the guard the codegen exists for, and it is a TEST rather than a
// `go generate && git diff` step so that it runs wherever the Go suite runs —
// including CI, which executes `go test ./...` and would not run a Makefile
// target nobody wired in.
//
// It re-derives the catalog from the live registry and compares it to the
// checked-in generated file. Two failures it catches, in both directions:
//
//   - A tool gained, lost, or renamed a parameter and the generator was not
//     re-run. The stale catalog then rejects a valid binding as "unknown
//     parameter", or accepts a binding for a parameter that no longer exists.
//   - A tool was added to or removed from the registry. A binding for a tool
//     missing from the catalog is rejected as "no such tool".
//
// Both are the registry/list drift that shipped twice in one day. The check is
// cheap and unconditional — no database, no network, no build tag — so it
// cannot silently skip the way the Postgres-harness suites do.

func TestCatalogMatchesLiveRegistry(t *testing.T) {
	factory := tools.NewToolsFactory(nil)
	registry := tools.GetToolRegistry()
	if len(registry) == 0 {
		t.Fatal("tool registry is empty; every assertion below is a loop and would pass vacuously")
	}

	for _, def := range registry {
		tool := def.Factory(factory)
		if tool == nil {
			t.Errorf("tool %q: factory returned nil", def.Name)
			continue
		}

		catalogued, known := toolcatalog.Lookup(def.Name)
		if !known {
			t.Errorf("tool %q is registered but missing from the catalog; "+
				"run `go generate ./internal/llm/tools/`", def.Name)
			continue
		}

		// The catalog describes what a human MAY bind, which is the full
		// parameter schema — including parameters already bound by default
		// and therefore hidden from ParamSchema.
		live := map[string]struct{}{}
		if schema := tool.ParamSchema(); schema != nil && schema.Properties != nil {
			for pair := schema.Properties.Oldest(); pair != nil; pair = pair.Next() {
				live[pair.Key] = struct{}{}
			}
		}
		if bindable, ok := tool.(tools.BindableTool); ok {
			for _, name := range bindable.Bindings().Names() {
				live[name] = struct{}{}
			}
		}

		for param := range live {
			_, isBindable := catalogued.Bindable[param]
			_, isUnbindable := catalogued.Unbindable[param]
			if !isBindable && !isUnbindable {
				t.Errorf("tool %q has parameter %q, which the catalog does not list at all; "+
					"run `go generate ./internal/llm/tools/`", def.Name, param)
			}
		}

		for param := range catalogued.Bindable {
			if _, exists := live[param]; !exists {
				t.Errorf("catalog lists %q.%q as bindable, but the tool has no such parameter; "+
					"run `go generate ./internal/llm/tools/`", def.Name, param)
			}
		}

		for param := range catalogued.Unbindable {
			if _, exists := live[param]; !exists {
				t.Errorf("catalog lists %q.%q as unbindable, but the tool has no such parameter — "+
					"the policy rule now protects nothing", def.Name, param)
			}
		}
	}
}

// The reverse direction: a catalog entry for a tool that no longer exists.
// Left alone, it would let a workflow bind parameters on a deleted tool and
// call that valid.
func TestCatalogHasNoStaleTools(t *testing.T) {
	registered := map[string]struct{}{}
	for _, def := range tools.GetToolRegistry() {
		registered[def.Name] = struct{}{}
	}
	// The shell tool is registered under one name per platform, but the
	// catalog deliberately carries both so the generated file is identical
	// everywhere. Neither is stale.
	registered["bash"] = struct{}{}
	registered["powershell"] = struct{}{}

	var stale []string
	for _, name := range toolcatalog.Tools() {
		if _, exists := registered[name]; !exists {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("catalog has tools that are not in the registry: %v; "+
			"run `go generate ./internal/llm/tools/`", stale)
	}
}

// The runtime binding path and the load-time catalog must agree on what a
// parameter name means. If they disagree, one of them is wrong and a human's
// configuration silently does nothing on exactly the disagreement.
//
// This pins the agreement behaviorally rather than by comparing two lists:
// every parameter the catalog calls bindable is actually accepted by
// BindTool, and a name the catalog rejects is rejected by BindTool too.
func TestCatalogAgreesWithRuntimeBinding(t *testing.T) {
	factory := tools.NewToolsFactory(nil)

	for _, def := range tools.GetToolRegistry() {
		tool := def.Factory(factory)
		if tool == nil {
			continue
		}
		if _, ok := tool.(tools.BindableTool); !ok {
			continue
		}

		for _, param := range toolcatalog.BindableNames(def.Name) {
			bindings := tools.Bindings{param: tools.LiteralBinding("probe")}
			if _, err := tools.BindTool(tool, bindings); err != nil {
				t.Errorf("catalog says %q.%q is bindable, but the runtime rejected it: %v",
					def.Name, param, err)
			}
		}

		// A name neither the catalog nor the tool knows must be refused by
		// the runtime. Without this the positive half above could pass
		// against a runtime that accepts everything.
		bogus := tools.Bindings{"definitely_not_a_real_parameter": tools.LiteralBinding("x")}
		if _, err := tools.BindTool(tool, bogus); err == nil {
			t.Errorf("tool %q accepted a binding for a parameter it does not have", def.Name)
		}
	}
}
