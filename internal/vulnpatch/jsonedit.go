package vulnpatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// setPackageJSON sets entries in the object at path of a package.json
// (["overrides"], ["pnpm", "overrides"], ["resolutions"]), creating it as
// needed. Every other key keeps its place and value, so the diff is the
// override and nothing else; the file is re-indented the way it was
// indented.
func setPackageJSON(file string, path []string, entries map[string]string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	out, err := setJSON(data, path, entries)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	// An existing file keeps its permission bits (atomicfile copies them).
	return atomicfile.WriteFile(file, out, 0o644)
}

func setJSON(data []byte, path []string, entries map[string]string) ([]byte, error) {
	root, err := parseObject(data)
	if err != nil {
		return nil, err
	}
	if err := root.setPath(path, entries); err != nil {
		return nil, err
	}
	indent := detectIndent(data)
	var buf bytes.Buffer
	if err := root.write(&buf, indent, 0); err != nil {
		return nil, err
	}
	if bytes.HasSuffix(bytes.TrimRight(data, " \t"), []byte("\n")) {
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// object is a JSON object with its keys in document order.
type object struct {
	keys []string
	vals map[string]json.RawMessage
}

// parseObject reads data as exactly one JSON object. It refuses what it
// could not write back faithfully: a duplicate key (a map keeps one value
// and one position, so a rewrite would silently drop the other) and anything
// after the object's closing brace (a rewrite would drop it). The values are
// kept as raw bytes, so objects nested below the ones setPath descends into
// are carried through verbatim, duplicates and all.
func parseObject(data []byte) (*object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	o := &object{vals: map[string]json.RawMessage{}}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("object key %v is not a string", tok)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if _, dup := o.vals[key]; dup {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		o.keys = append(o.keys, key)
		o.vals[key] = v
	}
	if tok, err := dec.Token(); err != nil {
		return nil, err
	} else if d, ok := tok.(json.Delim); !ok || d != '}' {
		return nil, fmt.Errorf("unexpected %v at the end of an object", tok)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("data after the top-level object")
		}
		return nil, fmt.Errorf("data after the top-level object: %w", err)
	}
	return o, nil
}

// checkPackageJSON reports why setPackageJSON must not edit file at path,
// nil when it can: the file is missing, is not one JSON object, has a
// duplicate key or trailing data at the top level or in an object on path,
// or something on path is not an object. It changes nothing.
func checkPackageJSON(file string, path []string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	_, err = setJSON(data, path, nil)
	return err
}

func (o *object) setPath(path []string, entries map[string]string) error {
	if len(path) == 0 {
		for _, k := range sortedKeys(entries) {
			v, err := marshalNoEscape(entries[k])
			if err != nil {
				return err
			}
			o.set(k, v)
		}
		return nil
	}
	child := &object{vals: map[string]json.RawMessage{}}
	if raw, ok := o.vals[path[0]]; ok {
		var err error
		if child, err = parseObject(raw); err != nil {
			return fmt.Errorf("%q: %w", path[0], err)
		}
	}
	if err := child.setPath(path[1:], entries); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := child.write(&buf, "", 0); err != nil {
		return err
	}
	o.set(path[0], buf.Bytes())
	return nil
}

func (o *object) set(k string, v json.RawMessage) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *object) write(buf *bytes.Buffer, indent string, level int) error {
	if len(o.keys) == 0 {
		buf.WriteString("{}")
		return nil
	}
	inner := strings.Repeat(indent, level+1)
	nl := "\n"
	if indent == "" {
		nl, inner = "", ""
	}
	buf.WriteString("{" + nl)
	for i, k := range o.keys {
		key, err := marshalNoEscape(k)
		if err != nil {
			return err
		}
		buf.WriteString(inner)
		buf.Write(key)
		buf.WriteString(": ")
		if indent == "" {
			if err := json.Compact(buf, o.vals[k]); err != nil {
				return err
			}
		} else if err := json.Indent(buf, o.vals[k], inner, indent); err != nil {
			return err
		}
		if i < len(o.keys)-1 {
			buf.WriteByte(',')
		}
		buf.WriteString(nl)
	}
	buf.WriteString(strings.Repeat(indent, level) + "}")
	return nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// detectIndent is the indentation of a JSON document's first nested line,
// two spaces when it has none.
func detectIndent(data []byte) string {
	i := bytes.IndexByte(data, '\n')
	if i < 0 {
		return "  "
	}
	rest := data[i+1:]
	n := 0
	for n < len(rest) && (rest[n] == ' ' || rest[n] == '\t') {
		n++
	}
	if n == 0 {
		return "  "
	}
	return string(rest[:n])
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
