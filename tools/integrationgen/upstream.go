// Copyright (c) 2025 Reliant Labs
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// upstream is the vendored @octokit/webhooks-schemas: one JSON Schema whose
// `definitions` hold one object per event (`issues$opened`, `push$event`) and
// the shared pieces they `$ref`.
//
// The generator never copies a type out of it. What an event emits is decided
// by the projection (text() of a null is ""), so the emitted type comes from
// the spec. The upstream schema is the check: every path a spec keeps must
// exist in the event's definition, with a type its kind can read. A bump that
// renames, removes or retypes a field the projection keeps fails the
// regeneration instead of surfacing as an event that quietly stops carrying it.
type upstream struct {
	defs map[string]any
}

func loadUpstream(path, wantSHA string) (*upstream, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		return nil, fmt.Errorf("%s: sha256 is %s, but provider.yaml pins %s: a vendored schema changes together with its pin (upstream.version and upstream.sha256)", path, got, wantSHA)
	}
	var doc struct {
		Definitions map[string]any `json:"definitions"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(doc.Definitions) == 0 {
		return nil, fmt.Errorf("%s: no definitions", path)
	}
	return &upstream{defs: doc.Definitions}, nil
}

// node is a position in the upstream schema: the schemas that apply there. A
// definition built from allOf/anyOf/oneOf and $ref is several schemas at once,
// and a property is reachable when any of them declares it.
type node []map[string]any

func (u *upstream) root(def string) (node, error) {
	d, ok := u.defs[def].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("upstream has no definition %q", def)
	}
	return u.flatten(d, 0), nil
}

func (u *upstream) flatten(s map[string]any, depth int) node {
	if depth > 24 {
		return nil
	}
	if ref, ok := s["$ref"].(string); ok {
		name := ref[strings.LastIndex(ref, "/")+1:]
		if d, ok := u.defs[name].(map[string]any); ok {
			return u.flatten(d, depth+1)
		}
		return nil
	}
	out := node{s}
	for _, k := range []string{"allOf", "anyOf", "oneOf"} {
		list, _ := s[k].([]any)
		for _, e := range list {
			if m, ok := e.(map[string]any); ok {
				out = append(out, u.flatten(m, depth+1)...)
			}
		}
	}
	return out
}

// child is the property key of every schema in n that declares it.
func (u *upstream) child(n node, key string) node {
	var out node
	for _, s := range n {
		props, _ := s["properties"].(map[string]any)
		if p, ok := props[key].(map[string]any); ok {
			out = append(out, u.flatten(p, 0)...)
		}
	}
	return out
}

// items is the element schema of every array schema in n.
func (u *upstream) items(n node) node {
	var out node
	for _, s := range n {
		if it, ok := s["items"].(map[string]any); ok {
			out = append(out, u.flatten(it, 0)...)
		}
	}
	return out
}

// walk follows a dotted path from n. An array on the way is entered through
// its elements, so `commits.author.name` reads the author of a commit.
func (u *upstream) walk(n node, path []string) (node, error) {
	cur := n
	for i, seg := range path {
		cur = u.enterArrays(cur)
		next := u.child(cur, seg)
		if len(next) == 0 {
			return nil, fmt.Errorf("upstream has no %q (after %s)", seg, joinPath(path[:i]))
		}
		cur = next
	}
	return cur, nil
}

// enterArrays steps into the elements of any array schema in n, when n is only
// that and declares no properties of its own.
func (u *upstream) enterArrays(n node) node {
	hasProps := false
	for _, s := range n {
		if _, ok := s["properties"]; ok {
			hasProps = true
		}
	}
	if hasProps {
		return n
	}
	if it := u.items(n); len(it) > 0 {
		return it
	}
	return n
}

// types is every JSON type the schemas at n allow, "string" included for an
// enum of strings; empty when upstream does not say.
func (n node) types() map[string]bool {
	out := map[string]bool{}
	for _, s := range n {
		switch t := s["type"].(type) {
		case string:
			out[t] = true
		case []any:
			for _, e := range t {
				if str, ok := e.(string); ok {
					out[str] = true
				}
			}
		}
		if _, ok := s["properties"]; ok {
			out["object"] = true
		}
		if _, ok := s["items"]; ok {
			out["array"] = true
		}
		if list, ok := s["enum"].([]any); ok {
			for _, e := range list {
				switch e.(type) {
				case string:
					out["string"] = true
				case float64:
					out["number"] = true
				case bool:
					out["boolean"] = true
				}
			}
		}
	}
	return out
}

// accepts reports whether a field of the given spec kind can read a value of
// the upstream types. An upstream that says nothing accepts anything.
func accepts(kind string, types map[string]bool) bool {
	if len(types) == 0 {
		return true
	}
	switch kind {
	case "string":
		return types["string"]
	case "integer":
		return types["integer"] || types["number"]
	case "boolean":
		return types["boolean"]
	case "object", "present":
		return types["object"]
	case "array", "names", "strings", "integers", "list":
		return types["array"]
	}
	return true
}

func joinPath(p []string) string {
	if len(p) == 0 {
		return "<event>"
	}
	return strings.Join(p, ".")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
