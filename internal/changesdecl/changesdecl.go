// Package changesdecl reads changes.yaml, the project-level declaration of
// the Forge Changes a project can serve: each change's key, value type,
// variants and default variant. The release declares what a change CAN be;
// the control plane decides who gets which variant.
//
// The file is optional. A project without one declares no change: nothing is
// generated for it and its release shape carries no changes key.
//
//	version: 1
//	changes:
//	  new-checkout:
//	    description: Shorter checkout flow
//	    type: bool            # bool | string | number | json
//	    default: control      # a variant name
//	    variants:
//	      control: false
//	      treatment: true
//
// Two consumers read the parse: the env shape (every env's release carries
// the declaration, [Decl.Shape]) and `forge generate` (typed Go and
// TypeScript accessors, named by [GoName] and [TSName]). The value rules
// are pkg/release's, so the declaration forge accepts here is exactly the
// one the shape validates.
//
//forge:exclude-contract: a parsed declaration (data types and pure functions); there is no behaviour to substitute
package changesdecl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/pkg/release"
)

// FileName is the declaration's path, relative to the project root.
const FileName = "changes.yaml"

// Version is the only schema version this package reads.
const Version = 1

// Decl is a parsed, validated changes.yaml.
type Decl struct {
	// Changes are in file order. Generated code keeps that order, so two
	// branches that add changes at different places in changes.yaml add
	// them at different places in every generated file too, and merge
	// there exactly as cleanly as they merge in the declaration.
	Changes []Change
}

// Change is one declared change.
type Change struct {
	Key         string
	Description string
	// Type is one of the release.ChangeType* values.
	Type string
	// Default names the default variant; it is one of Variants.
	Default string
	// Variants are in file order. Each value is compacted JSON of Type.
	Variants []Variant
	// Line is where the change's key is in the file.
	Line int
}

// Variant is one named value of a change.
type Variant struct {
	Name  string
	Value json.RawMessage
}

// DefaultValue is the default variant's value.
func (c Change) DefaultValue() json.RawMessage {
	for _, v := range c.Variants {
		if v.Name == c.Default {
			return v.Value
		}
	}
	return nil
}

// Load reads and validates projectDir/changes.yaml. A project without the
// file declares no change: Load returns (nil, nil).
func Load(projectDir string) (*Decl, error) {
	data, err := os.ReadFile(filepath.Join(projectDir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", FileName, err)
	}
	return Parse(FileName, data)
}

// Parse validates data as a changes.yaml named name (for messages). Every
// problem is reported, each as "<name>:<line>: <what>".
func Parse(name string, data []byte) (*Decl, error) {
	p := &parser{name: name}
	d := p.parse(data)
	if len(p.errs) > 0 {
		return nil, fmt.Errorf("%s is invalid:\n  %s", name, strings.Join(p.errs, "\n  "))
	}
	return d, nil
}

// Shape is the declaration in release-shape vocabulary, keyed by change key,
// or nil when d declares no change.
func (d *Decl) Shape() map[string]release.ShapeChange {
	if d == nil || len(d.Changes) == 0 {
		return nil
	}
	out := make(map[string]release.ShapeChange, len(d.Changes))
	for _, c := range d.Changes {
		vs := make(map[string]json.RawMessage, len(c.Variants))
		for _, v := range c.Variants {
			vs[v.Name] = v.Value
		}
		out[c.Key] = release.ShapeChange{Type: c.Type, Default: c.Default, Variants: vs, Description: c.Description}
	}
	return out
}

// GoName is the exported Go identifier of key's accessor: its words, split
// on '-', '_' and '.', in PascalCase with Go initialisms ("new-checkout" →
// NewCheckout, "api.url" → APIURL). Its key constant is "Key" + GoName.
func GoName(key string) string {
	return naming.ToPascalCase(strings.NewReplacer(".", "-").Replace(key))
}

// TSName is the TypeScript identifier of key's accessor: its words in
// camelCase ("new-checkout" → newCheckout). A key whose name would be a
// reserved word gets the suffix "Change" ("delete" → deleteChange). Its key
// constant is TSName + "Key".
func TSName(key string) string {
	words := strings.FieldsFunc(key, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	var b strings.Builder
	for i, w := range words {
		if i == 0 {
			b.WriteString(w)
			continue
		}
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	name := b.String()
	if tsReserved[name] {
		name += "Change"
	}
	return name
}

// tsReserved are the words a TypeScript module cannot declare as a function
// name: ECMAScript's reserved words, strict-mode reserved words, and
// TypeScript's own.
var tsReserved = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`await break case catch class const continue debugger default delete do else
		enum export extends false finally for function if implements import in instanceof interface let new
		null package private protected public return static super switch this throw true try typeof var void
		while with yield arguments eval`) {
		m[w] = true
	}
	return m
}()

type parser struct {
	name string
	errs []string
}

func (p *parser) errorf(line int, format string, args ...any) {
	p.errs = append(p.errs, fmt.Sprintf("%s:%d: %s", p.name, line, fmt.Sprintf(format, args...)))
}

// emptyDocument reports a document with no content, as a bare trailing `---`
// decodes.
func emptyDocument(n *yaml.Node) bool {
	if len(n.Content) == 0 {
		return true
	}
	c := n.Content[0]
	return len(n.Content) == 1 && c.Kind == yaml.ScalarNode && c.Tag == "!!null" && c.Value == ""
}

func (p *parser) parse(data []byte) *Decl {
	// One document only: yaml.Unmarshal reads the first of a stream and
	// drops the rest, so a change declared after a `---` would pass
	// generate and never ship.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		p.errs = append(p.errs, fmt.Sprintf("%s: %v", p.name, err))
		return nil
	}
	for {
		var extra yaml.Node
		err := dec.Decode(&extra)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			p.errs = append(p.errs, fmt.Sprintf("%s: %v", p.name, err))
			return nil
		}
		if emptyDocument(&extra) {
			continue // a trailing `---` with nothing after it
		}
		p.errorf(extra.Line, "a second YAML document: %s holds exactly one (remove the `---` and merge it into the first)", FileName)
		return nil
	}
	if len(doc.Content) == 0 {
		p.errorf(1, "the file is empty (declare `version: %d` and `changes:`, or delete the file)", Version)
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		p.errorf(root.Line, "expected a mapping with `version` and `changes`")
		return nil
	}
	d := &Decl{}
	var haveVersion bool
	p.fields(root, "the file", func(key, value *yaml.Node) {
		switch key.Value {
		case "version":
			haveVersion = true
			var v int
			if value.Kind != yaml.ScalarNode || value.Decode(&v) != nil || v != Version {
				p.errorf(value.Line, "version %q is not supported (this forge reads version %d)", value.Value, Version)
			}
		case "changes":
			d.Changes = p.changes(value)
		default:
			p.errorf(key.Line, "unknown field %q (expected version, changes)", key.Value)
		}
	})
	if !haveVersion {
		p.errorf(root.Line, "version is missing (declare `version: %d`)", Version)
	}
	return d
}

// fields walks a mapping's key/value pairs, refusing a repeated key.
func (p *parser) fields(m *yaml.Node, what string, each func(key, value *yaml.Node)) {
	seen := map[string]bool{}
	for i := 0; i+1 < len(m.Content); i += 2 {
		key, value := m.Content[i], m.Content[i+1]
		if seen[key.Value] {
			p.errorf(key.Line, "%s declares %q twice", what, key.Value)
			continue
		}
		seen[key.Value] = true
		each(key, value)
	}
}

func (p *parser) changes(n *yaml.Node) []Change {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return nil // `changes:` with nothing under it declares none
	}
	if n.Kind != yaml.MappingNode {
		p.errorf(n.Line, "changes must be a mapping of change key to its declaration")
		return nil
	}
	if len(n.Content)/2 > release.MaxShapeChanges {
		p.errorf(n.Line, "declares %d changes (at most %d)", len(n.Content)/2, release.MaxShapeChanges)
	}
	var out []Change
	p.fields(n, "changes", func(key, value *yaml.Node) {
		if c, ok := p.change(key, value); ok {
			out = append(out, c)
		}
	})
	p.checkNames(out)
	return out
}

// change parses one change. ok is false when anything about it was
// reported, so a broken change never reaches the name-collision check.
func (p *parser) change(keyNode, n *yaml.Node) (c Change, ok bool) {
	before := len(p.errs)
	c = Change{Key: keyNode.Value, Line: keyNode.Line}
	if !release.ValidChangeKey(c.Key) {
		p.errorf(keyNode.Line, "change key %q is invalid: a lowercase letter, then up to 63 of a-z 0-9 _ . -", c.Key)
	}
	if n.Kind != yaml.MappingNode {
		p.errorf(n.Line, "change %q must be a mapping with type, default and variants", c.Key)
		return c, false
	}
	var variants *yaml.Node
	var typeLine, defaultLine int
	p.fields(n, fmt.Sprintf("change %q", c.Key), func(key, value *yaml.Node) {
		switch key.Value {
		case "description":
			c.Description = p.str(c.Key, key.Value, value)
			if len(c.Description) > release.MaxChangeDescription {
				p.errorf(value.Line, "change %q: description is %d bytes (at most %d)", c.Key, len(c.Description), release.MaxChangeDescription)
			}
		case "type":
			c.Type, typeLine = p.str(c.Key, key.Value, value), value.Line
		case "default":
			c.Default, defaultLine = p.str(c.Key, key.Value, value), value.Line
		case "variants":
			variants = value
		default:
			p.errorf(key.Line, "change %q: unknown field %q (expected description, type, default, variants)", c.Key, key.Value)
		}
	})
	switch {
	case c.Type == "":
		p.errorf(n.Line, "change %q: type is missing (bool, string, number or json)", c.Key)
		return c, false
	case !release.ValidChangeType(c.Type):
		p.errorf(typeLine, "change %q: type %q is not bool, string, number or json", c.Key, c.Type)
		return c, false
	}
	if variants == nil {
		p.errorf(n.Line, "change %q: variants are missing (a mapping of variant name to value)", c.Key)
		return c, false
	}
	c.Variants = p.variants(c, variants)
	switch {
	case c.Default == "":
		p.errorf(n.Line, "change %q: default is missing (the variant that applies when the change is off)", c.Key)
	case variants.Kind == yaml.MappingNode && !declares(variants, c.Default):
		p.errorf(defaultLine, "change %q: default %q is not one of its variants (%s)", c.Key, c.Default, mappingKeys(variants))
	}
	return c, len(p.errs) == before
}

// str reads a scalar field as written: `default: true` names the variant
// "true", it does not declare a bool.
func (p *parser) str(key, field string, n *yaml.Node) string {
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		p.errorf(n.Line, "change %q: %s must be a string", key, field)
		return ""
	}
	return n.Value
}

func (p *parser) variants(c Change, n *yaml.Node) []Variant {
	if n.Kind != yaml.MappingNode || len(n.Content) == 0 {
		p.errorf(n.Line, "change %q: variants must be a non-empty mapping of variant name to value", c.Key)
		return nil
	}
	if len(n.Content)/2 > release.MaxChangeVariants {
		p.errorf(n.Line, "change %q declares %d variants (at most %d)", c.Key, len(n.Content)/2, release.MaxChangeVariants)
	}
	var out []Variant
	p.fields(n, fmt.Sprintf("change %q variants", c.Key), func(key, value *yaml.Node) {
		if !release.ValidChangeVariant(key.Value) {
			p.errorf(key.Line, "change %q: variant name %q is invalid: a lowercase letter, then up to 31 of a-z 0-9 _ -", c.Key, key.Value)
			return
		}
		var v any
		if err := value.Decode(&v); err != nil {
			p.errorf(value.Line, "change %q: variant %q: %v", c.Key, key.Value, err)
			return
		}
		raw, err := json.Marshal(v)
		if err != nil {
			p.errorf(value.Line, "change %q: variant %q is not a JSON value (%v)", c.Key, key.Value, err)
			return
		}
		if err := release.CheckChangeValue(c.Type, raw); err != nil {
			p.errorf(value.Line, "change %q: variant %q %v (the change's type is %s)", c.Key, key.Value, err, c.Type)
			return
		}
		out = append(out, Variant{Name: key.Value, Value: raw})
	})
	return out
}

// checkNames refuses two keys whose generated identifiers collide
// ("new-checkout" and "new_checkout" are both NewCheckout), in either
// language: generated code that does not compile is worse than a refusal.
func (p *parser) checkNames(cs []Change) {
	goNames, tsNames := map[string]string{}, map[string]string{}
	claim := func(names map[string]string, name, key string, line int, lang string) {
		if other, taken := names[name]; taken && other != key {
			p.errorf(line, "change %q: its %s name %s is also %q's; rename one of the keys", key, lang, name, other)
			return
		}
		names[name] = key
	}
	for _, c := range cs {
		if !release.ValidChangeKey(c.Key) {
			continue
		}
		claim(goNames, GoName(c.Key), c.Key, c.Line, "Go")
		claim(goNames, "Key"+GoName(c.Key), c.Key, c.Line, "Go")
		claim(tsNames, TSName(c.Key), c.Key, c.Line, "TypeScript")
		claim(tsNames, TSName(c.Key)+"Key", c.Key, c.Line, "TypeScript")
	}
}

// declares reports whether mapping m has the key name.
func declares(m *yaml.Node, name string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			return true
		}
	}
	return false
}

// mappingKeys lists m's keys, sorted, for a message.
func mappingKeys(m *yaml.Node) string {
	var names []string
	for i := 0; i+1 < len(m.Content); i += 2 {
		names = append(names, m.Content[i].Value)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}
