package manifest

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// A manifest is one file or a directory of fragments:
//
//	<id>/manifest.yaml    the manifest: id, connection, ... (and, optionally, inline actions/triggers)
//	<id>/types.yaml       the value of the manifest's `types:` key
//	<id>/actions/*.yaml   one action per file, <action id>.yaml; appended to `actions`
//	<id>/events/*.yaml    one trigger per file, <trigger id>.yaml; appended to `triggers`
//	<id>/schemas/*.yaml   shared pieces, reachable only through $ref
//
// The order of `actions` and `triggers` is meaningful (tool definitions, pickers
// and docs list in it). A manifest.yaml that has the key lists its entries
// itself, each inline or `- $ref: actions/<id>.yaml`, and every file in the
// directory must then be listed. Without the key the files are taken in file
// name order.
//
// `$ref: <relative path>.yaml` stands for the content of that file, resolved
// relative to the file that contains the reference and replacing the whole
// mapping it appears in. It takes the place of a YAML anchor, which cannot
// cross files. A `$ref` that is not a path to a .yaml/.yml file (a JSON Schema
// "#/$defs/x" inside params, say) is left alone.
const (
	typesFragment  = "types.yaml"
	actionsDir     = "actions"
	eventsDir      = "events"
	schemasDir     = "schemas"
	fragmentSuffix = ".yaml"
)

type assembler struct {
	fsys fs.FS
	// dir is the integration's directory; a $ref may not leave it.
	dir string
	// used records the files reached, to refuse an orphaned schemas/ file.
	used map[string]bool
}

// assemble reads the manifest at file and every fragment beside it, and
// returns the one YAML document they make.
func assemble(fsys fs.FS, file string) (*yaml.Node, error) {
	a := &assembler{fsys: fsys, dir: path.Dir(file), used: map[string]bool{file: true}}
	root, err := a.readFile(file, nil)
	if err != nil {
		return nil, err
	}
	doc := yamlDocument(root)
	if doc == nil || doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("a manifest is a YAML mapping")
	}
	if path.Base(file) != "manifest.yaml" {
		return root, nil
	}
	if err := a.addTypes(doc); err != nil {
		return nil, err
	}
	for _, c := range []struct{ dir, key string }{{actionsDir, "actions"}, {eventsDir, "triggers"}} {
		if err := a.addList(doc, c.dir, c.key); err != nil {
			return nil, err
		}
	}
	if err := a.checkSchemasUsed(); err != nil {
		return nil, err
	}
	return root, nil
}

func (a *assembler) addTypes(doc *yaml.Node) error {
	file := path.Join(a.dir, typesFragment)
	if _, err := fs.Stat(a.fsys, file); err != nil {
		return nil
	}
	if yamlChild(doc, "types") != nil {
		return fmt.Errorf("types are declared in both manifest.yaml and %s", typesFragment)
	}
	n, err := a.readFile(file, nil)
	if err != nil {
		return err
	}
	setChild(doc, "types", yamlDocument(n))
	return nil
}

// addList appends every <dir>/*.yaml, as an element, to the manifest's list
// under key.
func (a *assembler) addList(doc *yaml.Node, dir, key string) error {
	files, err := fs.Glob(a.fsys, path.Join(a.dir, dir, "*"+fragmentSuffix))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	sort.Strings(files)
	if list := yamlChild(doc, key); list != nil {
		return a.checkListed(files, list, dir, key)
	}
	list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	setChild(doc, key, list)
	for _, file := range files {
		n, err := a.readFile(file, nil)
		if err != nil {
			return err
		}
		el := yamlDocument(n)
		if el == nil || el.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: an entry of %s is a YAML mapping", file, dir)
		}
		id := yamlChild(el, "id")
		want := strings.TrimSuffix(path.Base(file), fragmentSuffix)
		if id == nil || id.Value != want {
			return fmt.Errorf("%s: file name must be the id it declares, %q", file, want)
		}
		list.Content = append(list.Content, el)
	}
	return nil
}

// checkListed holds a manifest that lists its own entries to naming every
// file of the directory, and each under its own id.
func (a *assembler) checkListed(files []string, list *yaml.Node, dir, key string) error {
	if list.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s must be a list", key)
	}
	for _, file := range files {
		if !a.used[file] {
			return fmt.Errorf("%s is not listed in %s: manifest.yaml lists its own %s, so list it there", file, key, key)
		}
		n, err := a.readFile(file, nil)
		if err != nil {
			return err
		}
		id := yamlChild(yamlDocument(n), "id")
		if want := strings.TrimSuffix(path.Base(file), fragmentSuffix); id == nil || id.Value != want {
			return fmt.Errorf("%s: file name must be the id it declares, %q", file, want)
		}
	}
	return nil
}

// checkSchemasUsed refuses a schemas/ file nothing refers to: it is dead text
// that still looks like part of the manifest.
func (a *assembler) checkSchemasUsed() error {
	files, err := fs.Glob(a.fsys, path.Join(a.dir, schemasDir, "*"+fragmentSuffix))
	if err != nil {
		return err
	}
	for _, f := range files {
		if !a.used[f] {
			return fmt.Errorf("%s is not referenced by any $ref", f)
		}
	}
	return nil
}

// readFile parses file and resolves every $ref in it. stack holds the files
// being resolved, to refuse a cycle.
func (a *assembler) readFile(file string, stack []string) (*yaml.Node, error) {
	for _, s := range stack {
		if s == file {
			return nil, fmt.Errorf("$ref cycle: %s", strings.Join(append(stack, file), " -> "))
		}
	}
	data, err := fs.ReadFile(a.fsys, file)
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	a.used[file] = true
	stack = append(stack, file)
	doc := &root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	resolved, err := a.resolve(doc, file, stack)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return resolved, nil
}

// resolve returns n with its $refs replaced by the files they name.
func (a *assembler) resolve(n *yaml.Node, file string, stack []string) (*yaml.Node, error) {
	if n.Kind == yaml.MappingNode {
		if ref := refTarget(n); ref != "" {
			if len(n.Content) != 2 {
				return nil, fmt.Errorf("line %d: $ref takes no sibling keys", n.Line)
			}
			target := path.Join(path.Dir(file), ref)
			if target != a.dir && !strings.HasPrefix(target, a.dir+"/") {
				return nil, fmt.Errorf("line %d: $ref %q leaves the integration directory", n.Line, ref)
			}
			got, err := a.readFile(target, stack)
			if err != nil {
				return nil, fmt.Errorf("line %d: $ref %q: %w", n.Line, ref, err)
			}
			return got, nil
		}
	}
	for i, c := range n.Content {
		r, err := a.resolve(c, file, stack)
		if err != nil {
			return nil, err
		}
		n.Content[i] = r
	}
	return n, nil
}

// refTarget is the file a `{$ref: path.yaml}` mapping names, or "".
func refTarget(n *yaml.Node) string {
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Value != "$ref" || v.Kind != yaml.ScalarNode {
			continue
		}
		if strings.HasPrefix(v.Value, "#") || (!strings.HasSuffix(v.Value, ".yaml") && !strings.HasSuffix(v.Value, ".yml")) {
			return ""
		}
		return v.Value
	}
	return ""
}

func setChild(m *yaml.Node, key string, v *yaml.Node) {
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

// AssembledJSON is the manifest at file (<dir>/manifest.yaml) with its
// fragments joined and its $refs resolved, as the JSON document the loader
// decodes; the published JSON Schema describes exactly this document.
func AssembledJSON(fsys fs.FS, file string) ([]byte, error) {
	root, err := assemble(fsys, file)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := root.Decode(&doc); err != nil {
		return nil, err
	}
	return json.Marshal(normalize(doc))
}

// AssembledJSONDir is AssembledJSON over a directory on disk.
func AssembledJSONDir(dir, file string) ([]byte, error) {
	return AssembledJSON(os.DirFS(dir), file)
}
