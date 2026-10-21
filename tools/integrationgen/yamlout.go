// Copyright (c) 2025 Reliant Labs
package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// The generator writes the YAML subset the manifest fragments use: ordered
// mappings, lists and scalars. It writes it itself rather than through a
// marshaller so the bytes (key order, flow versus block, quoting, folding) are
// the generator's to promise and a library upgrade cannot move them.

type entry struct {
	K string
	V any
}

// ymap is an ordered mapping.
type ymap []entry

func (m *ymap) set(k string, v any) { *m = append(*m, entry{k, v}) }

const (
	flowLimit = 150
	foldWidth = 78
	// foldAbove is the length past which a string in block position is
	// written as a folded `>-` block rather than one long line.
	foldAbove = 70
)

var plainScalar = regexp.MustCompile(`^[A-Za-z0-9_./$][A-Za-z0-9_ ./()-]*$`)

var yamlWords = map[string]bool{
	"true": true, "false": true, "null": true, "yes": true, "no": true,
	"on": true, "off": true, "y": true, "n": true, "~": true,
}

// scalar renders a string as a plain scalar when that reads back as the same
// string in every context, and as a JSON (hence YAML) double-quoted string
// otherwise.
func scalar(s string) string {
	if plainScalar.MatchString(s) && !strings.HasSuffix(s, " ") && !yamlWords[strings.ToLower(s)] {
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			var back any
			if yaml.Unmarshal([]byte(s), &back) == nil && back == s {
				return s
			}
		}
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func scalarOf(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return scalar(x), true
	case bool:
		return strconv.FormatBool(x), true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), true
	case nil:
		return "null", true
	}
	return "", false
}

// flow renders v on one line, or reports that it cannot be (prose, or a
// nested block).
func flow(v any) (string, bool) {
	if s, ok := scalarOf(v); ok {
		return s, true
	}
	switch x := v.(type) {
	case ymap:
		if len(x) == 0 {
			return "{}", true
		}
		parts := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := flow(e.V)
			if !ok {
				return "", false
			}
			parts = append(parts, scalar(e.K)+": "+s)
		}
		return "{ " + strings.Join(parts, ", ") + " }", true
	case []any:
		parts := make([]string, 0, len(x))
		for _, it := range x {
			s, ok := flow(it)
			if !ok {
				return "", false
			}
			parts = append(parts, s)
		}
		return "[" + strings.Join(parts, ", ") + "]", true
	}
	return "", false
}

// foldable reports whether s survives a round trip through a folded block:
// single spaces only, no edge whitespace, nothing a block header could read
// as an indentation or chomping indicator.
func foldable(s string) bool {
	return s != "" && strings.Join(strings.Fields(s), " ") == s && !strings.ContainsAny(s[:1], " >|-+")
}

func fold(s string, width int) []string {
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = w
		case len(cur)+1+len(w) > width:
			lines = append(lines, cur)
			cur = w
		default:
			cur += " " + w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// render writes the mapping m at indent, one entry per line.
func render(b *strings.Builder, m ymap, indent int) {
	pad := strings.Repeat(" ", indent)
	for _, e := range m {
		key := scalar(e.K)
		switch v := e.V.(type) {
		case string:
			if foldable(v) && len(pad)+len(key)+2+len(v) > foldAbove {
				fmt.Fprintf(b, "%s%s: >-\n", pad, key)
				for _, l := range fold(v, foldWidth-indent-2) {
					fmt.Fprintf(b, "%s  %s\n", pad, l)
				}
				continue
			}
			fmt.Fprintf(b, "%s%s: %s\n", pad, key, scalar(v))
		case ymap:
			// A mapping of properties is always a block: one property per line.
			if s, ok := flow(v); ok && e.K != "properties" && len(pad)+len(key)+2+len(s) <= flowLimit {
				fmt.Fprintf(b, "%s%s: %s\n", pad, key, s)
				continue
			}
			fmt.Fprintf(b, "%s%s:\n", pad, key)
			render(b, v, indent+2)
		case []any:
			if s, ok := flow(v); ok && len(pad)+len(key)+2+len(s) <= flowLimit {
				fmt.Fprintf(b, "%s%s: %s\n", pad, key, s)
				continue
			}
			fmt.Fprintf(b, "%s%s:\n", pad, key)
			renderList(b, v, indent+2)
		default:
			s, _ := scalarOf(v)
			fmt.Fprintf(b, "%s%s: %s\n", pad, key, s)
		}
	}
}

func renderList(b *strings.Builder, l []any, indent int) {
	pad := strings.Repeat(" ", indent)
	for _, it := range l {
		if s, ok := flow(it); ok && len(pad)+2+len(s) <= flowLimit {
			fmt.Fprintf(b, "%s- %s\n", pad, s)
			continue
		}
		if m, ok := it.(ymap); ok {
			var inner strings.Builder
			render(&inner, m, indent+2)
			out := inner.String()
			b.WriteString(pad + "- " + strings.TrimPrefix(out, strings.Repeat(" ", indent+2)))
			continue
		}
		s, _ := scalarOf(it)
		fmt.Fprintf(b, "%s- %s\n", pad, s)
	}
}

// fromNode converts a decoded YAML node to the writer's types, keeping
// mapping order.
func fromNode(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return fromNode(n.Content[0])
	case yaml.MappingNode:
		var m ymap
		for i := 0; i+1 < len(n.Content); i += 2 {
			v, err := fromNode(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			m.set(n.Content[i].Value, v)
		}
		return m, nil
	case yaml.SequenceNode:
		l := []any{}
		for _, c := range n.Content {
			v, err := fromNode(c)
			if err != nil {
				return nil, err
			}
			l = append(l, v)
		}
		return l, nil
	case yaml.ScalarNode:
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, err
		}
		return v, nil
	}
	return nil, fmt.Errorf("line %d: unsupported YAML node", n.Line)
}

// renderValue writes a top-level value: one flow line when it fits, a block
// otherwise.
func renderValue(b *strings.Builder, m ymap) {
	if s, ok := flow(m); ok && len(s) <= flowLimit {
		b.WriteString(s + "\n")
		return
	}
	render(b, m, 0)
}

// plain converts the writer's tree to what a YAML decoder returns for the text
// it wrote, for the round-trip check.
func plain(v any) any {
	switch x := v.(type) {
	case ymap:
		out := make(map[string]any, len(x))
		for _, e := range x {
			out[e.K] = plain(e.V)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, it := range x {
			out[i] = plain(it)
		}
		return out
	}
	return v
}

// roundTrips reports whether text decodes to exactly the tree m, and the
// first difference if not.
func roundTrips(text string, m ymap) error {
	var got any
	if err := yaml.Unmarshal([]byte(text), &got); err != nil {
		return fmt.Errorf("generated YAML does not parse: %w", err)
	}
	if !reflect.DeepEqual(got, plain(m)) {
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(plain(m))
		return fmt.Errorf("generated YAML does not decode to the model:\n got: %s\nwant: %s", g, w)
	}
	return nil
}

// parseMap decodes a YAML file that is one mapping.
func parseMap(path string, b []byte) (ymap, error) {
	var n yaml.Node
	if err := yaml.Unmarshal(b, &n); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	v, err := fromNode(&n)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m, ok := v.(ymap)
	if !ok {
		return nil, fmt.Errorf("%s: expected one mapping", path)
	}
	return m, nil
}
