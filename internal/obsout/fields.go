package obsout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Schema declares one document: its name and the dotted field paths a caller
// may select with --fields. A verb declares it once; --help prints it
// ([Schema.Help]) and --fields is validated against it, so a typo is an error
// that lists the real fields instead of an empty answer.
//
// Paths are dotted. A list is transparent: with "logs" a list of objects,
// "logs.service" is the service of every element. A path ending ".*" declares
// an open map whose keys are data ("logs.attributes.*"), so
// --fields logs.attributes.http.route is valid.
type Schema struct {
	// Name is the schema name stamped in the head, e.g. "forge.obs.logs/v1".
	Name string
	// Fields are every selectable path, parents included ("logs",
	// "logs.service").
	Fields []string
}

// alwaysKept are the keys a --fields projection keeps: the head, so a failure
// is never hidden, and the page, so a truncated list can still be resumed.
var alwaysKept = []string{"schema", "ok", "exit_code", "error", "truncated", "omitted", "next_page_token"}

// Help is the "Fields:" block for a verb's --help.
func (s Schema) Help() string {
	fs := append([]string(nil), s.Fields...)
	sort.Strings(fs)
	var b strings.Builder
	fmt.Fprintf(&b, "JSON schema %s. --fields takes dotted paths of:\n", s.Name)
	for _, f := range fs {
		b.WriteString("  " + f + "\n")
	}
	fmt.Fprintf(&b, "Always kept: %s.\n", strings.Join(alwaysKept, ", "))
	return b.String()
}

// ParseFields splits a --fields value ("a,b.c") and checks every path against
// the schema.
func (s Schema) ParseFields(csv string) ([]string, error) {
	var paths []string
	for _, p := range strings.Split(csv, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !s.valid(p) {
			return nil, fmt.Errorf("--fields: %q is not a field of %s; fields: %s", p, s.Name, strings.Join(s.sortedFields(), ", "))
		}
		paths = append(paths, p)
	}
	return paths, nil
}

func (s Schema) sortedFields() []string {
	fs := append([]string(nil), s.Fields...)
	sort.Strings(fs)
	return fs
}

// valid: p is declared, an ancestor of a declared path (selecting an object
// selects everything under it), or under an open ".*" map.
func (s Schema) valid(p string) bool {
	for _, f := range s.Fields {
		switch {
		case f == p, strings.HasPrefix(f, p+"."):
			return true
		case strings.HasSuffix(f, ".*") && strings.HasPrefix(p, strings.TrimSuffix(f, "*")):
			return true
		}
	}
	return false
}

// Write encodes doc to w: the whole document, or only the selected paths (plus
// the schema's always-kept keys) when fields is non-empty. Output is indented,
// the house --json convention.
func (s Schema) Write(w io.Writer, doc any, fields []string) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode %s: %w", s.Name, err)
	}
	if len(fields) > 0 {
		if raw, err = s.projectJSON(raw, fields); err != nil {
			return err
		}
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return fmt.Errorf("encode %s: %w", s.Name, err)
	}
	out.WriteByte('\n')
	_, err = w.Write(out.Bytes())
	return err
}

// Project returns the compact JSON of doc restricted to fields (plus the
// always-kept keys). It is Write without the indentation, for one NDJSON line.
func (s Schema) Project(doc any, fields []string) ([]byte, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", s.Name, err)
	}
	if len(fields) == 0 {
		return raw, nil
	}
	return s.projectJSON(raw, fields)
}

func (s Schema) projectJSON(raw []byte, fields []string) ([]byte, error) {
	v, err := decodeOrdered(raw)
	if err != nil {
		return nil, err
	}
	keep := &selection{children: map[string]*selection{}}
	for _, p := range append(append([]string(nil), alwaysKept...), fields...) {
		keep.add(strings.Split(p, "."))
	}
	out, _ := keep.project(v)
	return json.Marshal(out)
}

// ── selection ───────────────────────────────────────────────────────────

// selection is the tree of selected paths. A node with no children selects
// everything beneath it.
type selection struct{ children map[string]*selection }

func (n *selection) add(path []string) {
	if len(path) == 0 || n.children == nil {
		// Selecting an ancestor selects the subtree: drop narrower children.
		n.children = nil
		return
	}
	c, ok := n.children[path[0]]
	if !ok {
		c = &selection{children: map[string]*selection{}}
		n.children[path[0]] = c
	}
	c.add(path[1:])
}

// child is the selection for an object key. A key that contains dots (an
// attribute such as "http.route") is matched against the path segments as a
// run, so --fields entries.attributes.http.route selects it.
func (n *selection) child(key string) (*selection, bool) {
	if c, ok := n.children[key]; ok {
		return c, true
	}
	if !strings.Contains(key, ".") {
		return nil, false
	}
	cur := n
	for _, part := range strings.Split(key, ".") {
		if cur.children == nil {
			return cur, true // an ancestor already selected the subtree
		}
		c, ok := cur.children[part]
		if !ok {
			return nil, false
		}
		cur = c
	}
	return cur, true
}

// project returns v restricted to the selection and whether anything remains.
// Lists are transparent: the selection applies to each element.
func (n *selection) project(v any) (any, bool) {
	if n.children == nil {
		return v, true
	}
	switch t := v.(type) {
	case *object:
		out := &object{vals: map[string]any{}}
		for _, k := range t.keys {
			c, ok := n.child(k)
			if !ok {
				continue
			}
			if pv, ok := c.project(t.vals[k]); ok {
				out.keys = append(out.keys, k)
				out.vals[k] = pv
			}
		}
		return out, len(out.keys) > 0
	case []any:
		out := make([]any, 0, len(t))
		kept := false
		for _, e := range t {
			pe, ok := n.project(e)
			if !ok {
				pe = &object{vals: map[string]any{}}
			} else {
				kept = true
			}
			out = append(out, pe)
		}
		return out, kept
	}
	return nil, false
}

// ── ordered JSON ────────────────────────────────────────────────────────

// object is a JSON object that remembers its key order, so a projection keeps
// the document's declared field order instead of Go's sorted-map order.
type object struct {
	keys []string
	vals map[string]any
}

func (o *object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(o.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func decodeOrdered(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return readValue(dec)
}

func readValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch d {
	case '{':
		o := &object{vals: map[string]any{}}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			k := kt.(string)
			v, err := readValue(dec)
			if err != nil {
				return nil, err
			}
			if _, dup := o.vals[k]; !dup {
				o.keys = append(o.keys, k)
			}
			o.vals[k] = v
		}
		_, err := dec.Token() // '}'
		return o, err
	case '[':
		out := []any{}
		for dec.More() {
			v, err := readValue(dec)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		_, err := dec.Token() // ']'
		return out, err
	}
	return nil, fmt.Errorf("unexpected JSON delimiter %q", d)
}
