// Copyright (c) 2025 Reliant Labs
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// A provider's projection specs live in one directory:
//
//	provider.yaml          the pinned upstream schema, the caps, the envelope
//	objects/<name>.yaml    a projected object several events share
//	<trigger id>.yaml      one event: its catalog prose, attributes, data
//
// Every file is decoded strictly: an unknown key is an error.

type providerSpec struct {
	Provider string `yaml:"provider"`
	// Package is the Go package the generated projections join.
	Package string `yaml:"package"`
	// Catalog is the integration's catalog directory, relative to the repo root.
	Catalog string `yaml:"catalog"`
	// Hooks is the hand-written Go file (in the parser's package directory)
	// whose functions the specs may name.
	Hooks    string       `yaml:"hooks"`
	Upstream upstreamSpec `yaml:"upstream"`
	Caps     capsSpec     `yaml:"caps"`
	Envelope envelopeSpec `yaml:"envelope"`
	// AttributeGroups and DataGroups are lists several events share.
	AttributeGroups map[string][]attrSpec `yaml:"attribute_groups"`
	DataGroups      map[string]fieldList  `yaml:"data_groups"`
}

type upstreamSpec struct {
	Package string `yaml:"package"`
	Version string `yaml:"version"`
	// File is the vendored copy of the upstream schema, relative to the repo root.
	File   string `yaml:"file"`
	SHA256 string `yaml:"sha256"`
}

type capsSpec struct {
	TextBytes       int    `yaml:"text_bytes"`
	DataBytes       int    `yaml:"data_bytes"`
	ListItems       int    `yaml:"list_items"`
	TruncatedSuffix string `yaml:"truncated_suffix"`
	// Lists bounds the object lists a spec names with `max:`; each becomes a
	// constant, max<Name>.
	Lists map[string]int `yaml:"lists"`
}

// envelopeSpec is what every event of the provider carries.
type envelopeSpec struct {
	// Attributes are declared first on every event. A `parser: true` one is set
	// by the hand-written parser from the delivery's routing envelope (no
	// projection produces it); the others are derived like an event's own.
	Attributes []attrSpec `yaml:"attributes"`
	// DataFirst and Data are projected into every event's data, before and
	// after the event's own fields (the order is the schema's property order).
	DataFirst fieldList `yaml:"data_first"`
	Data      fieldList `yaml:"data"`
}

type objectSpec struct {
	// Absent says what the projection yields when the source is not an object:
	// "null" (the default is an object of zero values).
	Absent string    `yaml:"absent"`
	Fields fieldList `yaml:"fields"`
}

type eventSpec struct {
	ID string `yaml:"id"`
	// Event is the provider's event type: "<X-GitHub-Event>.<action>", or the
	// bare event for one with no action.
	Event       string   `yaml:"event"`
	DisplayName string   `yaml:"display_name"`
	Summary     string   `yaml:"summary"`
	Description string   `yaml:"description"`
	Keywords    []string `yaml:"keywords"`
	// Omit names envelope attributes and data fields this event does not carry
	// (push has no action).
	Omit       []string   `yaml:"omit"`
	Attributes []attrSpec `yaml:"attributes"`
	// DataGroup names a provider data group, projected before Data.
	DataGroup string      `yaml:"data_group"`
	Data      fieldList   `yaml:"data"`
	Shrink    *shrinkSpec `yaml:"shrink"`
}

// shrinkSpec says how an oversized payload is cut down to the data cap: the
// named list loses the `drop` keys of each element, then elements from the
// end, until it fits; `flag` is then set true.
type shrinkSpec struct {
	List        string   `yaml:"list"`
	Drop        []string `yaml:"drop"`
	Flag        string   `yaml:"flag"`
	Description string   `yaml:"description"`
}

// attrSpec is an attribute declaration plus how the parser derives its value.
type attrSpec struct {
	// Group stands for the provider's attribute group of that name.
	Group string `yaml:"group"`
	// Use stands for a shared declaration, catalog schemas/attr_<use>.yaml;
	// the name comes from that file.
	Use         string `yaml:"use"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Example     string `yaml:"example"`
	Type        string `yaml:"type"`

	// From is the payload path the value is read from (default: Name), or
	// several, tried in order until one is non-empty.
	From stringList `yaml:"from"`
	// Kind is string (default) or boolean.
	Kind string `yaml:"kind"`
	// Hook names a hand-written func(map[string]any) bool that is given the
	// object at From; the attribute is its result, "true" or "false".
	Hook        string `yaml:"hook"`
	Lower       bool   `yaml:"lower"`
	StripPrefix string `yaml:"strip_prefix"`
	// Parser marks an envelope attribute the hand-written parser sets.
	Parser bool `yaml:"parser"`
	// Unchecked excuses the attribute from the upstream check, with the reason.
	Unchecked string `yaml:"unchecked"`
}

// fieldSpec is one key of a projected object. Kind is one of
//
//	string         text(value); cap, lower, omit_empty apply
//	integer        a JSON number as int64; omit_empty drops a missing one
//	boolean        value == true
//	present        true when the value is a JSON object (a marker field)
//	string_list    the strings of a list; pluck reads that key of each element
//	integer_list   the numbers under key pluck of each element of a list
//	object         object: <name> (a projected object of objects/), or fields:
//	               (an inline one); omit_empty drops it when the source is not an object
//	list           object: <name>, one per element, at most max (caps.<name>)
type fieldSpec struct {
	Name        string
	Kind        string   `yaml:"kind"`
	From        string   `yaml:"from"`
	Description string   `yaml:"description"`
	Cap         bool     `yaml:"cap"`
	Lower       bool     `yaml:"lower"`
	OmitEmpty   bool     `yaml:"omit_empty"`
	Enum        []string `yaml:"enum"`
	Pluck       string   `yaml:"pluck"`
	Object      string   `yaml:"object"`
	Max         string   `yaml:"max"`
	// Require drops an inline omit_empty object unless its named child is a
	// non-empty string.
	Require string    `yaml:"require"`
	Fields  fieldList `yaml:"fields"`
	// Optional says why upstream may omit the path for some events that use
	// this field. The path must still exist in at least one of them: a typo
	// cannot hide behind it.
	Optional string `yaml:"optional"`
	// Unchecked excuses the field from the upstream check, with the reason.
	Unchecked string `yaml:"unchecked"`

	line int
}

// fieldList is an ordered mapping of field name to field. A bare scalar is a
// kind: `title: string`.
type fieldList []*fieldSpec

func (l *fieldList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: fields are a mapping of name to field", n.Line)
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		f := &fieldSpec{Name: k.Value, line: k.Line}
		if seen[f.Name] {
			return fmt.Errorf("line %d: field %q is declared twice", k.Line, f.Name)
		}
		seen[f.Name] = true
		switch v.Kind {
		case yaml.ScalarNode:
			f.Kind = v.Value
		case yaml.MappingNode:
			if err := decodeNode(v, f); err != nil {
				return fmt.Errorf("field %q: %w", f.Name, err)
			}
		default:
			return fmt.Errorf("line %d: field %q is a kind or a mapping", v.Line, f.Name)
		}
		*l = append(*l, f)
	}
	return nil
}

// stringList is a string or a list of strings.
type stringList []string

func (s *stringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*s = stringList{n.Value}
		return nil
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: expected a string", c.Line)
			}
			*s = append(*s, c.Value)
		}
		return nil
	}
	return fmt.Errorf("line %d: expected a string or a list of strings", n.Line)
}

// decodeNode decodes n into v, refusing unknown keys.
func decodeNode(n *yaml.Node, v any) error {
	b, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	return decodeStrict(b, v)
}

func decodeStrict(b []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	return dec.Decode(v)
}

func loadFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := decodeStrict(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

type specSet struct {
	dir      string // the projection directory
	provider providerSpec
	objects  map[string]*objectSpec
	events   []*eventSpec
	specFile map[*eventSpec]string
}

func loadSpecs(dir string) (*specSet, error) {
	s := &specSet{dir: dir, objects: map[string]*objectSpec{}, specFile: map[*eventSpec]string{}}
	if err := loadFile(filepath.Join(dir, "provider.yaml"), &s.provider); err != nil {
		return nil, err
	}
	objs, err := filepath.Glob(filepath.Join(dir, "objects", "*.yaml"))
	if err != nil {
		return nil, err
	}
	for _, f := range objs {
		o := &objectSpec{}
		if err := loadFile(f, o); err != nil {
			return nil, err
		}
		s.objects[strings.TrimSuffix(filepath.Base(f), ".yaml")] = o
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	for _, f := range files {
		if filepath.Base(f) == "provider.yaml" {
			continue
		}
		e := &eventSpec{}
		if err := loadFile(f, e); err != nil {
			return nil, err
		}
		if want := strings.TrimSuffix(filepath.Base(f), ".yaml"); e.ID != want {
			return nil, fmt.Errorf("%s: the file name must be the trigger id it declares, %q", f, want)
		}
		s.events = append(s.events, e)
		s.specFile[e] = f
	}
	return s, nil
}
