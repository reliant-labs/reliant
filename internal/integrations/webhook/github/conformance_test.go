// Copyright (c) 2025 Reliant Labs
package github

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	gojsonschema "github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
)

// eventSchemas maps each event type to the `data` schema of the trigger that
// declares it, as the embedded manifest ships it.
func eventSchemas(t *testing.T) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, m := range catalog.MustBuiltin().Manifests() {
		if m.GetId() != ProviderID {
			continue
		}
		for _, tr := range m.GetTriggers() {
			for _, ev := range tr.GetEvents() {
				out[ev] = tr.GetData().AsMap()
			}
		}
	}
	require.NotEmpty(t, out, "no embedded github triggers")
	return out
}

// fixtureEvents parses every recorded webhook body under testdata with the
// header its file name starts with, and returns the events each emitted, by
// event type. A body that emits nothing (a ping, an installation change) has no
// data to check.
func fixtureEvents(t *testing.T) map[string][]Event {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	out := map[string][]Event{}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		header, _, _ := strings.Cut(name, ".")
		body, err := os.ReadFile(f)
		require.NoError(t, err)
		p, err := Parse(header, body, received)
		require.NoError(t, err, name)
		for _, ev := range p.Events {
			out[ev.Type] = append(out[ev.Type], ev)
		}
	}
	// A push too big to record whole is cut and flagged; no recorded body is.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(fixture(t, "push"), &raw))
	big := make([]any, maxCommits)
	for i := range big {
		big[i] = map[string]any{"id": "c", "message": strings.Repeat("m", 3000), "added": []any{strings.Repeat("dir/", 60) + "file.go"}}
	}
	raw["commits"] = big
	body, err := json.Marshal(raw)
	require.NoError(t, err)
	p, err := Parse("push", body, received)
	require.NoError(t, err)
	for _, ev := range p.Events {
		out[ev.Type] = append(out[ev.Type], ev)
	}
	return out
}

func asJSONValue(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var out any
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

// Every recorded webhook's emitted Data validates against the data schema of
// its event, additionalProperties: false included, and every property the
// schema declares (at any depth) is produced by at least one fixture. The first keeps the schema true to the parser; the second keeps the
// schema from promising what no event carries.
func TestEmittedDataConformsToTheDeclaredSchema(t *testing.T) {
	schemas := eventSchemas(t)
	events := fixtureEvents(t)

	types := make([]string, 0, len(events))
	for typ := range events {
		types = append(types, typ)
	}
	sort.Strings(types)

	// A property is covered when some fixture of ANY event that declares it
	// produces it. The issue and pull request schemas are shared by many events,
	// and an `issues.opened` never carries `closed_at` however well it is
	// recorded; `issues.closed` is where that property is proven.
	producedBy := map[string]map[string]bool{} // event -> paths
	declaredBy := map[string]map[string]bool{}
	for _, typ := range types {
		raw, ok := schemas[typ]
		require.True(t, ok, "event type %q has no manifest trigger", typ)
		b, err := json.Marshal(raw)
		require.NoError(t, err)
		var s gojsonschema.Schema
		require.NoError(t, json.Unmarshal(b, &s), typ)
		resolved, err := s.Resolve(nil)
		require.NoError(t, err, typ)

		producedBy[typ] = map[string]bool{}
		for i, ev := range events[typ] {
			data := asJSONValue(t, ev.Data)
			if err := resolved.Validate(data); err != nil {
				t.Errorf("%s (fixture %d): emitted data does not validate: %v", typ, i, err)
			}
			collectPaths("", data, producedBy[typ])

			// The schema is closed at its top level: a key it does not declare
			// must fail, or the check above proves nothing.
			if m, ok := data.(map[string]any); ok && i == 0 {
				m["zz_undeclared"] = true
				if resolved.Validate(m) == nil {
					t.Errorf("%s: the data schema accepts an undeclared top-level key", typ)
				}
			}
		}
		declaredBy[typ] = map[string]bool{}
		collectSchemaPaths("", raw, declaredBy[typ])
	}
	// Nested objects are open in the schema, so validation alone lets an emitted
	// nested key through undeclared; require every emitted path to be declared.
	for _, typ := range types {
		var undeclared []string
		for p := range producedBy[typ] {
			if !declaredBy[typ][p] {
				undeclared = append(undeclared, p)
			}
		}
		sort.Strings(undeclared)
		if len(undeclared) > 0 {
			t.Errorf("%s emits data properties its schema does not declare: %s", typ, strings.Join(undeclared, ", "))
		}
	}
	for _, typ := range types {
		var missing []string
		for p := range declaredBy[typ] {
			covered := false
			for other := range declaredBy {
				if declaredBy[other][p] && producedBy[other][p] {
					covered = true
					break
				}
			}
			if !covered {
				missing = append(missing, p)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s declares data properties no fixture produces: %s", typ, strings.Join(missing, ", "))
		}
	}

	for typ := range schemas {
		if _, ok := events[typ]; !ok {
			t.Errorf("event type %q is declared by the manifest but no fixture emits it", typ)
		}
	}
}

// collectPaths records the path of every object key in v; array elements share
// the path segment "[]".
func collectPaths(prefix string, v any, out map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			p := prefix + "." + k
			out[p] = true
			collectPaths(p, val, out)
		}
	case []any:
		for _, e := range t {
			collectPaths(prefix+"[]", e, out)
		}
	}
}

// collectSchemaPaths records the path of every property the schema declares,
// in the same notation as collectPaths.
func collectSchemaPaths(prefix string, schema map[string]any, out map[string]bool) {
	if props, ok := schema["properties"].(map[string]any); ok {
		for k, v := range props {
			p := prefix + "." + k
			out[p] = true
			if sub, ok := v.(map[string]any); ok {
				collectSchemaPaths(p, sub, out)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		collectSchemaPaths(prefix+"[]", items, out)
	}
}
