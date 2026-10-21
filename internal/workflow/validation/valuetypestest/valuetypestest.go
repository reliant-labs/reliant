// Copyright (c) 2025 Reliant Labs
// Package valuetypestest is a map-backed fake of the resolver for integration
// input types (validation.ValueTypes, triggerspec.ValueTypes), for tests that
// need `type: github.repository` to resolve without a catalog.
//
//forge:exclude-contract: a test fake, the substitute a contract would generate; it is no boundary
package valuetypestest

import "sort"

// Type is one input type the fake declares.
type Type struct {
	// Value is the JSON Schema of one value.
	Value map[string]any
	// Scope is scope key -> the type ref the input bound to it must have.
	Scope map[string]string
}

// Fake declares input types by ref (`github.repository`).
type Fake map[string]Type

// GitHub is the fake the tests share: a repository (owner/name), a label that
// is scoped to a repository, and a numeric user id.
func GitHub() Fake {
	return Fake{
		"github.repository": {Value: map[string]any{
			"type":    "string",
			"pattern": `^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`,
		}},
		"github.label": {
			Value: map[string]any{"type": "string", "minLength": 1, "maxLength": 50},
			Scope: map[string]string{"repository": "github.repository"},
		},
		"github.user": {Value: map[string]any{"type": "string", "pattern": `^[0-9]+$`}},
	}
}

func (f Fake) ValueSchema(ref string) (map[string]any, bool) {
	t, ok := f[ref]
	if !ok {
		return nil, false
	}
	return t.Value, true
}

func (f Fake) TypeRefs() []string {
	refs := make([]string, 0, len(f))
	for ref := range f {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

func (f Fake) ScopeTypes(ref string) map[string]string { return f[ref].Scope }
