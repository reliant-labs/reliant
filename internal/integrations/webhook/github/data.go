// Copyright (c) 2025 Reliant Labs
package github

import (
	"encoding/json"
	"strconv"
)

// Helpers the generated projections (zz_projection_gen.go, from projection/*.yaml)
// are written in. What an event keeps is decided by its spec, not here.
//
// The recorded payload is a TRIMMED copy of the webhook body (provider.yaml,
// "caps"): long text is capped, emails and API-only URLs are dropped, and the
// whole payload has a hard size bound.

// --- caps -----------------------------------------------------------------

func capText(s string) string {
	if len(s) <= maxTextBytes {
		return s
	}
	cut := maxTextBytes
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + truncatedSuffix
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func capList(v any) []any {
	items := list(v)
	if len(items) > maxListItems {
		items = items[:maxListItems]
	}
	out := make([]any, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func names(v any, key string) []any {
	out := []any{}
	for _, it := range list(v) {
		if s := text(dig(it, key)); s != "" {
			out = append(out, s)
		}
		if len(out) == maxListItems {
			break
		}
	}
	return out
}

// shrinkRule is how an event cuts an oversized payload down to maxDataBytes,
// from its spec: the named list loses the drop keys of every element, then
// elements from the end, until the payload fits, and flag is set.
type shrinkRule struct {
	list string
	drop []string
	flag string
}

func capData(data map[string]any, r *shrinkRule) map[string]any {
	if r == nil || size(data) <= maxDataBytes {
		return data
	}
	if items, ok := data[r.list].([]any); ok {
		for _, it := range items {
			if m, ok := it.(map[string]any); ok {
				for _, k := range r.drop {
					delete(m, k)
				}
			}
		}
		for len(items) > 0 && size(data) > maxDataBytes {
			items = items[:len(items)-1]
			data[r.list] = items
		}
		data[r.flag] = true
	}
	return data
}

func size(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// --- accessors over decoded JSON ------------------------------------------

func dig(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func str(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && s != ""
}

func text(v any) string {
	s, _ := v.(string)
	return s
}

// num keeps a JSON number as an int64 when it is one (ids, counts), so the
// payload carries 42 rather than 4.2e1 and a filter compares it exactly.
func num(v any) any {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := n.Float64(); err == nil {
			return f
		}
	case float64:
		return n
	}
	return nil
}

// intString renders a JSON integer id as a string key.
func intString(v any) (string, bool) {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil && i != 0 {
			return strconv.FormatInt(i, 10), true
		}
	case float64:
		if n != 0 {
			return strconv.FormatInt(int64(n), 10), true
		}
	}
	return "", false
}

// integers is the numbers under key of each element of a list: the pull
// request numbers of a check, for one.
func integers(v any, key string) []any {
	out := []any{}
	for _, it := range list(v) {
		if n := num(dig(it, key)); n != nil {
			out = append(out, n)
		}
	}
	return out
}

// objects projects the elements of a list with fn, at most max of them.
func objects(v any, max int, fn func(any) map[string]any) []any {
	items := list(v)
	if len(items) > max {
		items = items[:max]
	}
	out := make([]any, 0, len(items))
	for _, it := range items {
		out = append(out, fn(it))
	}
	return out
}

// hasText reports whether v is a non-empty string.
func hasText(v any) bool {
	_, ok := str(v)
	return ok
}
