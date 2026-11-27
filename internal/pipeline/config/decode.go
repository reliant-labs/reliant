package config

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
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
	return LoadFor(data, Project{})
}

// LoadFor is Parse followed by ResolveFor: the stage defaults are p's.
func LoadFor(data []byte, p Project) (Resolved, error) {
	cfg, err := Parse(data)
	if err != nil {
		return Resolved{}, err
	}
	return cfg.ResolveFor(p)
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
	// for another version reports only that. A version written twice is
	// ambiguous about which schema that is, so it too is reported alone.
	var versionKeys []*yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if k := root.Content[i]; k.Kind == yaml.ScalarNode && k.ShortTag() == "!!str" && k.Value == "version" {
			versionKeys = append(versionKeys, k)
		}
	}
	if len(versionKeys) > 1 {
		return nil, Errors{duplicateKey("version", versionKeys[0], versionKeys[1])}
	}
	if vn := mappingValue(root, "version"); vn != nil && !isNull(vn) {
		var v int
		switch {
		case vn.Kind != yaml.ScalarNode || vn.ShortTag() != "!!int" || vn.Decode(&v) != nil:
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

// mappingValue is the value of key in m, or nil. A key written twice is
// reported by the decoder; this reads the first.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i]; k.Kind == yaml.ScalarNode && k.ShortTag() == "!!str" && k.Value == key {
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
	return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null"
}

func isString(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str"
}

// shorthand is implemented by the types written as a string or a mapping.
// Null is neither, so decode reports it rather than reading it as absent.
type shorthand interface {
	nodeDecoder
	// emptyHint completes "is empty: …" for a null value.
	emptyHint() string
}

var shorthandType = reflect.TypeFor[shorthand]()

func (d *decoder) decode(n *yaml.Node, path string, v reflect.Value) {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	d.pos[path] = position{n.Line, n.Column}
	if isNull(n) {
		t := v.Type()
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if reflect.PointerTo(t).Implements(shorthandType) {
			d.errorf(n, path, "is empty: %s", reflect.New(t).Interface().(shorthand).emptyHint())
		}
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
		d.decodeMap(n, path, v, d.decode)
	default:
		d.decodeScalar(n, path, v)
	}
}

// keyValue is one entry of a mapping whose key passed [decoder.pairs].
type keyValue struct{ key, value *yaml.Node }

// pairs returns the entries of the mapping n in document order. It reports
// and drops every key that is not a string (`true:`, `1:`, `~:`, a list, a
// merge key) and every key written a second time, so no mapping in the file
// can coerce a key or silently replace an earlier value with a later one.
func (d *decoder) pairs(n *yaml.Node, path string) []keyValue {
	seen := map[string]*yaml.Node{}
	out := make([]keyValue, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, val := n.Content[i], n.Content[i+1]
		if k.Kind == yaml.AliasNode && k.Alias != nil {
			k = k.Alias
		}
		switch {
		case k.Kind != yaml.ScalarNode:
			d.errorf(n.Content[i], path, "keys must be strings, got %s", describe(k))
			continue
		case k.ShortTag() == "!!merge":
			d.errorf(n.Content[i], joinPath(path, k.Value), "merge keys (<<) are not supported: write the keys out")
			continue
		case k.ShortTag() != "!!str":
			d.errorf(n.Content[i], joinPath(path, k.Value), "keys must be strings, got %s%s", describe(k), quoteHint(k))
			continue
		}
		if first, ok := seen[k.Value]; ok {
			d.errs = append(d.errs, duplicateKey(joinPath(path, k.Value), first, n.Content[i]))
			continue
		}
		seen[k.Value] = n.Content[i]
		out = append(out, keyValue{k, val})
	}
	return out
}

func duplicateKey(path string, first, again *yaml.Node) *FieldError {
	_, key := splitLast(path)
	return &FieldError{Path: path, Line: again.Line, Column: again.Column,
		Msg: fmt.Sprintf("key %q is written twice, on lines %d and %d: keep one", key, first.Line, again.Line)}
}

// splitLast splits a YAML path at its last key.
func splitLast(path string) (parent, key string) {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return path[:i], path[i+1:]
	}
	return "", path
}

// decodeMap decodes a free mapping (playbooks, gate.env) into the map v,
// each value with elem.
func (d *decoder) decodeMap(n *yaml.Node, path string, v reflect.Value, elem func(*yaml.Node, string, reflect.Value)) {
	if n.Kind != yaml.MappingNode {
		d.errorf(n, path, "must be a mapping, got %s", describe(n))
		return
	}
	kvs := d.pairs(n, path)
	m := reflect.MakeMapWithSize(v.Type(), len(kvs))
	for _, kv := range kvs {
		e := reflect.New(v.Type().Elem()).Elem()
		elem(kv.value, joinPath(path, kv.key.Value), e)
		m.SetMapIndex(reflect.ValueOf(kv.key.Value).Convert(v.Type().Key()), e)
	}
	v.Set(m)
}

func (d *decoder) decodeStruct(n *yaml.Node, path string, v reflect.Value) {
	if n.Kind != yaml.MappingNode {
		d.errorf(n, path, "must be a mapping, got %s", describe(n))
		return
	}
	fields := yamlFields(v.Type())
	for _, kv := range d.pairs(n, path) {
		idx, ok := fields[kv.key.Value]
		if !ok {
			where := "the top level"
			if path != "" {
				where = path
			}
			d.errorf(kv.key, joinPath(path, kv.key.Value), "unknown key %q: %s takes %s", kv.key.Value, where, knownKeys(fields))
			continue
		}
		d.decode(kv.value, joinPath(path, kv.key.Value), v.Field(idx))
	}
}

func (d *decoder) decodeScalar(n *yaml.Node, path string, v reflect.Value) {
	if n.Kind != yaml.ScalarNode {
		d.errorf(n, path, "must be %s, got %s", kindNoun(v.Kind()), describe(n))
		return
	}
	switch v.Kind() {
	case reflect.String:
		// A string field takes only a string: `run: 42` or `pool: true` is
		// an error, never the text "42" or "true".
		if !isString(n) {
			d.errorf(n, path, "must be a string, got %s%s", describe(n), quoteHint(n))
			return
		}
		v.SetString(n.Value)
	case reflect.Int:
		var i int
		if n.ShortTag() != "!!int" || n.Decode(&i) != nil {
			d.errorf(n, path, "must be an integer, got %s", describe(n))
			return
		}
		v.SetInt(int64(i))
	case reflect.Bool:
		var b bool
		if n.ShortTag() != "!!bool" || n.Decode(&b) != nil {
			d.errorf(n, path, "must be true or false, got %s", describe(n))
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
	}
	switch n.ShortTag() {
	case "!!str":
		return strconv.Quote(n.Value)
	case "!!int":
		return "the integer " + n.Value
	case "!!float":
		return "the number " + n.Value
	case "!!bool":
		return "the boolean " + n.Value
	case "!!null":
		return "null"
	case "!!timestamp":
		return "the timestamp " + n.Value
	default:
		return fmt.Sprintf("a value tagged %s", n.ShortTag())
	}
}

// quoteHint suggests quoting a scalar YAML read as a number, boolean or
// timestamp, when the text was meant.
func quoteHint(n *yaml.Node) string {
	switch n.ShortTag() {
	case "!!int", "!!float", "!!bool", "!!timestamp":
		return fmt.Sprintf("; quote it (%q) to write the text", n.Value)
	}
	return ""
}

// shorthandString reads the string form of a string-or-mapping field. Only a
// YAML string is that form: `gate: 42` and `gate: true` are errors, never
// the commands "42" and "true".
func (d *decoder) shorthandString(n *yaml.Node, path, what string) (string, bool) {
	if !isString(n) {
		d.errorf(n, path, "must be %s or a mapping, got %s%s", what, describe(n), quoteHint(n))
		return "", false
	}
	return n.Value, true
}

func (g *Gate) emptyHint() string {
	return "name the full suite main must always pass, e.g. `gate: \"npm test\"`"
}

func (g *Gate) decodeNode(d *decoder, n *yaml.Node, path string) {
	if n.Kind == yaml.ScalarNode {
		g.Run, _ = d.shorthandString(n, path, "a command")
		return
	}
	d.decodeStruct(n, path, reflect.ValueOf(g).Elem())
}

// stageOff is the one string a gate stage takes.
const stageOff = "off"

func (s *Stage) emptyHint() string {
	return "write {run: <command>} to replace the stage's command, or off to turn it off; remove the key for its default"
}

// decodeNode reads a gate stage: the mapping {run} or the exact string off.
// Any other scalar is an error, a command string included: a command goes in
// run, so a stage named by a bare string is always off and never a command
// that happens to read "off".
func (s *Stage) decodeNode(d *decoder, n *yaml.Node, path string) {
	if n.Kind != yaml.ScalarNode {
		d.decodeStruct(n, path, reflect.ValueOf(s).Elem())
		return
	}
	switch {
	case isString(n) && n.Value == stageOff:
		s.Off = true
	case isString(n) && strings.EqualFold(n.Value, stageOff):
		d.errorf(n, path, "must be off (lowercase) or {run: <command>}, got %s", describe(n))
	case isString(n):
		d.errorf(n, path, "must be off or {run: <command>}, got %s: a command goes in run, e.g. `%s: {run: %s}`", describe(n), lastKey(path), strconv.Quote(n.Value))
	case n.ShortTag() == "!!bool":
		d.errorf(n, path, "must be off or {run: <command>}, got %s: write off to turn the stage off", describe(n))
	default:
		d.errorf(n, path, "must be off or {run: <command>}, got %s", describe(n))
	}
}

func lastKey(path string) string {
	_, key := splitLast(path)
	return key
}

func (c *Check) emptyHint() string {
	return "write a command or {run, checks}, or remove the key for no check tier"
}

func (c *Check) decodeNode(d *decoder, n *yaml.Node, path string) {
	if n.Kind == yaml.ScalarNode {
		c.Run, _ = d.shorthandString(n, path, "a command")
		return
	}
	d.decodeStruct(n, path, reflect.ValueOf(c).Elem())
}

func (c *SweepCheck) emptyHint() string {
	return "write a built-in check name or {name, run}"
}

func (c *SweepCheck) decodeNode(d *decoder, n *yaml.Node, path string) {
	if n.Kind == yaml.ScalarNode {
		c.Name, _ = d.shorthandString(n, path, "a built-in check name")
		return
	}
	namedCommand(d, n, path, reflect.ValueOf(c).Elem(), &c.Name, &c.Run, "check", SweepBuiltins)
}

func (p *Probe) emptyHint() string {
	return "write a built-in probe name or {name, run}"
}

func (p *Probe) decodeNode(d *decoder, n *yaml.Node, path string) {
	if n.Kind == yaml.ScalarNode {
		p.Name, _ = d.shorthandString(n, path, "a built-in probe name")
		return
	}
	namedCommand(d, n, path, reflect.ValueOf(p).Elem(), &p.Name, &p.Run, "probe", ProbeBuiltins)
}

// namedCommand decodes the mapping form of a sweep check or a probe. Without
// run, `{name: <built-in>}` is the built-in, the same as the bare name; with
// run it is a repo command, whose name the validator keeps off the built-in
// names. The JSON schema states the same rule.
func namedCommand[B ~string](d *decoder, n *yaml.Node, path string, v reflect.Value, name, run *string, what string, builtins []B) {
	before := len(d.errs)
	d.decodeStruct(n, path, v)
	if len(d.errs) > before {
		return
	}
	_, hasRun := d.pos[joinPath(path, "run")]
	switch {
	case hasRun && *run == "":
		d.errorf(n, joinPath(path, "run"), "is empty: write the command, or remove run to name a built-in %s", what)
	case hasRun:
		// A repo command: the validator checks its name and command.
	case *name == "":
		d.errorf(n, joinPath(path, "name"), "is required: a built-in %s (%s), or {name, run} for a command %s", what, list(builtins), what)
	case !slices.Contains(builtins, B(*name)):
		d.errorf(n, joinPath(path, "run"), "a command %s needs run: %q is not a built-in %s (the built-ins are %s)", what, *name, what, list(builtins))
	}
}

func (dur *Duration) decodeNode(d *decoder, n *yaml.Node, path string) {
	if !isString(n) {
		d.errorf(n, path, "must be a duration with a unit, such as 60m or 1h30m; got %s", describe(n))
		return
	}
	parsed, err := time.ParseDuration(n.Value)
	if err != nil {
		d.errorf(n, path, "must be a duration with a unit, such as 60m or 1h30m; got %s", describe(n))
		return
	}
	*dur = Duration(parsed)
}

// decodeNode reads gate.env: a mapping of names to scalar values. A number
// or boolean is kept as written (`PORT: 8080` is "8080"); null, a list or a
// mapping is an error.
func (e *EnvVars) decodeNode(d *decoder, n *yaml.Node, path string) {
	d.decodeMap(n, path, reflect.ValueOf(e).Elem(), func(val *yaml.Node, vpath string, v reflect.Value) {
		if val.Kind == yaml.AliasNode && val.Alias != nil {
			val = val.Alias
		}
		d.pos[vpath] = position{val.Line, val.Column}
		switch {
		case isNull(val):
			d.errorf(val, vpath, "is empty: write \"\" for an empty value, or remove the variable")
		case val.Kind != yaml.ScalarNode:
			d.errorf(val, vpath, "must be a string, a number or true/false, got %s", describe(val))
		default:
			v.SetString(val.Value)
		}
	})
}
