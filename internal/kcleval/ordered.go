package kcleval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Member is one key of an Object.
type Member struct {
	Key   string
	Value any
}

// Object is a KCL dict or schema instance with its keys in the order KCL
// declared them.
//
// It exists because the render arrives as JSON in declared order and a Go map
// has none: decoding into map[string]any alphabetised every `--format json`
// document, so a consumer comparing committed JSON byte for byte could not use
// forge's output and needed a second `kcl run` pass. KCL cannot repeat a key,
// so a slice of members loses nothing a map held.
//
// A decoded document holds only Object, []any, string, json.Number, bool and
// nil. Numbers stay json.Number — KCL's own text — so `1.0` and `1e+20` print
// as KCL wrote them rather than as a float64 would.
type Object []Member

// get returns the value of key.
func (o Object) get(key string) (any, bool) {
	for _, m := range o {
		if m.Key == key {
			return m.Value, true
		}
	}
	return nil, false
}

// Decode parses KCL's JSON result into a document that keeps key order.
func Decode(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected content after the top-level value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return tok, nil // string, json.Number, bool or nil
	}
	switch d {
	case '{':
		obj := Object{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			obj = append(obj, Member{Key: kt.(string), Value: v})
		}
		if _, err := dec.Token(); err != nil { // '}'
			return nil, err
		}
		return obj, nil
	case '[':
		list := []any{}
		for dec.More() {
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		if _, err := dec.Token(); err != nil { // ']'
			return nil, err
		}
		return list, nil
	}
	return nil, fmt.Errorf("unexpected delimiter %q", d)
}

// encodeJSON writes v the way `kcl run --format json` does: four-space
// indent, members in declared order, an empty container as `[]` / `{}`, a
// number as KCL wrote it, and a string escaped with only what JSON requires —
// no \u003c for `<`, no \u2028 — so the bytes match kcl's.
func encodeJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := writeJSON(&b, v, 0); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}

func writeJSON(b *bytes.Buffer, v any, depth int) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case json.Number:
		b.WriteString(x.String())
	case string:
		writeJSONString(b, x)
	case Object:
		if len(x) == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteString("{\n")
		for i, m := range x {
			writeIndent(b, depth+1)
			writeJSONString(b, m.Key)
			b.WriteString(": ")
			if err := writeJSON(b, m.Value, depth+1); err != nil {
				return err
			}
			if i < len(x)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		writeIndent(b, depth)
		b.WriteByte('}')
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteString("[\n")
		for i, e := range x {
			writeIndent(b, depth+1)
			if err := writeJSON(b, e, depth+1); err != nil {
				return err
			}
			if i < len(x)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		writeIndent(b, depth)
		b.WriteByte(']')
	default:
		return fmt.Errorf("cannot encode %T", v)
	}
	return nil
}

func writeIndent(b *bytes.Buffer, depth int) {
	for i := 0; i < depth; i++ {
		b.WriteString("    ")
	}
}

func writeJSONString(b *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteByte(hex[r>>4])
			b.WriteByte(hex[r&0xf])
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// encodeYAML writes v as YAML with members in declared order. Scalars go
// through yaml.v3's own quoting, so a string that would read back as another
// type (`"0755"`, `"y"`, `"null"`) is quoted.
func encodeYAML(v any) ([]byte, error) {
	n, err := yamlNode(v)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	enc.CompactSeqIndent()
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func yamlNode(v any) (*yaml.Node, error) {
	switch x := v.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(x)}, nil
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(x.String(), ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: x.String()}, nil
	case string:
		return yamlString(x), nil
	case Object:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, m := range x {
			val, err := yamlNode(m.Value)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, yamlString(m.Key), val)
		}
		return n, nil
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, e := range x {
			val, err := yamlNode(e)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, val)
		}
		return n, nil
	}
	return nil, fmt.Errorf("cannot encode %T", v)
}

// yaml11Bools are the words a YAML 1.1 parser (PyYAML, the sigs.k8s.io encoder
// forge used before, kcl itself) reads as a boolean. yaml.v3 emits them bare
// because 1.2 does not, which would silently turn a `y` key into `true` for
// any 1.1 consumer, so they are quoted.
var yaml11Bools = map[string]bool{
	"y": true, "Y": true, "yes": true, "Yes": true, "YES": true,
	"n": true, "N": true, "no": true, "No": true, "NO": true,
	"on": true, "On": true, "ON": true, "off": true, "Off": true, "OFF": true,
}

func yamlString(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	if yaml11Bools[s] {
		n.Style = yaml.SingleQuotedStyle
	}
	return n
}
