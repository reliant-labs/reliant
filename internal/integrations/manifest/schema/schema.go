// Package schema publishes the integration manifest format as JSON Schema.
//
// The format's one source is the manifest package: reliantv1.IntegrationManifest
// (what the loader decodes YAML into, strictly) for structure and doc comments,
// and the manifest package's exported rules (patterns, enums, bounds) for what
// the validators accept. This package derives the schema from them, so an editor
// told to use it (a `# yaml-language-server: $schema=...` comment) checks exactly
// what the loader checks. Nothing here is hand-maintained: a test fails when the
// checked-in files differ from what the code generates, and `go generate` (make
// generate-manifest-schema) rewrites them.
package schema

//go:generate go run ./cmd/schemagen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// File names of the published schemas, all in this directory.
const (
	ManifestFile = "manifest.schema.json"
	ActionFile   = "action.schema.json"
	EventFile    = "event.schema.json"
)

// ProtoGoFile is the generated Go for the manifest messages, relative to this
// directory; its field comments are the schema's descriptions.
const ProtoGoFile = "../../../../gen/reliant/v1/integration.pb.go"

const metaSchema = "https://json-schema.org/draft/2020-12/schema"

// fragmentRefDef is the object a fragment file reference parses to. A
// reference may stand in for any value (see manifest.Assemble), so every
// property accepts it beside its own type.
const fragmentRefDef = "FragmentRef"

// Generate returns the published schema files by name.
func Generate(protoGoFile string) (map[string][]byte, error) {
	docs, err := loadDocs(protoGoFile)
	if err != nil {
		return nil, err
	}
	g := &gen{docs: docs, defs: map[string]any{}}
	root := (&reliantv1.IntegrationManifest{}).ProtoReflect().Descriptor()
	if err := g.message(root); err != nil {
		return nil, err
	}
	g.defs[fragmentRefDef] = map[string]any{
		"type":                 "object",
		"description":          "A reference to a fragment file, resolved relative to the file that contains it. It stands for that file's content.",
		"required":             []string{"$ref"},
		"additionalProperties": false,
		"properties": map[string]any{
			"$ref": map[string]any{"type": "string", "pattern": `^[^#].*\.ya?ml$`},
		},
	}
	defs := g.defs
	out := map[string][]byte{}
	files := map[string]map[string]any{
		ManifestFile: {
			"$schema":     metaSchema,
			"title":       "Reliant integration manifest",
			"description": "The manifest of one integration: how a user connects, what it can do (actions) and what can start a run from it (triggers). Authored as YAML, either one manifest.yaml or a manifest.yaml with actions/, events/, types.yaml and schemas/ fragments beside it.",
			"$ref":        "#/$defs/" + string(root.Name()),
			"$defs":       defs,
		},
		ActionFile: {
			"$schema":     metaSchema,
			"title":       "Reliant integration action",
			"description": "One file of an integration's actions/ directory, named <id>.yaml.",
			"$ref":        ManifestFile + "#/$defs/ActionSpec",
		},
		EventFile: {
			"$schema":     metaSchema,
			"title":       "Reliant integration event",
			"description": "One file of an integration's events/ directory, named <id>.yaml: a trigger type.",
			"$ref":        ManifestFile + "#/$defs/TriggerSpec",
		},
	}
	for name, doc := range files {
		b, err := marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = b
	}
	return out, nil
}

// marshal is deterministic: encoding/json sorts map keys.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type gen struct {
	docs docs
	defs map[string]any
}

// message adds the definition of m, and of every message it reaches.
func (g *gen) message(m protoreflect.MessageDescriptor) error {
	name := string(m.Name())
	if _, done := g.defs[name]; done {
		return nil
	}
	g.defs[name] = nil // cycle guard
	props := map[string]any{}
	var oneofs []any
	seenOneof := map[string]bool{}
	fields := m.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		fname := string(fd.Name())
		if hidden[name+"."+fname] {
			continue
		}
		if o := fd.ContainingOneof(); o != nil && !o.IsSynthetic() {
			// Exactly one arm: each is an object with only its own property.
			if seenOneof[string(o.Name())] {
				continue
			}
			seenOneof[string(o.Name())] = true
			var arms []any
			for j := 0; j < o.Fields().Len(); j++ {
				arm := o.Fields().Get(j)
				s, err := g.field(name, arm)
				if err != nil {
					return err
				}
				arms = append(arms, map[string]any{
					"type":                 "object",
					"required":             []string{string(arm.Name())},
					"additionalProperties": false,
					"properties":           map[string]any{string(arm.Name()): s},
				})
			}
			oneofs = append(oneofs, arms)
			continue
		}
		s, err := g.field(name, fd)
		if err != nil {
			return err
		}
		props[fname] = s
	}
	def := map[string]any{"type": "object", "additionalProperties": false, "properties": props}
	if d := g.docs.message(name); d != "" {
		def["description"] = d
	}
	if req := required[name]; len(req) > 0 {
		def["required"] = req
	}
	switch len(oneofs) {
	case 0:
	case 1:
		// Exactly one arm. additionalProperties only sees this schema's own
		// properties, so name the arms here; their schemas live in oneOf.
		arms := oneofs[0].([]any)
		names := map[string]any{}
		for _, a := range arms {
			for k := range a.(map[string]any)["properties"].(map[string]any) {
				names[k] = true
			}
		}
		def["oneOf"] = arms
		def["properties"] = names
	default:
		return fmt.Errorf("%s: more than one oneof is not supported", name)
	}
	g.defs[name] = def
	return nil
}

// field is the schema of one message field, with the rules that apply to it.
func (g *gen) field(msg string, fd protoreflect.FieldDescriptor) (any, error) {
	path := msg + "." + string(fd.Name())
	var s map[string]any
	switch {
	case fd.IsMap():
		v, err := g.scalarOrMessage(path, fd.MapValue())
		if err != nil {
			return nil, err
		}
		s = map[string]any{"type": "object", "additionalProperties": withFragmentRef(v)}
	case fd.IsList():
		item, err := g.scalarOrMessage(path, fd)
		if err != nil {
			return nil, err
		}
		s = map[string]any{"type": "array", "items": withFragmentRef(item)}
		if n := maxItems[path]; n > 0 {
			s["maxItems"] = n
		}
		if n := minItems[path]; n > 0 {
			s["minItems"] = n
		}
	default:
		var err error
		s, err = g.scalarOrMessage(path, fd)
		if err != nil {
			return nil, err
		}
	}
	if d := g.docs.field(msg, string(fd.Name())); d != "" {
		s["description"] = d
	}
	return withFragmentRef(s), nil
}

func (g *gen) scalarOrMessage(path string, fd protoreflect.FieldDescriptor) (map[string]any, error) {
	var s map[string]any
	switch fd.Kind() {
	case protoreflect.StringKind:
		s = map[string]any{"type": "string"}
		if e := enums[path]; e != nil {
			if templatable[path] {
				s["anyOf"] = []any{
					map[string]any{"enum": e},
					map[string]any{"pattern": `\{\{`, "description": "A template: its value is checked per call."},
				}
			} else {
				s["enum"] = e
			}
		}
		if p := patterns[path]; p != "" {
			s["pattern"] = p
		}
		if n := maxLength[path]; n > 0 {
			s["maxLength"] = n
		}
	case protoreflect.BoolKind:
		s = map[string]any{"type": "boolean"}
	case protoreflect.Int32Kind, protoreflect.Int64Kind:
		s = map[string]any{"type": "integer"}
		if b, ok := bounds[path]; ok {
			s["minimum"], s["maximum"] = b[0], b[1]
		}
	case protoreflect.MessageKind:
		switch full := fd.Message().FullName(); full {
		case "google.protobuf.Struct":
			s = map[string]any{"type": "object"}
			if jsonSchemaFields[path] {
				s["description"] = "A JSON Schema object."
				s["properties"] = map[string]any{"type": map[string]any{"const": "object"}}
				s["required"] = []string{"type"}
			}
		case "google.protobuf.Value":
			s = map[string]any{}
		default:
			if err := g.message(fd.Message()); err != nil {
				return nil, err
			}
			s = map[string]any{"$ref": "#/$defs/" + string(fd.Message().Name())}
		}
	default:
		return nil, fmt.Errorf("%s: unsupported field kind %s", path, fd.Kind())
	}
	if k := keywordItems[path]; k {
		s["pattern"] = manifest.KeywordPattern.String()
		s["maxLength"] = 40
	}
	return s, nil
}

// withFragmentRef lets the property also be a fragment file reference. The
// field's description stays on the outer schema, where an editor shows it.
func withFragmentRef(s map[string]any) map[string]any {
	out := map[string]any{"anyOf": []any{without(s, "description"), map[string]any{"$ref": "#/$defs/" + fragmentRefDef}}}
	if d, ok := s["description"]; ok {
		out["description"] = d
	}
	return out
}

func without(m map[string]any, key string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k != key {
			out[k] = v
		}
	}
	return out
}

// docs holds the doc comments of the generated messages: protoc-gen-go copies
// each proto comment onto its Go struct and field, and drops it from the
// compiled descriptor.
type docs struct {
	messages map[string]string
	fields   map[string]string // "Message.proto_field"
}

func (d docs) message(name string) string { return d.messages[name] }
func (d docs) field(msg, field string) string {
	return d.fields[msg+"."+field]
}

func loadDocs(file string) (docs, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
	if err != nil {
		return docs{}, err
	}
	d := docs{messages: map[string]string{}, fields: map[string]string{}}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts := spec.(*ast.TypeSpec)
			st, ok := ts.Type.(*ast.StructType)
			if !ok || !ast.IsExported(ts.Name.Name) {
				continue
			}
			doc := gd.Doc
			if ts.Doc != nil {
				doc = ts.Doc
			}
			d.messages[ts.Name.Name] = clean(doc.Text())
			for _, fld := range st.Fields.List {
				if fld.Tag == nil || fld.Doc == nil {
					continue
				}
				tag, _ := strconv.Unquote(fld.Tag.Value)
				name := protoName(reflect.StructTag(tag).Get("protobuf"))
				if name != "" {
					d.fields[ts.Name.Name+"."+name] = clean(fld.Doc.Text())
				}
			}
		}
	}
	return d, nil
}

// protoName is the name= entry of a protobuf struct tag.
func protoName(tag string) string {
	for _, part := range strings.Split(tag, ",") {
		if n, ok := strings.CutPrefix(part, "name="); ok {
			return n
		}
	}
	return ""
}

// clean joins a doc comment's wrapped lines into paragraphs.
func clean(text string) string {
	var paras []string
	for _, p := range strings.Split(strings.TrimSpace(text), "\n\n") {
		paras = append(paras, strings.Join(strings.Fields(p), " "))
	}
	return strings.Join(paras, "\n\n")
}

// Write generates the schemas and writes them to dir.
func Write(dir, protoGoFile string) error {
	files, err := Generate(protoGoFile)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := os.WriteFile(dir+"/"+n, files[n], 0o644); err != nil {
			return err
		}
	}
	return nil
}
