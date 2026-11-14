// Package schemagen derives a JSON Schema (draft 2020-12) from the Go types
// that decode a JSON document, and validates documents against such a schema
// with errors that name the path and the field.
//
// It exists so a wire contract has ONE source: the Go structs encoding/json
// decodes into. A hand-written schema beside them is a second copy that drifts
// the first time someone adds a field; a schema generated from them cannot.
//
// The generator follows encoding/json's own rules, so what it describes is
// what the decoder accepts:
//
//   - a struct is an object whose properties are its exported fields, named by
//     their `json` tag (the Go field name when there is none); `json:"-"` and
//     unexported fields are not properties; an embedded struct with no name in
//     its tag is flattened into its parent, shallower fields winning;
//   - every named struct becomes one `$defs` entry, referenced by `$ref`, so a
//     type that appears in twenty places is described once;
//   - a property may be null unless it is required: encoding/json accepts null
//     for every field, and a render projects an unset optional field as null;
//   - an object accepts no property its struct does not declare. Unknown keys
//     are what a typo looks like, and a hand-written document is exactly where
//     typos happen.
//
// What reflection cannot see — a field decoded through json.RawMessage into a
// union, a type with its own UnmarshalJSON, which fields are required — is
// stated in [Options], next to the types, and nowhere else.
package schemagen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Draft is the JSON Schema dialect every generated schema declares.
const Draft = "https://json-schema.org/draft/2020-12/schema"

// Schema is one JSON Schema node. Only the keywords the generator emits and
// [Validate] implements are fields: a schema is generated, never hand-written,
// so there is no need to model the rest of the vocabulary.
type Schema struct {
	Dialect     string
	ID          string
	Title       string
	Description string
	Ref         string
	// Types is the `type` keyword: one name, or several (["string","null"]).
	Types []string
	Const any
	// Properties keeps declaration order, so the output is stable and reads
	// in the order the Go struct declares its fields.
	Properties []Property
	Required   []string
	// AdditionalProperties is `false` when NoAdditional is set, else the
	// schema every undeclared property's value must match (nil: no keyword).
	AdditionalProperties *Schema
	NoAdditional         bool
	Items                *Schema
	OneOf                []*Schema
	AnyOf                []*Schema
	Minimum              *float64
	Format               string
	Defs                 map[string]*Schema
}

// Property is one named member of an object schema.
type Property struct {
	Name   string
	Schema *Schema
}

// MarshalJSON writes the schema with its keywords in a fixed order, so a
// regenerated schema is byte-identical when nothing changed.
func (s *Schema) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	put := func(key string, v any) error {
		if !first {
			b.WriteByte(',')
		}
		first = false
		k, _ := json.Marshal(key)
		b.Write(k)
		b.WriteByte(':')
		enc, err := json.Marshal(v)
		if err != nil {
			return err
		}
		b.Write(enc)
		return nil
	}
	type kv struct {
		key string
		val any
		set bool
	}
	var typ any
	switch len(s.Types) {
	case 0:
	case 1:
		typ = s.Types[0]
	default:
		typ = s.Types
	}
	var props *orderedProps
	if len(s.Properties) > 0 {
		props = &orderedProps{s.Properties}
	}
	var additional any
	if s.NoAdditional {
		additional = false
	} else if s.AdditionalProperties != nil {
		additional = s.AdditionalProperties
	}
	var defs *orderedProps
	if len(s.Defs) > 0 {
		names := make([]string, 0, len(s.Defs))
		for n := range s.Defs {
			names = append(names, n)
		}
		sort.Strings(names)
		defs = &orderedProps{}
		for _, n := range names {
			defs.props = append(defs.props, Property{Name: n, Schema: s.Defs[n]})
		}
	}
	fields := []kv{
		{"$schema", s.Dialect, s.Dialect != ""},
		{"$id", s.ID, s.ID != ""},
		{"$ref", s.Ref, s.Ref != ""},
		{"title", s.Title, s.Title != ""},
		{"description", s.Description, s.Description != ""},
		{"type", typ, typ != nil},
		{"const", s.Const, s.Const != nil},
		{"format", s.Format, s.Format != ""},
		{"minimum", s.Minimum, s.Minimum != nil},
		{"properties", props, props != nil},
		{"required", s.Required, len(s.Required) > 0},
		{"additionalProperties", additional, additional != nil},
		{"items", s.Items, s.Items != nil},
		{"oneOf", s.OneOf, len(s.OneOf) > 0},
		{"anyOf", s.AnyOf, len(s.AnyOf) > 0},
		{"$defs", defs, defs != nil},
	}
	for _, f := range fields {
		if !f.set {
			continue
		}
		if err := put(f.key, f.val); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

type orderedProps struct{ props []Property }

func (o *orderedProps) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range o.props {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(p.Name)
		b.Write(k)
		b.WriteByte(':')
		v, err := json.Marshal(p.Schema)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// FieldKey names one struct field by its owning type and its JSON name.
type FieldKey struct {
	Type reflect.Type
	Name string
}

// Options configures one generation. Everything reflection cannot infer is
// declared here.
type Options struct {
	ID          string
	Title       string
	Description string
	// Required lists, per struct type, the JSON names of the properties a
	// document must carry, non-null.
	Required map[reflect.Type][]string
	// Types replaces a type's schema outright: for a type with its own
	// UnmarshalJSON, whose wire shape is not its fields. The func may call
	// [Generator.Ref] to reference other types.
	Types map[reflect.Type]func(*Generator) (*Schema, error)
	// Fields replaces one field's schema: for a json.RawMessage the decoder
	// dispatches on (a tagged union), or a value decoded elsewhere.
	Fields map[FieldKey]func(*Generator) (*Schema, error)
	// Describe returns the doc comment of a type (field == "") or of one of
	// its fields (field is the Go field name), "" when there is none.
	Describe func(t reflect.Type, field string) string
}

// Generator builds one schema. Use [Generate]; a Generator is handed to the
// funcs in [Options] so they can reference types.
type Generator struct {
	opts   Options
	defs   map[string]*Schema
	names  map[reflect.Type]string
	taken  map[string]reflect.Type
	active map[reflect.Type]bool
}

var (
	rawMessageType = reflect.TypeOf(json.RawMessage(nil))
	timeType       = reflect.TypeOf(time.Time{})
	unmarshalerTyp = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
)

// Generate derives the schema of root.
func Generate(root reflect.Type, opts Options) (*Schema, error) {
	g := &Generator{opts: opts, defs: map[string]*Schema{}, names: map[reflect.Type]string{}, taken: map[string]reflect.Type{}, active: map[reflect.Type]bool{}}
	for root.Kind() == reflect.Pointer {
		root = root.Elem()
	}
	if root.Kind() != reflect.Struct {
		return nil, fmt.Errorf("schemagen: root %v is not a struct", root)
	}
	s, err := g.structSchema(root)
	if err != nil {
		return nil, err
	}
	s.Dialect = Draft
	s.ID = opts.ID
	s.Title = opts.Title
	if opts.Description != "" {
		s.Description = opts.Description
	}
	if len(g.defs) > 0 {
		s.Defs = g.defs
	}
	return s, nil
}

// Ref returns a `$ref` to t's definition, generating it on first use.
func (g *Generator) Ref(t reflect.Type) (*Schema, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return g.typeSchema(t)
}

// defName is the `$defs` key of a named type: its name, qualified by its
// package's when two packages declare the same name.
func (g *Generator) defName(t reflect.Type) string {
	if n, ok := g.names[t]; ok {
		return n
	}
	name := t.Name()
	if other, ok := g.taken[name]; ok && other != t {
		pkg := t.PkgPath()
		pkg = pkg[strings.LastIndex(pkg, "/")+1:]
		name = pkg + "." + name
	}
	g.names[t] = name
	g.taken[name] = t
	return name
}

func (g *Generator) typeSchema(t reflect.Type) (*Schema, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if f, ok := g.opts.Types[t]; ok {
		if t.Name() == "" {
			return f(g)
		}
		return g.def(t, func() (*Schema, error) { return f(g) })
	}
	switch {
	case t == rawMessageType:
		return &Schema{}, nil
	case t == timeType:
		return &Schema{Types: []string{"string"}, Format: "date-time"}, nil
	}
	if t.Kind() != reflect.Interface && (t.Implements(unmarshalerTyp) || reflect.PointerTo(t).Implements(unmarshalerTyp)) {
		// A string-kinded type with its own decoder is a closed string set
		// (an enum that validates its value): its wire form is a string.
		if t.Kind() == reflect.String {
			return &Schema{Types: []string{"string"}}, nil
		}
		return nil, fmt.Errorf("schemagen: %v has its own UnmarshalJSON, so its fields are not its wire shape: declare it in Options.Types", t)
	}
	switch t.Kind() {
	case reflect.Bool:
		return &Schema{Types: []string{"boolean"}}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return &Schema{Types: []string{"integer"}}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		zero := 0.0
		return &Schema{Types: []string{"integer"}, Minimum: &zero}, nil
	case reflect.Float32, reflect.Float64:
		return &Schema{Types: []string{"number"}}, nil
	case reflect.String:
		return &Schema{Types: []string{"string"}}, nil
	case reflect.Interface:
		return &Schema{}, nil
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return &Schema{Types: []string{"string"}, Description: "base64"}, nil
		}
		items, err := g.typeSchema(t.Elem())
		if err != nil {
			return nil, err
		}
		return &Schema{Types: []string{"array"}, Items: items}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("schemagen: map key %v is not a string", t.Key())
		}
		vals, err := g.typeSchema(t.Elem())
		if err != nil {
			return nil, err
		}
		return &Schema{Types: []string{"object"}, AdditionalProperties: vals}, nil
	case reflect.Struct:
		if t.Name() == "" {
			return g.structSchema(t)
		}
		return g.def(t, func() (*Schema, error) { return g.structSchema(t) })
	}
	return nil, fmt.Errorf("schemagen: %v (%s) has no JSON Schema", t, t.Kind())
}

// def registers t's definition once and returns a reference to it. A type
// reached again while its definition is being built (a recursive type) gets
// the reference too: $ref resolves lazily.
func (g *Generator) def(t reflect.Type, build func() (*Schema, error)) (*Schema, error) {
	name := g.defName(t)
	ref := &Schema{Ref: "#/$defs/" + escapePointer(name)}
	if _, done := g.defs[name]; done || g.active[t] {
		return ref, nil
	}
	g.active[t] = true
	s, err := build()
	delete(g.active, t)
	if err != nil {
		return nil, err
	}
	if s.Description == "" && g.opts.Describe != nil {
		s.Description = g.opts.Describe(t, "")
	}
	g.defs[name] = s
	return ref, nil
}

// jsonField is one property a struct contributes, after encoding/json's
// embedding rules.
type jsonField struct {
	name  string
	field reflect.StructField
	owner reflect.Type
	depth int
}

// jsonFields lists t's properties the way encoding/json resolves them: an
// embedded struct without a tag name is flattened, the shallower of two
// same-named fields wins, and two at the same depth are an error here (the
// decoder would silently drop both).
func jsonFields(t reflect.Type) ([]jsonField, error) {
	var out []jsonField
	index := map[string]int{}
	var walk func(t reflect.Type, depth int) error
	walk = func(t reflect.Type, depth int) error {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if f.Anonymous && name == "" && ft.Kind() == reflect.Struct {
				if err := walk(ft, depth+1); err != nil {
					return err
				}
				continue
			}
			if !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			jf := jsonField{name: name, field: f, owner: t, depth: depth}
			if at, ok := index[name]; ok {
				switch prev := out[at]; {
				case prev.depth < depth:
					continue
				case prev.depth > depth:
					out[at] = jf
					continue
				default:
					return fmt.Errorf("schemagen: %v declares JSON property %q twice at one depth", t, name)
				}
			}
			index[name] = len(out)
			out = append(out, jf)
		}
		return nil
	}
	if err := walk(t, 0); err != nil {
		return nil, err
	}
	return out, nil
}

func (g *Generator) structSchema(t reflect.Type) (*Schema, error) {
	fields, err := jsonFields(t)
	if err != nil {
		return nil, err
	}
	required := map[string]bool{}
	for _, r := range g.opts.Required[t] {
		required[r] = true
	}
	s := &Schema{Types: []string{"object"}, NoAdditional: true}
	for _, f := range fields {
		var fs *Schema
		if build, ok := g.opts.Fields[FieldKey{Type: f.owner, Name: f.name}]; ok {
			fs, err = build(g)
		} else {
			fs, err = g.typeSchema(f.field.Type)
		}
		if err != nil {
			return nil, fmt.Errorf("%v.%s: %w", t, f.field.Name, err)
		}
		if g.opts.Describe != nil {
			if d := g.opts.Describe(f.owner, f.field.Name); d != "" {
				fs = withDescription(fs, d)
			}
		}
		if required[f.name] {
			s.Required = append(s.Required, f.name)
			delete(required, f.name)
		} else {
			fs = nullable(fs)
		}
		s.Properties = append(s.Properties, Property{Name: f.name, Schema: fs})
	}
	for r := range required {
		return nil, fmt.Errorf("schemagen: %v: required property %q is not a field", t, r)
	}
	return s, nil
}

// withDescription returns fs carrying d, without mutating a schema another
// property may share.
func withDescription(fs *Schema, d string) *Schema {
	c := *fs
	if c.Ref != "" && c.Description == "" {
		c.Description = d
		return &c
	}
	c.Description = d
	return &c
}

// nullable lets fs also be null: a type list gains "null"; anything else (a
// reference, a union, the any-schema) becomes anyOf it or null.
func nullable(fs *Schema) *Schema {
	if fs.Ref == "" && len(fs.OneOf) == 0 && len(fs.AnyOf) == 0 && len(fs.Types) > 0 {
		c := *fs
		c.Types = append(append([]string(nil), fs.Types...), "null")
		return &c
	}
	if fs.Ref == "" && len(fs.Types) == 0 && len(fs.OneOf) == 0 && len(fs.AnyOf) == 0 && fs.Const == nil {
		return fs // already accepts anything, null included
	}
	desc := fs.Description
	inner := *fs
	inner.Description = ""
	return &Schema{Description: desc, AnyOf: []*Schema{&inner, {Types: []string{"null"}}}}
}

// escapePointer escapes a $defs key for use in a JSON Pointer.
func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// TaggedUnion is the schema of a value decoded by dispatching on one string
// property: exactly one variant, chosen by the value of tag. Each variant is
// its struct's definition with the tag (required, a const) and the shared
// properties prepended — the decoder reads those off the same object before it
// dispatches, so they are part of every variant's wire shape. A variant type
// therefore belongs to its union: one also referenced plainly is an error.
//
// [Validate] reports an unknown or missing tag by name rather than as a failed
// oneOf.
func TaggedUnion(g *Generator, tag string, variants map[string]reflect.Type, shared ...Property) (*Schema, error) {
	keys := make([]string, 0, len(variants))
	for k := range variants {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	u := &Schema{}
	for _, k := range keys {
		t := variants[k]
		if t == nil {
			// A variant that is its tag alone (plus the shared properties).
			v := &Schema{Types: []string{"object"}, NoAdditional: true, Required: []string{tag},
				Properties: append([]Property{{Name: tag, Schema: &Schema{Const: k}}}, shared...)}
			u.OneOf = append(u.OneOf, v)
			continue
		}
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if _, done := g.defs[g.defName(t)]; done {
			return nil, fmt.Errorf("schemagen: union variant %v is also referenced outside its union", t)
		}
		k := k
		ref, err := g.def(t, func() (*Schema, error) {
			var s *Schema
			var err error
			if f, ok := g.opts.Types[t]; ok {
				s, err = f(g)
			} else {
				s, err = g.structSchema(t)
			}
			if err != nil {
				return nil, err
			}
			head := []Property{{Name: tag, Schema: &Schema{Const: k}}}
			for _, p := range shared {
				if !hasProperty(s, p.Name) {
					head = append(head, p)
				}
			}
			s.Properties = append(head, s.Properties...)
			s.Required = append([]string{tag}, s.Required...)
			return s, nil
		})
		if err != nil {
			return nil, err
		}
		u.OneOf = append(u.OneOf, ref)
	}
	return u, nil
}

// Struct returns t's object schema inline, from its fields, ignoring any
// UnmarshalJSON: the starting point of an [Options.Types] func for a type
// whose wire shape is its fields with one or two of them widened.
func (g *Generator) Struct(t reflect.Type) (*Schema, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return g.structSchema(t)
}

// SetProperty replaces the schema of an existing property, keeping its
// description. It reports whether the property exists.
func (s *Schema) SetProperty(name string, ps *Schema) bool {
	for i, p := range s.Properties {
		if p.Name == name {
			if ps.Description == "" {
				c := *ps
				c.Description = p.Schema.Description
				ps = &c
			}
			s.Properties[i].Schema = ps
			return true
		}
	}
	return false
}

// Nullable returns s also accepting null, the way an optional property is.
func Nullable(s *Schema) *Schema { return nullable(s) }

func hasProperty(s *Schema, name string) bool {
	for _, p := range s.Properties {
		if p.Name == name {
			return true
		}
	}
	return false
}
