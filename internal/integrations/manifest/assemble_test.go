package manifest

import (
	"strings"
	"testing"
	"testing/fstest"
)

const fragHead = `
id: demo
version: 1
display_name: Demo
connection:
  base_url: https://api.example.com/v1
`

const fragAction = `
id: thing.get
placement: server
params:
  type: object
  properties:
    id: { $ref: ../schemas/id.yaml }
request: { method: GET, path: "/things/{{ params.id }}" }
`

func fragFS(extra map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{
		"demo/manifest.yaml":          {Data: []byte(fragHead)},
		"demo/actions/thing.get.yaml": {Data: []byte(fragAction)},
		"demo/schemas/id.yaml":        {Data: []byte("type: string\ndescription: The thing id.\n")},
	}
	for k, v := range extra {
		if v == "" {
			delete(fsys, k)
			continue
		}
		fsys[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return fsys
}

func TestLoadFSAssemblesFragments(t *testing.T) {
	ms, err := LoadFS(fragFS(map[string]string{
		"demo/actions/thing.list.yaml": `
id: thing.list
placement: server
request: { method: GET, path: /things }
`,
		"demo/events/thing.created.yaml": `
id: thing.created
events: [thing.created]
data: { type: object, additionalProperties: false, properties: { thing: { $ref: ../schemas/id.yaml } } }
`,
	}), TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	m := ms[0]
	if len(m.GetActions()) != 2 || len(m.GetTriggers()) != 1 {
		t.Fatalf("actions=%d triggers=%d", len(m.GetActions()), len(m.GetTriggers()))
	}
	// Without an actions: key the files are taken in name order.
	if m.GetActions()[0].GetId() != "thing.get" || m.GetActions()[1].GetId() != "thing.list" {
		t.Errorf("order: %v %v", m.GetActions()[0].GetId(), m.GetActions()[1].GetId())
	}
	props := m.GetActions()[0].GetParams().AsMap()["properties"].(map[string]any)
	if got := props["id"].(map[string]any)["description"]; got != "The thing id." {
		t.Errorf("$ref not resolved: %v", props["id"])
	}
	if got := m.GetActions()[0].GetParamOrder(); len(got) != 1 || got[0] != "id" {
		t.Errorf("param_order = %v", got)
	}
}

func TestLoadFSManifestListsItsOwnOrder(t *testing.T) {
	ms, err := LoadFS(fragFS(map[string]string{
		"demo/manifest.yaml": fragHead + "actions:\n  - $ref: actions/thing.get.yaml\n",
	}), TrustCurated)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms[0].GetActions()) != 1 {
		t.Fatalf("actions = %d", len(ms[0].GetActions()))
	}
	_, err = LoadFS(fragFS(map[string]string{
		"demo/manifest.yaml":      fragHead + "actions:\n  - $ref: actions/thing.get.yaml\n",
		"demo/actions/extra.yaml": "id: extra\nplacement: server\nrequest: { method: GET, path: /x }\n",
	}), TrustCurated)
	if err == nil || !strings.Contains(err.Error(), "extra.yaml is not listed") {
		t.Errorf("an unlisted action file should fail, got %v", err)
	}
}

func TestLoadFSTypesFragment(t *testing.T) {
	fsys := fragFS(nil)
	fsys["demo/types.yaml"] = &fstest.MapFile{Data: []byte("- id: repository\n  value: { type: string }\n")}
	root, err := assemble(fsys, "demo/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if yamlChild(yamlDocument(root), "types") == nil {
		t.Error("types.yaml was not attached under types:")
	}
	fsys["demo/manifest.yaml"] = &fstest.MapFile{Data: []byte(fragHead + "types: {}\n")}
	if _, err := assemble(fsys, "demo/manifest.yaml"); err == nil || !strings.Contains(err.Error(), "both manifest.yaml and types.yaml") {
		t.Errorf("types in both places should fail, got %v", err)
	}
}

func TestLoadFSRefErrors(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		want  string
	}{
		"missing file": {map[string]string{"demo/schemas/id.yaml": ""}, "file does not exist"},
		"escapes dir":  {map[string]string{"demo/actions/thing.get.yaml": strings.Replace(fragAction, "../schemas/id.yaml", "../../other/id.yaml", 1)}, "leaves the integration directory"},
		"sibling keys": {map[string]string{"demo/actions/thing.get.yaml": strings.Replace(fragAction, "{ $ref: ../schemas/id.yaml }", "{ $ref: ../schemas/id.yaml, type: string }", 1)}, "takes no sibling keys"},
		"cycle": {map[string]string{
			"demo/schemas/id.yaml":    "a: { $ref: other.yaml }\n",
			"demo/schemas/other.yaml": "b: { $ref: id.yaml }\n",
		}, "cycle"},
		"wrong file name": {map[string]string{"demo/actions/thing.get.yaml": strings.Replace(fragAction, "id: thing.get", "id: thing.put", 1)}, "file name must be the id"},
		"orphan schema":   {map[string]string{"demo/schemas/unused.yaml": "type: string\n"}, "not referenced by any $ref"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFS(fragFS(c.files), TrustCurated)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("want %q, got %v", c.want, err)
			}
		})
	}
}

// A $ref that is not a YAML file path is JSON Schema's own and passes through.
func TestJSONSchemaRefIsLeftAlone(t *testing.T) {
	fsys := fragFS(map[string]string{"demo/actions/thing.get.yaml": strings.Replace(strings.Replace(fragAction, "{ $ref: ../schemas/id.yaml }", "{ $ref: \"#/$defs/id\" }", 1), "  properties:", "  $defs: { id: { type: string } }\n  properties:", 1), "demo/schemas/id.yaml": ""})
	if _, err := LoadFS(fsys, TrustCurated); err != nil {
		t.Fatal(err)
	}
}
