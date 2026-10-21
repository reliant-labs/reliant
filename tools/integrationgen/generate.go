// Copyright (c) 2025 Reliant Labs
package main

import (
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// output is every file a run owns, by path relative to the repo root.
type output struct {
	files map[string][]byte
	// owned is the directories whose generated files (by marker) are stale when
	// no output covers them.
	owned []string
}

// generate runs one provider's projection specs.
func generate(root, provider string) (*output, error) {
	dir := filepath.Join(root, "internal/integrations/webhook", provider, "projection")
	specs, err := loadSpecs(dir)
	if err != nil {
		return nil, err
	}
	if specs.provider.Provider != provider {
		return nil, fmt.Errorf("%s/provider.yaml: provider is %q, want %q", dir, specs.provider.Provider, provider)
	}
	pl, err := newPlan(root, specs)
	if err != nil {
		return nil, err
	}
	if err := pl.check(root); err != nil {
		return nil, err
	}
	g := &emitter{pl: pl, root: root, out: &output{files: map[string][]byte{}}}
	if err := g.run(); err != nil {
		return nil, err
	}
	return g.out, nil
}

type emitter struct {
	pl   *plan
	root string
	out  *output

	funcs     map[string]string // generated Go funcs by name
	sharedAtt map[string]string // shared attribute stem -> attribute name
}

func (g *emitter) run() error {
	p := g.pl.specs.provider
	g.funcs = map[string]string{}
	g.sharedAtt = map[string]string{}
	catalog := p.Catalog

	for name, o := range g.pl.specs.objects {
		g.objectFunc(name+"Object", o.Fields, o.Absent == "null")
		m := ymap{}
		m.set("type", "object")
		m.set("properties", g.properties(o.Fields, ""))
		g.put(filepath.Join(catalog, "schemas", name+"_schema.yaml"), []byte(g.yamlHeader("", "objects/"+name+".yaml")+body(m)))
	}

	var mappings []string
	for _, e := range g.pl.specs.events {
		fn := camel(e.Event)
		fields := g.pl.dataFields(e)
		g.objectFunc(fn+"Data", fields, false)
		attrs, err := g.attrFunc(fn+"Attrs", e)
		if err != nil {
			return err
		}
		entry := fmt.Sprintf("\t%s: {", strconv.Quote(e.Event))
		var parts []string
		if attrs {
			parts = append(parts, "attrs: "+fn+"Attrs")
		}
		parts = append(parts, "data: "+fn+"Data")
		if e.Shrink != nil {
			drop := make([]string, len(e.Shrink.Drop))
			for i, d := range e.Shrink.Drop {
				drop[i] = strconv.Quote(d)
			}
			parts = append(parts, fmt.Sprintf("shrink: &shrinkRule{list: %q, drop: []string{%s}, flag: %q}", e.Shrink.List, strings.Join(drop, ", "), e.Shrink.Flag))
		}
		mappings = append(mappings, entry+strings.Join(parts, ", ")+"},")

		frag, err := g.fragment(e)
		if err != nil {
			return err
		}
		g.put(filepath.Join(catalog, "events", e.ID+".yaml"), frag)
	}
	sort.Strings(mappings)

	src, err := g.goSource(mappings)
	if err != nil {
		return err
	}
	g.put(filepath.Join("internal/integrations/webhook", p.Provider, "zz_projection_gen.go"), src)

	man, err := g.syncManifest()
	if err != nil {
		return err
	}
	g.put(filepath.Join(catalog, "manifest.yaml"), man)
	g.out.owned = []string{filepath.Join(catalog, "events"), filepath.Join(catalog, "schemas")}
	return nil
}

func (g *emitter) put(path string, b []byte) {
	g.out.files[filepath.ToSlash(path)] = b
}

func body(m ymap) string {
	var b strings.Builder
	render(&b, m, 0)
	return b.String()
}

func (g *emitter) yamlHeader(schema, spec string) string {
	p := g.pl.specs.provider
	h := ""
	if schema != "" {
		h = "# yaml-language-server: $schema=" + schema + "\n"
	}
	return h + fmt.Sprintf("# %s from internal/integrations/webhook/%s/projection/%s\n# against %s@%s. DO NOT EDIT: change the spec and run `make generate-integrations`.\n",
		generatedMarker, p.Provider, spec, p.Upstream.Package, p.Upstream.Version)
}

// --- YAML -----------------------------------------------------------------

// schemaOf is the JSON Schema of one field. prefix is the path from the file
// being written to the catalog's schemas/ directory.
func (g *emitter) schemaOf(f *fieldSpec, prefix string) any {
	m := ymap{}
	switch f.Kind {
	case "string":
		m.set("type", "string")
		if len(f.Enum) > 0 {
			enum := make([]any, len(f.Enum))
			for i, e := range f.Enum {
				enum[i] = e
			}
			m.set("enum", enum)
		}
	case "integer":
		m.set("type", "integer")
	case "boolean", "present":
		m.set("type", "boolean")
	case "names", "strings":
		m.set("type", "array")
		m.set("items", ymap{{"type", "string"}})
	case "integers":
		m.set("type", "array")
		m.set("items", ymap{{"type", "integer"}})
	case "object":
		if f.Object != "" {
			return ymap{{"$ref", prefix + f.Object + "_schema.yaml"}}
		}
		m.set("type", "object")
		m.set("properties", g.properties(f.Fields, prefix))
	case "list":
		m.set("type", "array")
		m.set("items", ymap{{"$ref", prefix + f.Object + "_schema.yaml"}})
	}
	if f.Description != "" {
		m.set("description", f.Description)
	}
	return m
}

func (g *emitter) properties(fields fieldList, prefix string) ymap {
	m := ymap{}
	for _, f := range fields {
		m.set(f.Name, g.schemaOf(f, prefix))
	}
	return m
}

func (g *emitter) attrDecl(a attrSpec) (ymap, error) {
	if a.Use != "" {
		return ymap{{"$ref", "../schemas/attr_" + a.Use + ".yaml"}}, nil
	}
	m := ymap{}
	m.set("name", a.Name)
	m.set("description", a.Description)
	m.set("example", a.Example)
	if a.Type != "" {
		m.set("type", a.Type)
	}
	return m, nil
}

func (g *emitter) fragment(e *eventSpec) ([]byte, error) {
	root := ymap{}
	root.set("id", e.ID)
	root.set("display_name", e.DisplayName)
	root.set("summary", e.Summary)
	if e.Description != "" {
		root.set("description", e.Description)
	}
	if len(e.Keywords) > 0 {
		kw := make([]any, len(e.Keywords))
		for i, k := range e.Keywords {
			kw[i] = k
		}
		root.set("keywords", kw)
	}
	root.set("events", []any{e.Event})
	var attrs []any
	for _, a := range g.pl.eventAttrs(e) {
		d, err := g.attrDecl(a)
		if err != nil {
			return nil, err
		}
		attrs = append(attrs, d)
	}
	root.set("attributes", attrs)

	props := ymap{}
	for _, f := range g.pl.dataFields(e) {
		props.set(f.Name, g.schemaOf(f, "../schemas/"))
		if e.Shrink != nil && f.Name == e.Shrink.List {
			props.set(e.Shrink.Flag, ymap{{"type", "boolean"}, {"description", e.Shrink.Description}})
		}
	}
	data := ymap{}
	data.set("type", "object")
	data.set("additionalProperties", false)
	data.set("properties", props)
	root.set("data", data)

	text := g.yamlHeader("../../../manifest/schema/event.schema.json", e.ID+".yaml") + body(root)
	if err := roundTrips(body(root), root); err != nil {
		return nil, fmt.Errorf("%s: %w", e.ID, err)
	}
	return []byte(text), nil
}

// syncManifest lists every generated event in manifest.yaml's triggers, adding
// the ones it does not yet name at the end: the list's order is the order the
// catalog shows them in, and moving one is an ordinary edit.
func (g *emitter) syncManifest() ([]byte, error) {
	path := filepath.Join(g.root, g.pl.specs.provider.Catalog, "manifest.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(b), "\n")
	ref := regexp.MustCompile(`^  - \$ref: events/(.+)\.yaml$`)
	start, last := -1, -1
	listed := map[string]bool{}
	for i, l := range lines {
		if l == "triggers:" {
			start = i
			continue
		}
		if start >= 0 && last < 0 || start >= 0 && i == last+1 {
			if m := ref.FindStringSubmatch(l); m != nil {
				listed[m[1]] = true
				last = i
			}
		}
	}
	if start < 0 || last < 0 {
		return nil, fmt.Errorf("%s: no `triggers:` list of `- $ref: events/<id>.yaml` entries to add the generated events to", path)
	}
	var add []string
	for _, e := range g.pl.specs.events {
		if !listed[e.ID] {
			add = append(add, "  - $ref: events/"+e.ID+".yaml")
		}
	}
	sort.Strings(add)
	out := append(append(append([]string{}, lines[:last+1]...), add...), lines[last+1:]...)
	return []byte(strings.Join(out, "\n")), nil
}

// --- Go -------------------------------------------------------------------

func camel(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for i := range parts {
		if i > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func title(s string) string {
	c := camel(s)
	return strings.ToUpper(c[:1]) + c[1:]
}

func pathLit(p string) string {
	segs := strings.Split(p, ".")
	for i, s := range segs {
		segs[i] = strconv.Quote(s)
	}
	return strings.Join(segs, ", ")
}

// readExpr is the Go expression reading one field's value out of v.
func (g *emitter) readExpr(fn string, f *fieldSpec) string {
	src := fmt.Sprintf("dig(v, %s)", pathLit(f.srcPath()))
	switch f.Kind {
	case "string":
		switch {
		case f.Cap:
			return "capText(text(" + src + "))"
		case f.Lower:
			return "strings.ToLower(text(" + src + "))"
		}
		return "text(" + src + ")"
	case "integer":
		return "num(" + src + ")"
	case "boolean":
		return src + " == true"
	case "present":
		return "isObject(" + src + ")"
	case "names":
		return fmt.Sprintf("names(%s, %q)", src, f.Pluck)
	case "strings":
		return "capList(" + src + ")"
	case "integers":
		return fmt.Sprintf("integers(%s, %q)", src, f.Pluck)
	case "object":
		return g.objectName(fn, f) + "(" + src + ")"
	case "list":
		return fmt.Sprintf("objects(%s, max%s, %sObject)", src, title(f.Max), f.Object)
	}
	panic("unreachable kind " + f.Kind)
}

// objectName is the generated func that projects a field's object, emitting an
// inline object's func on first use.
func (g *emitter) objectName(parent string, f *fieldSpec) string {
	if f.Object != "" {
		return f.Object + "Object"
	}
	name := parent + title(f.Name)
	g.objectFunc(name, f.Fields, false)
	return name
}

func (g *emitter) objectFunc(name string, fields fieldList, absentNull bool) {
	var b strings.Builder
	fmt.Fprintf(&b, "func %s(v any) map[string]any {\n", name)
	if absentNull {
		b.WriteString("\tif !isObject(v) {\n\t\treturn nil\n\t}\n")
	}
	var always, cond []string
	for _, f := range fields {
		key := strconv.Quote(f.Name)
		expr := g.readExpr(name, f)
		if !f.OmitEmpty {
			always = append(always, fmt.Sprintf("\t\t%s: %s,\n", key, expr))
			continue
		}
		switch f.Kind {
		case "string":
			cond = append(cond, fmt.Sprintf("\tif s := %s; s != \"\" {\n\t\tout[%s] = s\n\t}\n", expr, key))
		case "integer":
			cond = append(cond, fmt.Sprintf("\tif n := %s; n != nil {\n\t\tout[%s] = n\n\t}\n", expr, key))
		case "object":
			test := fmt.Sprintf("isObject(dig(v, %s))", pathLit(f.srcPath()))
			if f.Require != "" {
				test = fmt.Sprintf("hasText(dig(v, %s, %s))", pathLit(f.srcPath()), strconv.Quote(f.Require))
			}
			cond = append(cond, fmt.Sprintf("\tif %s {\n\t\tout[%s] = %s\n\t}\n", test, key, expr))
		}
	}
	if len(always) == 0 {
		b.WriteString("\tout := map[string]any{}\n")
	} else {
		b.WriteString("\tout := map[string]any{\n" + strings.Join(always, "") + "\t}\n")
	}
	b.WriteString(strings.Join(cond, ""))
	b.WriteString("\treturn out\n}\n")
	g.funcs[name] = b.String()
}

// attrFunc emits the func that sets an event's projected attributes, and says
// whether there is one.
func (g *emitter) attrFunc(name string, e *eventSpec) (bool, error) {
	var stmts []string
	for _, a := range g.pl.eventAttrs(e) {
		if a.Parser {
			continue
		}
		attr := a.Name
		if a.Use != "" {
			n, err := g.sharedName(a.Use)
			if err != nil {
				return false, err
			}
			attr = n
		}
		key := strconv.Quote(attr)
		reads := make([]string, len(a.From))
		for i, f := range a.From {
			reads[i] = fmt.Sprintf("dig(raw, %s)", pathLit(f))
		}
		var val string
		switch {
		case a.Hook != "":
			val = fmt.Sprintf("strconv.FormatBool(%s(%s))", a.Hook, reads[0])
		case a.Kind == "boolean":
			val = fmt.Sprintf("strconv.FormatBool(%s == true)", reads[0])
		case a.Kind == "present":
			val = fmt.Sprintf("strconv.FormatBool(isObject(%s))", reads[0])
		case a.StripPrefix != "":
			stmts = append(stmts, fmt.Sprintf("\tif rest, ok := strings.CutPrefix(text(%s), %q); ok {\n\t\tattrs[%s] = rest\n\t}\n", reads[0], a.StripPrefix, key))
			continue
		case len(reads) > 1:
			texts := make([]string, len(reads))
			for i, r := range reads {
				texts[i] = "text(" + r + ")"
			}
			val = "firstNonEmpty(" + strings.Join(texts, ", ") + ")"
		default:
			val = "text(" + reads[0] + ")"
			if a.Lower {
				val = "strings.ToLower(" + val + ")"
			}
		}
		stmts = append(stmts, fmt.Sprintf("\tattrs[%s] = %s\n", key, val))
	}
	if len(stmts) == 0 {
		return false, nil
	}
	g.funcs[name] = fmt.Sprintf("func %s(raw any, attrs map[string]string) {\n%s}\n", name, strings.Join(stmts, ""))
	return true, nil
}

func (g *emitter) sharedName(use string) (string, error) {
	if n, ok := g.sharedAtt[use]; ok {
		return n, nil
	}
	m, err := sharedAttr(g.root, g.pl.specs.provider.Catalog, use)
	if err != nil {
		return "", err
	}
	name := ""
	for _, e := range m {
		if e.K == "name" {
			name, _ = e.V.(string)
		}
	}
	g.sharedAtt[use] = name
	return name, nil
}

func (g *emitter) goSource(mappings []string) ([]byte, error) {
	p := g.pl.specs.provider
	var b strings.Builder
	fmt.Fprintf(&b, "// Code generated by tools/integrationgen from projection/*.yaml; DO NOT EDIT.\n")
	fmt.Fprintf(&b, "// Upstream schema: %s@%s (sha256 %s).\n", p.Upstream.Package, p.Upstream.Version, p.Upstream.SHA256)
	fmt.Fprintf(&b, "// Change a spec and run `make generate-integrations`.\n\n")
	fmt.Fprintf(&b, "package %s\n\nimport (\n\t\"strconv\"\n\t\"strings\"\n)\n\n", p.Package)
	b.WriteString("var (\n\t_ = strconv.Itoa\n\t_ = strings.ToLower\n)\n\n")

	c := p.Caps
	b.WriteString("// The caps of the trimmed payload (provider.yaml).\nconst (\n")
	fmt.Fprintf(&b, "\t// maxTextBytes caps one text field.\n\tmaxTextBytes = %d\n", c.TextBytes)
	fmt.Fprintf(&b, "\t// maxDataBytes bounds the encoded payload.\n\tmaxDataBytes = %d\n", c.DataBytes)
	fmt.Fprintf(&b, "\t// maxListItems bounds a list of names.\n\tmaxListItems = %d\n", c.ListItems)
	var lists []string
	for k := range c.Lists {
		lists = append(lists, k)
	}
	sort.Strings(lists)
	for _, k := range lists {
		fmt.Fprintf(&b, "\tmax%s = %d\n", title(k), c.Lists[k])
	}
	fmt.Fprintf(&b, "\n\ttruncatedSuffix = %s\n)\n\n", strconv.Quote(c.TruncatedSuffix))

	b.WriteString("// mappings is every event type the parser emits, keyed \"<event>.<action>\"\n// (or the bare event for push).\nvar mappings = map[string]mapping{\n")
	b.WriteString(strings.Join(mappings, "\n"))
	b.WriteString("\n}\n")

	names := make([]string, 0, len(g.funcs))
	for n := range g.funcs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b.WriteString("\n" + g.funcs[n])
	}
	out, err := format.Source([]byte(b.String()))
	if err != nil {
		return nil, fmt.Errorf("generated Go does not parse: %w\n%s", err, b.String())
	}
	return out, nil
}

// dataFields is an event's data fields in schema order: the envelope's leading
// fields, the event's data group, its own fields, the envelope's trailing ones.
func (pl *plan) dataFields(e *eventSpec) fieldList {
	env := pl.specs.provider.Envelope
	var out fieldList
	out = append(out, pl.envelopeSplit(e, env.DataFirst)...)
	if e.DataGroup != "" {
		out = append(out, pl.specs.provider.DataGroups[e.DataGroup]...)
	}
	out = append(out, e.Data...)
	out = append(out, pl.envelopeSplit(e, env.Data)...)
	return out
}

// sharedAttr reads a shared attribute declaration from the catalog.
func sharedAttr(root, catalog, use string) (ymap, error) {
	path := filepath.Join(root, catalog, "schemas", "attr_"+use+".yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("shared attribute %q: %w", use, err)
	}
	return parseMap(path, b)
}
