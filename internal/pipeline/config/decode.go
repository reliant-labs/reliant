package config

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FieldError is one problem in a pipeline.yaml, at a YAML path such as
// `release.envs[1].soak`. Path is empty for a problem with the document as a
// whole. Line and Column are 1-based, and 0 when the position is unknown (a
// Config built in code rather than parsed).
type FieldError struct {
	Path   string
	Line   int
	Column int
	Msg    string
}

func (e *FieldError) Error() string {
	switch {
	case e.Path == "" && e.Line > 0:
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	case e.Path == "":
		return e.Msg
	case e.Line > 0:
		return fmt.Sprintf("%s (line %d): %s", e.Path, e.Line, e.Msg)
	default:
		return e.Path + ": " + e.Msg
	}
}

// Errors is every problem Parse or Validate found, in document order.
type Errors []*FieldError

func (es Errors) Error() string {
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = e.Error()
	}
	return strings.Join(lines, "\n")
}

// Duration is a Go duration string in the file (`60m`, `1h30m`).
type Duration time.Duration

// Std returns d as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

type position struct{ line, column int }

// nodeDecoder is implemented by the types with a shorthand or a scalar
// encoding of their own. decode never passes it a null node.
type nodeDecoder interface {
	decodeNode(d *decoder, n *yaml.Node, path string)
}

type decoder struct {
	errs Errors
	pos  map[string]position
}

// Parse decodes and validates a pipeline.yaml. The error, when not nil, is
// an [Errors] listing every problem found.
func Parse(data []byte) (*Config, error) {
	cfg, err := decodeDocument(data)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Load is Parse followed by Resolve: the one call most readers want.
func Load(data []byte) (Resolved, error) {
	cfg, err := Parse(data)
	if err != nil {
		return Resolved{}, err
	}
	return cfg.Resolve()
}

func decodeDocument(data []byte) (*Config, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, Errors{{Path: "version", Msg: fmt.Sprintf("the file is empty: write at least `version: %d` and `gate`", SupportedVersion)}}
		}
		return nil, Errors{{Msg: strings.TrimPrefix(err.Error(), "yaml: ")}}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, Errors{{Line: extra.Line, Msg: "the file holds more than one YAML document; pipeline.yaml is one document"}}
	} else if !errors.Is(err, io.EOF) {
		return nil, Errors{{Msg: strings.TrimPrefix(err.Error(), "yaml: ")}}
	}
	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil, Errors{{Line: root.Line, Column: root.Column, Msg: "the document must be a mapping of keys (version, gate, …)"}}
	}

	// The version decides which schema the rest is read against, so a file
	// for another version reports only that.
	if vn := mappingValue(root, "version"); vn != nil && !isNull(vn) {
		var v int
		switch {
		case vn.Kind != yaml.ScalarNode || vn.Tag != "!!int" || vn.Decode(&v) != nil:
			return nil, Errors{{Path: "version", Line: vn.Line, Column: vn.Column,
				Msg: fmt.Sprintf("must be the integer %d, got %s", SupportedVersion, describe(vn))}}
		case v != SupportedVersion:
			return nil, Errors{{Path: "version", Line: vn.Line, Column: vn.Column,
				Msg: fmt.Sprintf("version %d is not supported: this build of Reliant reads pipeline.yaml version %d", v, SupportedVersion)}}
		}
	}

	d := &decoder{pos: map[string]position{}}
	cfg := &Config{}
	d.decodeStruct(root, "", reflect.ValueOf(cfg).Elem())
	cfg.pos = d.pos
	if len(d.errs) > 0 {
		return nil, d.errs
	}
	return cfg, nil
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func (d *decoder) errorf(n *yaml.Node, path, format string, args ...any) {
	fe := &FieldError{Path: path, Msg: fmt.Sprintf(format, args...)}
	if n != nil {
		fe.Line, fe.Column = n.Line, n.Column
	}
	d.errs = append(d.errs, fe)
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

func (d *decoder) decode(n *yaml.Node, path string, v reflect.Value) {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	d.pos[path] = position{n.Line, n.Column}
	if isNull(n) {
		return
	}
	if v.CanAddr() {
		if nd, ok := v.Addr().Interface().(nodeDecoder); ok {
			nd.decodeNode(d, n, path)
			return
		}
	}
	switch v.Kind() {
	case reflect.Pointer:
		ptr := reflect.New(v.Type().Elem())
		d.decode(n, path, ptr.Elem())
		v.Set(ptr)
	case reflect.Struct:
		d.decodeStruct(n, path, v)
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			d.errorf(n, path, "must be a list, got %s", describe(n))
			return
		}
		s := reflect.MakeSlice(v.Type(), len(n.Content), len(n.Content))
		for i, item := range n.Content {
			d.decode(item, fmt.Sprintf("%s[%d]", path, i), s.Index(i))
		}
		v.Set(s)
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			d.errorf(n, path, "must be a mapping, got %s", describe(n))
			return
		}
		m := reflect.MakeMapWithSize(v.Type(), len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, val := n.Content[i], n.Content[i+1]
			if k.Kind != yaml.ScalarNode {
				d.errorf(k, path, "keys must be strings")
				continue
			}
			elem := reflect.New(v.Type().Elem()).Elem()
			d.decode(val, joinPath(path, k.Value), elem)
			m.SetMapIndex(reflect.ValueOf(k.Value).Convert(v.Type().Key()), elem)
		}
		v.Set(m)
	default:
		d.decodeScalar(n, path, v)
	}
}

func (d *decoder) decodeStruct(n *yaml.Node, path string, v reflect.Value) {
	if n.Kind != yaml.MappingNode {
		d.errorf(n, path, "must be a mapping, got %s", describe(n))
		return
	}
	fields := yamlFields(v.Type())
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, val := n.Content[i], n.Content[i+1]
		idx, ok := fields[k.Value]
		if !ok || k.Kind != yaml.ScalarNode {
			where := "the top level"
			if path != "" {
				where = path
			}
			d.errorf(k, joinPath(path, k.Value), "unknown key %q: %s takes %s", k.Value, where, knownKeys(fields))
			continue
		}
		d.decode(val, joinPath(path, k.Value), v.Field(idx))
	}
}

func (d *decoder) decodeScalar(n *yaml.Node, path string, v reflect.Value) {
	if n.Kind != yaml.ScalarNode {
		d.errorf(n, path, "must be %s, got %s", kindNoun(v.Kind()), describe(n))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(n.Value)
	case reflect.Int:
		var i int
		if n.Tag != "!!int" || n.Decode(&i) != nil {
			d.errorf(n, path, "must be an integer, got %s", describeScalar(n))
			return
		}
		v.SetInt(int64(i))
	case reflect.Bool:
		var b bool
		if n.Tag != "!!bool" || n.Decode(&b) != nil {
			d.errorf(n, path, "must be true or false, got %s", describeScalar(n))
			return
		}
		v.SetBool(b)
	default:
		// Every field type is one of the kinds above; reaching here is a
		// programming error in the schema types, caught by the tests.
		d.errorf(n, path, "cannot decode into %s", v.Type())
	}
}

var fieldCache = map[reflect.Type]map[string]int{}

// yamlFields maps each yaml tag of t to its field index. The schema types are
// fixed, so the cache is filled once per type; Parse may run concurrently, so
// it is filled at init for every type reachable from Config.
func yamlFields(t reflect.Type) map[string]int {
	if f, ok := fieldCache[t]; ok {
		return f
	}
	return buildFields(t)
}

func buildFields(t reflect.Type) map[string]int {
	fields := map[string]int{}
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(sf.Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		fields[name] = i
	}
	return fields
}

func init() {
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map:
			walk(t.Elem())
		case reflect.Struct:
			if _, ok := fieldCache[t]; ok {
				return
			}
			fieldCache[t] = buildFields(t)
			for i := range t.NumField() {
				if t.Field(i).IsExported() {
					walk(t.Field(i).Type)
				}
			}
		}
	}
	walk(reflect.TypeFor[Config]())
}

func knownKeys(fields map[string]int) string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return fields[names[i]] < fields[names[j]] })
	return strings.Join(names, ", ")
}

func kindNoun(k reflect.Kind) string {
	switch k {
	case reflect.Int:
		return "an integer"
	case reflect.Bool:
		return "true or false"
	default:
		return "a string"
	}
}

func describe(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	default:
		return describeScalar(n)
	}
}

func describeScalar(n *yaml.Node) string {
	if n.Kind != yaml.ScalarNode {
		return describe(n)
	}
	return strconv.Quote(n.Value)
}

func (g *Gate) decodeNode(d *decoder, n *yaml.Node, path string) {
	if n.Kind == yaml.ScalarNode {
		g.Run = n.Value
		return
	}
	d.decodeStruct(n, path, reflect.ValueOf(g).Elem())
}

func (c *Check) decodeNode(d *decoder, n *yaml.Node, path string) {
	if n.Kind == yaml.ScalarNode {
		c.Run = n.Value
		return
	}
	d.decodeStruct(n, path, reflect.ValueOf(c).Elem())
}

func (c *SweepCheck) decodeNode(d *decoder, n *yaml.Node, path string) {
	if n.Kind == yaml.ScalarNode {
		c.Name = n.Value
		return
	}
	d.decodeStruct(n, path, reflect.ValueOf(c).Elem())
	if c.Run == "" {
		d.errorf(n, joinPath(path, "run"), "a command check needs run; name a built-in check as a bare string")
	}
}

func (p *Probe) decodeNode(d *decoder, n *yaml.Node, path string) {
	if n.Kind == yaml.ScalarNode {
		p.Name = n.Value
		return
	}
	d.decodeStruct(n, path, reflect.ValueOf(p).Elem())
	if p.Run == "" {
		d.errorf(n, joinPath(path, "run"), "a command probe needs run; name a built-in probe as a bare string")
	}
}

func (dur *Duration) decodeNode(d *decoder, n *yaml.Node, path string) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		d.errorf(n, path, "must be a duration with a unit, such as 60m or 1h30m; got %s", describe(n))
		return
	}
	parsed, err := time.ParseDuration(n.Value)
	if err != nil {
		d.errorf(n, path, "must be a duration with a unit, such as 60m or 1h30m; got %s", describeScalar(n))
		return
	}
	*dur = Duration(parsed)
}
