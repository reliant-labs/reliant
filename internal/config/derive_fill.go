// derive_fill.go — field-level section defaulting.
//
// Section defaulting used to be all-or-nothing: `if sectionIsZero(c.Database)
// { c.Database = d.Database }`. Writing ANY key inside a section therefore
// suppressed the default for every OTHER key in it, and the fields left empty
// were not neutral — they fed feature derivation.
//
// The measured cost: forge's own migration-safety error recommends
//
//	database:
//	    migration_safety:
//	        allowed_destructive: [db/migrations/0009_harden.up.sql]
//
// as its escape hatch. That block makes the section non-zero, so Driver stays
// "", DeriveFeatureDefaults reads hasDB off it and resolves FeatureMigrations
// to false, and `forge lint --migration-safety` answers "migrations feature is
// disabled in forge.yaml" and exits 0. Following forge's own instructions
// silently switched off the safety check the instructions were about, and a CI
// job running that lint would go green with nothing checked.
//
// The fill is driven by WHICH YAML KEYS THE USER WROTE, not by whether the
// decoded field is zero. That distinction is the whole design: a zero-value
// test cannot tell `race: false` from an absent `race`, so it would resurrect
// every explicitly-disabled boolean whose default is true — trading a silent
// disable for a silent ENABLE, which is the same class of bug pointed the
// other way. Presence comes from the yaml.Node tree, which knows exactly.
//
// ApplyDerivedDefaults (no node in hand — tests, post-load mutation) keeps the
// whole-section semantics, which is correct for its callers: they are filling
// a config nobody hand-wrote.
package config

import (
	"bytes"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

// presentKeys collects the dotted path of every mapping key in the document,
// so "database" and "database.migration_safety.allowed_destructive" are both
// recorded. Sequence contents are not descended into: a list element is a
// value the user wrote wholesale, and there is no per-element default to
// suppress.
func presentKeys(node *yaml.Node) map[string]bool {
	present := map[string]bool{}
	var walk func(n *yaml.Node, path string)
	walk = func(n *yaml.Node, path string) {
		if n == nil || n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			keyNode, valNode := n.Content[i], n.Content[i+1]
			if keyNode.Kind != yaml.ScalarNode {
				continue
			}
			full := joinPath(path, keyNode.Value)
			present[full] = true
			walk(valNode, full)
		}
	}
	walk(node, "")
	return present
}

// fillZeroSection writes def into the section *dst only when *dst is
// entirely zero: the no-node path of ApplyDerivedDefaultsFromNode, which
// cannot tell user-written keys from defaulted ones and so never touches a
// section that holds anything.
func fillZeroSection[T any](dst *T, def T) {
	if reflect.ValueOf(dst).Elem().IsZero() {
		*dst = def
	}
}

// fillSectionDefaults writes def into dst for every field the user did not
// write, recursing into nested structs so a partially-specified block keeps
// the defaults of the keys it left out.
//
// dst must be a pointer to a struct; def is the canonical default for the
// same type. path is dst's dotted position in forge.yaml ("database"), and
// present is the key set from presentKeys.
func fillSectionDefaults(dst, def any, path string, present map[string]bool) {
	dstVal := reflect.ValueOf(dst)
	if dstVal.Kind() != reflect.Pointer || dstVal.IsNil() {
		return
	}
	fillStruct(dstVal.Elem(), reflect.ValueOf(def), path, present)
}

func fillStruct(dst, def reflect.Value, path string, present map[string]bool) {
	// A section the user never mentioned takes the default wholesale —
	// identical to the historical behaviour, and the common case.
	if !present[path] {
		if dst.CanSet() {
			dst.Set(def)
		}
		return
	}
	if dst.Kind() != reflect.Struct || def.Kind() != reflect.Struct {
		return
	}
	for i := range dst.NumField() {
		name, ok := yamlFieldName(dst.Type().Field(i))
		if !ok {
			continue
		}
		field, defField := dst.Field(i), def.Field(i)
		if !field.CanSet() {
			continue
		}
		full := joinPath(path, name)
		if !present[full] {
			field.Set(defField)
			continue
		}
		// The key IS present. Descend into a nested mapping so its own
		// unwritten children still default; a scalar, slice or map the
		// user wrote is taken literally.
		if field.Kind() == reflect.Struct {
			fillStruct(field, defField, full, present)
		}
	}
}

// stripSectionDefaults is fillSectionDefaults' inverse, used by
// NormalizeForWrite: every field byte-identical to its derived default is
// reset to zero so it marshals away, and a section whose every field matched
// collapses to the zero value (i.e. vanishes from forge.yaml entirely).
//
// The two must stay symmetric or a round-trip changes meaning. They are,
// field by field: a dropped key is absent on the next load, so the fill
// re-derives exactly the value that was dropped — and a key that DIFFERS from
// its default is kept, which is what keeps `race: false` from being normalized
// into oblivion and then re-derived as true.
func stripSectionDefaults(dst, def any) {
	dstVal := reflect.ValueOf(dst)
	if dstVal.Kind() != reflect.Pointer || dstVal.IsNil() {
		return
	}
	stripStruct(dstVal.Elem(), reflect.ValueOf(def))
}

func stripStruct(dst, def reflect.Value) {
	if dst.Kind() != reflect.Struct || def.Kind() != reflect.Struct {
		return
	}
	for i := range dst.NumField() {
		if _, ok := yamlFieldName(dst.Type().Field(i)); !ok {
			continue
		}
		field, defField := dst.Field(i), def.Field(i)
		if !field.CanSet() {
			continue
		}
		if valuesEquivalent(field, defField) {
			field.Set(reflect.Zero(field.Type()))
			continue
		}
		if field.Kind() == reflect.Struct {
			stripStruct(field, defField)
		}
	}
}

// valuesEquivalent compares by canonical YAML rendering — the representation
// that actually round-trips through forge.yaml — so nil-vs-empty slice and
// pointer-identity noise does not read as a difference. Same contract as
// sectionsEquivalent, one level down.
func valuesEquivalent(a, b reflect.Value) bool {
	ab, errA := yaml.Marshal(a.Interface())
	bb, errB := yaml.Marshal(b.Interface())
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

// yamlFieldName returns the forge.yaml key a struct field marshals to, and
// false for fields that carry none (untagged, or `yaml:"-"`).
func yamlFieldName(f reflect.StructField) (string, bool) {
	tag := f.Tag.Get("yaml")
	if tag == "" || tag == "-" {
		return "", false
	}
	name := strings.SplitN(tag, ",", 2)[0]
	if name == "" {
		name = strings.ToLower(f.Name)
	}
	return name, true
}
