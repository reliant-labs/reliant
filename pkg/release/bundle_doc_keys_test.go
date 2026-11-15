package release

import (
	"encoding"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// bundleDocKeyPaths is every JSON key path a strict decode of t accepts:
// "provenance.commit", "shape.objects[].kind", "pins.images{}" (a map's
// values). A leaf is a path with nothing below it: a scalar, a type with its
// own UnmarshalJSON/UnmarshalText, or raw JSON.
//
// testdata/bundle_doc_keys.ee3bded4.txt is this function run over forge
// ee3bded4's BundleDoc — the decoder every control plane built before
// forge#850 runs (scripts: see that file's header). Keep the two in step.
func bundleDocKeyPaths(t reflect.Type) []string {
	set := map[string]bool{}
	walkKeyPaths(t, "", set, map[reflect.Type]bool{})
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

var (
	jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
	rawMessage      = reflect.TypeFor[json.RawMessage]()
)

func walkKeyPaths(t reflect.Type, prefix string, set map[string]bool, onPath map[reflect.Type]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == rawMessage || reflect.PointerTo(t).Implements(jsonUnmarshaler) || reflect.PointerTo(t).Implements(textUnmarshaler) {
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		if onPath[t] {
			return
		}
		onPath[t] = true
		defer delete(onPath, t)
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if f.Anonymous && name == "" {
				walkKeyPaths(f.Type, prefix, set, onPath)
				continue
			}
			if name == "" {
				name = f.Name
			}
			p := joinKeyPath(prefix, name)
			set[p] = true
			walkKeyPaths(f.Type, p, set, onPath)
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return // []byte is a base64 string
		}
		set[prefix+"[]"] = true
		walkKeyPaths(t.Elem(), prefix+"[]", set, onPath)
	case reflect.Map:
		set[prefix+"{}"] = true
		walkKeyPaths(t.Elem(), prefix+"{}", set, onPath)
	}
}

func joinKeyPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// unknownKeyPaths returns every key path in the encoded document that a
// decoder accepting exactly allowed would refuse — what
// json.Decoder.DisallowUnknownFields reports as `unknown field`.
func unknownKeyPaths(doc []byte, allowed []string) ([]string, error) {
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(allowed))
	for _, p := range allowed {
		set[p] = true
	}
	var unknown []string
	walkJSONKeys(v, "", set, &unknown)
	sort.Strings(unknown)
	return unknown, nil
}

func walkJSONKeys(v any, prefix string, set map[string]bool, unknown *[]string) {
	switch x := v.(type) {
	case map[string]any:
		if prefix != "" && !hasChildren(prefix, set) {
			return // a leaf type with its own decoding (or raw JSON)
		}
		if set[prefix+"{}"] {
			for _, val := range x {
				walkJSONKeys(val, prefix+"{}", set, unknown)
			}
			return
		}
		for k, val := range x {
			p := joinKeyPath(prefix, k)
			if !set[p] {
				*unknown = append(*unknown, p)
				continue
			}
			walkJSONKeys(val, p, set, unknown)
		}
	case []any:
		if !set[prefix+"[]"] {
			return
		}
		for _, val := range x {
			walkJSONKeys(val, prefix+"[]", set, unknown)
		}
	}
}

func hasChildren(prefix string, set map[string]bool) bool {
	for p := range set {
		if strings.HasPrefix(p, prefix+".") || strings.HasPrefix(p, prefix+"[]") || strings.HasPrefix(p, prefix+"{}") {
			return true
		}
	}
	return false
}
