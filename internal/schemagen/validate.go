package schemagen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Keywords is every keyword [Validate] implements. A schema that uses another
// one is refused by [Compile] rather than validated as if the keyword were
// absent: the generator emits exactly this set, and a test holds the two
// together.
var Keywords = map[string]bool{
	"$schema": true, "$id": true, "$ref": true, "$defs": true,
	"title": true, "description": true, "format": true,
	"type": true, "const": true, "enum": true, "minimum": true,
	"properties": true, "required": true, "additionalProperties": true,
	"items": true, "oneOf": true, "anyOf": true,
}

// Error is one way a document fails its schema: where (a path such as
// `workloads[0].runtime.port`, "" for the document itself) and what.
type Error struct {
	Path    string
	Message string
}

func (e Error) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

// Errors is every failure of one document, in document order.
type Errors []Error

func (es Errors) Error() string {
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = "  - " + e.Error()
	}
	return strings.Join(lines, "\n")
}

// Compiled is a schema ready to validate documents.
type Compiled struct {
	root map[string]any
}

// Compile parses a schema document (the JSON a [Schema] marshals to). It
// refuses a keyword [Validate] does not implement, so no rule is skipped
// silently.
func Compile(schemaJSON []byte) (*Compiled, error) {
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(schemaJSON))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	if err := checkKeywords(root, "#"); err != nil {
		return nil, err
	}
	return &Compiled{root: root}, nil
}

func checkKeywords(s map[string]any, at string) error {
	for k, v := range s {
		if !Keywords[k] {
			return fmt.Errorf("schema %s: keyword %q is not implemented by schemagen.Validate", at, k)
		}
		switch k {
		case "properties", "$defs":
			m, _ := v.(map[string]any)
			for name, sub := range m {
				if sm, ok := sub.(map[string]any); ok {
					if err := checkKeywords(sm, at+"/"+k+"/"+name); err != nil {
						return err
					}
				}
			}
		case "items", "additionalProperties":
			if sm, ok := v.(map[string]any); ok {
				if err := checkKeywords(sm, at+"/"+k); err != nil {
					return err
				}
			}
		case "oneOf", "anyOf":
			list, _ := v.([]any)
			for i, sub := range list {
				if sm, ok := sub.(map[string]any); ok {
					if err := checkKeywords(sm, fmt.Sprintf("%s/%s/%d", at, k, i)); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// DecodeJSON decodes a JSON document the way [Compiled.Validate] expects
// (numbers kept exact). The data must hold exactly one value: a second value
// or trailing garbage after the document is an error, trailing whitespace is
// not.
func DecodeJSON(data []byte) (any, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("data after the JSON document at offset %d", dec.InputOffset())
	}
	return v, nil
}

// Validate checks doc (decoded by [DecodeJSON]) against the schema and returns
// every failure, nil when there is none.
func (c *Compiled) Validate(doc any) Errors {
	v := &validator{root: c.root}
	v.validate(c.root, doc, "")
	return v.errs
}

type validator struct {
	root map[string]any
	errs Errors
}

func (v *validator) fail(path, format string, args ...any) {
	v.errs = append(v.errs, Error{Path: path, Message: fmt.Sprintf(format, args...)})
}

// resolve follows $ref (local `#/$defs/...` only: a generated schema has no
// other kind).
func (v *validator) resolve(s map[string]any) map[string]any {
	for i := 0; i < 64; i++ {
		ref, ok := s["$ref"].(string)
		if !ok {
			return s
		}
		name := strings.TrimPrefix(ref, "#/$defs/")
		name = strings.ReplaceAll(strings.ReplaceAll(name, "~1", "/"), "~0", "~")
		defs, _ := v.root["$defs"].(map[string]any)
		next, _ := defs[name].(map[string]any)
		if next == nil {
			return map[string]any{}
		}
		s = next
	}
	return s
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func (v *validator) validate(s map[string]any, x any, path string) {
	s = v.resolve(s)
	if any, ok := s["anyOf"].([]any); ok {
		v.anyOf(any, x, path)
	}
	if one, ok := s["oneOf"].([]any); ok {
		v.oneOf(one, x, path)
	}
	if t, ok := s["type"]; ok && !typeMatches(t, x) {
		v.fail(path, "must be %s, not %s", describeType(t), jsonKind(x))
		return
	}
	if c, ok := s["const"]; ok && !equalJSON(c, x) {
		v.fail(path, "must be %s, not %s", show(c), show(x))
	}
	if e, ok := s["enum"].([]any); ok {
		found := false
		for _, want := range e {
			if equalJSON(want, x) {
				found = true
				break
			}
		}
		if !found {
			v.fail(path, "%s is not one of %s", show(x), showList(e))
		}
	}
	if m, ok := s["minimum"].(json.Number); ok {
		if n, isNum := x.(json.Number); isNum {
			lo, _ := m.Float64()
			if f, _ := n.Float64(); f < lo {
				v.fail(path, "%s is less than the minimum %s", n, m)
			}
		}
	}
	switch val := x.(type) {
	case map[string]any:
		v.object(s, val, path)
	case []any:
		if items, ok := s["items"].(map[string]any); ok {
			for i, el := range val {
				v.validate(items, el, path+"["+strconv.Itoa(i)+"]")
			}
		}
	}
}

func (v *validator) object(s map[string]any, obj map[string]any, path string) {
	props, _ := s["properties"].(map[string]any)
	if req, ok := s["required"].([]any); ok {
		for _, r := range req {
			name, _ := r.(string)
			val, present := obj[name]
			switch {
			case !present:
				v.fail(join(path, name), "required, and missing")
			case val == nil:
				v.fail(join(path, name), "required, and null")
			}
		}
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if sub, ok := props[k].(map[string]any); ok {
			v.validate(sub, obj[k], join(path, k))
			continue
		}
		switch ap := s["additionalProperties"].(type) {
		case bool:
			if !ap {
				v.fail(join(path, k), "unknown field%s", suggest(k, props))
			}
		case map[string]any:
			v.validate(ap, obj[k], join(path, k))
		}
	}
}

// anyOf: an optional value is `anyOf [<schema>, {type: null}]`, so the useful
// report is the non-null branch's own errors, not "matched none of 2".
func (v *validator) anyOf(branches []any, x any, path string) {
	var real []map[string]any
	for _, b := range branches {
		bm, _ := b.(map[string]any)
		if isNullSchema(bm) {
			if x == nil {
				return
			}
			continue
		}
		real = append(real, bm)
	}
	var best Errors
	for i, b := range real {
		sub := &validator{root: v.root}
		sub.validate(b, x, path)
		if len(sub.errs) == 0 {
			return
		}
		if i == 0 || len(sub.errs) < len(best) {
			best = sub.errs
		}
	}
	if len(real) == 0 {
		v.fail(path, "must be null, not %s", jsonKind(x))
		return
	}
	v.errs = append(v.errs, best...)
}

// oneOf: a tagged union (every branch an object fixing one property to a
// const) is dispatched on that property, so the report names the tag or the
// chosen variant's own fields. Any other oneOf is checked generically.
func (v *validator) oneOf(branches []any, x any, path string) {
	tag, byValue := v.discriminator(branches)
	if tag != "" {
		obj, ok := x.(map[string]any)
		if !ok {
			v.fail(path, "must be object, not %s", jsonKind(x))
			return
		}
		raw, present := obj[tag]
		want := make([]string, 0, len(byValue))
		for k := range byValue {
			want = append(want, k)
		}
		sort.Strings(want)
		if !present || raw == nil {
			v.fail(join(path, tag), "required, and missing (one of: %s)", strings.Join(want, ", "))
			return
		}
		key, _ := raw.(string)
		branch, ok := byValue[key]
		if !ok {
			v.fail(join(path, tag), "%s is not one of: %s", show(raw), strings.Join(want, ", "))
			return
		}
		v.validate(branch, x, path)
		return
	}
	matched := 0
	for _, b := range branches {
		bm, _ := b.(map[string]any)
		sub := &validator{root: v.root}
		sub.validate(bm, x, path)
		if len(sub.errs) == 0 {
			matched++
		}
	}
	if matched != 1 {
		v.fail(path, "must match exactly one of %d alternatives (matched %d)", len(branches), matched)
	}
}

// discriminator returns the property every branch fixes to a distinct string
// const, and each branch by that const; "" when the branches are not a tagged
// union.
func (v *validator) discriminator(branches []any) (string, map[string]map[string]any) {
	tag := ""
	out := map[string]map[string]any{}
	for _, b := range branches {
		bm, _ := b.(map[string]any)
		bm = v.resolve(bm)
		props, _ := bm["properties"].(map[string]any)
		found := ""
		for name, p := range props {
			pm, _ := p.(map[string]any)
			if c, ok := pm["const"].(string); ok && (tag == "" || name == tag) {
				found = name
				if _, dup := out[c]; dup {
					return "", nil
				}
				out[c] = bm
				break
			}
		}
		if found == "" || (tag != "" && found != tag) {
			return "", nil
		}
		tag = found
	}
	return tag, out
}

func isNullSchema(s map[string]any) bool {
	t, ok := s["type"].(string)
	return ok && t == "null" && len(s) == 1
}

func typeMatches(t any, x any) bool {
	switch tt := t.(type) {
	case string:
		return kindIs(tt, x)
	case []any:
		for _, one := range tt {
			if name, _ := one.(string); kindIs(name, x) {
				return true
			}
		}
	}
	return false
}

func kindIs(name string, x any) bool {
	switch name {
	case "null":
		return x == nil
	case "boolean":
		_, ok := x.(bool)
		return ok
	case "string":
		_, ok := x.(string)
		return ok
	case "object":
		_, ok := x.(map[string]any)
		return ok
	case "array":
		_, ok := x.([]any)
		return ok
	case "number":
		_, ok := x.(json.Number)
		return ok
	case "integer":
		n, ok := x.(json.Number)
		if !ok {
			return false
		}
		if _, err := n.Int64(); err == nil {
			return true
		}
		f, err := n.Float64()
		return err == nil && f == math.Trunc(f)
	}
	return false
}

func jsonKind(x any) string {
	switch x.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case json.Number:
		if kindIs("integer", x) {
			return "integer"
		}
		return "number"
	}
	return fmt.Sprintf("%T", x)
}

func describeType(t any) string {
	switch tt := t.(type) {
	case string:
		return tt
	case []any:
		names := make([]string, 0, len(tt))
		for _, n := range tt {
			if s, _ := n.(string); s != "null" {
				names = append(names, s)
			}
		}
		if len(names) < len(tt) {
			return strings.Join(names, " or ") + " (or null)"
		}
		return strings.Join(names, " or ")
	}
	return fmt.Sprint(t)
}

func equalJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

func show(x any) string {
	b, err := json.Marshal(x)
	if err != nil {
		return fmt.Sprint(x)
	}
	if len(b) > 60 {
		return string(b[:57]) + "..."
	}
	return string(b)
}

func showList(xs []any) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = show(x)
	}
	return strings.Join(parts, ", ")
}

// suggest names the declared property closest to an unknown key, or lists the
// declared ones when none is close.
func suggest(key string, props map[string]any) string {
	if len(props) == 0 {
		return " (this object declares no fields)"
	}
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	best, bestD := "", len(key)/2+2
	for _, n := range names {
		if d := levenshtein(strings.ToLower(key), strings.ToLower(n)); d < bestD {
			best, bestD = n, d
		}
	}
	if best != "" {
		return fmt.Sprintf(" (did you mean %q?)", best)
	}
	if len(names) > 12 {
		return fmt.Sprintf(" (known: %s, … %d more)", strings.Join(names[:12], ", "), len(names)-12)
	}
	return " (known: " + strings.Join(names, ", ") + ")"
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}
