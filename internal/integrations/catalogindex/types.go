// Copyright (c) 2025 Reliant Labs

package catalogindex

import (
	"sort"

	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// An integration's value types (github.repository) are indexed beside its
// actions and triggers, under the qualified ref. The methods below are the
// whole of what a consumer that only needs the types (workflow validation,
// activation params) asks of the catalog, so they are declared by those
// consumers and satisfied here.

// Type returns the entry of a value type by its qualified ref
// (github.repository).
func (idx *Index) Type(ref string) (*Entry, bool) {
	e, ok := idx.byRef[ref]
	if !ok || e.Kind != KindType {
		return nil, false
	}
	return e, true
}

// ValueSchema is the JSON Schema of ONE value of the type ref, a fresh map;
// ok is false when no integration declares ref.
func (idx *Index) ValueSchema(ref string) (map[string]any, bool) {
	e, ok := idx.Type(ref)
	if !ok {
		return nil, false
	}
	return e.ValueSchema(), true
}

// TypeRefs lists every type ref the integrations declare, sorted.
func (idx *Index) TypeRefs() []string {
	var refs []string
	for _, e := range idx.entries {
		if e.Kind == KindType {
			refs = append(refs, e.Ref)
		}
	}
	sort.Strings(refs)
	return refs
}

// ScopeTypes names the other typed values the options of the type ref depend
// on: scope key -> the qualified type ref a value bound to that key must have.
// nil for a type with no scope, or an unknown one.
func (idx *Index) ScopeTypes(ref string) map[string]string {
	e, ok := idx.Type(ref)
	if !ok || len(e.Type.GetOptions().GetScope()) == 0 {
		return nil
	}
	out := make(map[string]string, len(e.Type.GetOptions().GetScope()))
	for key, sc := range e.Type.GetOptions().GetScope() {
		out[key] = manifest.QualifyTypeRef(e.IntegrationID(), sc.GetType())
	}
	return out
}
