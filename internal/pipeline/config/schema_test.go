package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"gopkg.in/yaml.v3"
)

func resolvedSchema(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	var s jsonschema.Schema
	if err := json.Unmarshal(JSONSchema(), &s); err != nil {
		t.Fatalf("pipeline.schema.json does not parse: %v", err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("pipeline.schema.json does not resolve: %v", err)
	}
	return rs
}

// asJSON turns a YAML document into the value a JSON Schema validator
// sees: the same document, read back from its JSON encoding.
func asJSON(t *testing.T, data []byte) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("json: %v", err)
	}
	return out
}

func testdataFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", dir, "*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no testdata in %s: %v", dir, err)
	}
	out := map[string][]byte{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = data
	}
	return out
}

// TestExamplesParseAndMatchSchema: design §10.2's three files (forge's with
// its `...` filled in) and a file using every key parse, and validate
// against the editor schema.
func TestExamplesParseAndMatchSchema(t *testing.T) {
	rs := resolvedSchema(t)
	files := testdataFiles(t, "valid")
	for _, name := range []string{"control-plane.yaml", "forge.yaml", "minimal.yaml", "everything.yaml"} {
		if _, ok := files[name]; !ok {
			t.Errorf("testdata/valid/%s is missing", name)
		}
	}
	for name, data := range files {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(data); err != nil {
				t.Errorf("Parse rejected it:\n%v", err)
			}
			if err := rs.Validate(asJSON(t, data)); err != nil {
				t.Errorf("the JSON schema rejected it: %v", err)
			}
		})
	}

	// Spot-check the two real repositories' files resolve as the design
	// intends.
	cp, _ := Load(files["control-plane.yaml"])
	if cp.Gate.Run != "task ci:full" || cp.Queue.BatchMax != 6 || cp.Release == nil || cp.Release.Schedule.String() != DefaultReleaseSchedule ||
		len(cp.Sweep.Checks) != 3 || cp.Pins[0].LockstepWith[0] != "reliant-labs/forge" {
		t.Errorf("control-plane resolved to %+v", cp)
	}
	forge, _ := Load(files["forge.yaml"])
	if forge.Gate.Runner != RunnerMachine || !slices.Equal(forge.Gate.Checks, []string{"Windows"}) || !forge.Release.OnLand ||
		forge.Consumers[0].CommandFor("abc") != "task ci:consumer PRODUCER_SHA=abc" {
		t.Errorf("forge resolved to %+v", forge)
	}
}

// TestInvalidFilesRejectedByBoth: each testdata/invalid file is refused by
// Parse at the path its first line names, and by the JSON schema.
func TestInvalidFilesRejectedByBoth(t *testing.T) {
	rs := resolvedSchema(t)
	files := testdataFiles(t, "invalid")
	for _, name := range []string{"missing-gate.yaml", "version-2.yaml", "unknown-key.yaml", "canary-strategy.yaml", "recurring-prompt-and-run.yaml"} {
		if _, ok := files[name]; !ok {
			t.Errorf("testdata/invalid/%s is missing", name)
		}
	}
	for name, data := range files {
		t.Run(name, func(t *testing.T) {
			first, _, _ := strings.Cut(string(data), "\n")
			wantPath, ok := strings.CutPrefix(first, "# want: ")
			if !ok {
				t.Fatalf("first line must be `# want: <path>`, got %q", first)
			}
			errs := parseErrors(t, string(data))
			if findError(errs, wantPath) == nil {
				t.Errorf("Parse errors %v, want one at %s", errs, wantPath)
			}
			if err := rs.Validate(asJSON(t, data)); err == nil {
				t.Errorf("the JSON schema accepted it")
			}
		})
	}
}

// TestSchemaKeysMatchTypes holds the editor schema to the parser: every
// object in the schema declares exactly the keys its Go type decodes.
func TestSchemaKeysMatchTypes(t *testing.T) {
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Defs       map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(JSONSchema(), &doc); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]json.RawMessage) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	fieldsOf := func(typ reflect.Type) []string {
		out := make([]string, 0)
		for k := range yamlFields(typ) {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	if got, want := keys(doc.Properties), fieldsOf(reflect.TypeFor[Config]()); !slices.Equal(got, want) {
		t.Errorf("schema top level = %v, Config = %v", got, want)
	}
	for _, typ := range []reflect.Type{
		reflect.TypeFor[Gate](), reflect.TypeFor[Check](), reflect.TypeFor[OwnerApproval](), reflect.TypeFor[Pin](),
		reflect.TypeFor[Consumer](), reflect.TypeFor[Queue](), reflect.TypeFor[Review](), reflect.TypeFor[Fix](),
		reflect.TypeFor[Flake](), reflect.TypeFor[Release](), reflect.TypeFor[ReleaseEnv](), reflect.TypeFor[Strategy](),
		reflect.TypeFor[RecurringJob](), reflect.TypeFor[Sweep](), reflect.TypeFor[SweepCheck](), reflect.TypeFor[Probe](),
	} {
		def, ok := doc.Defs[typ.Name()]
		if !ok {
			t.Errorf("schema has no $defs/%s", typ.Name())
			continue
		}
		if got, want := keys(def.Properties), fieldsOf(typ); !slices.Equal(got, want) {
			t.Errorf("schema $defs/%s = %v, Go type = %v", typ.Name(), got, want)
		}
	}
	// Every struct type the decoder knows is covered above.
	if n := len(fieldCache); n != 17 {
		t.Errorf("the decoder knows %d struct types; add the new ones to this test and to the schema", n)
	}
}

func TestSchemaEnumsMatchClosedSets(t *testing.T) {
	var doc struct {
		Properties map[string]struct {
			PropertyNames struct {
				Enum []string `json:"enum"`
			} `json:"propertyNames"`
		} `json:"properties"`
		Defs map[string]struct {
			Enum       []string `json:"enum"`
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(JSONSchema(), &doc); err != nil {
		t.Fatal(err)
	}
	strs := func(items any) []string {
		v := reflect.ValueOf(items)
		out := make([]string, v.Len())
		for i := range out {
			out[i] = v.Index(i).String()
		}
		return out
	}
	checks := []struct {
		where string
		got   []string
		want  []string
	}{
		{"playbooks", doc.Properties["playbooks"].PropertyNames.Enum, strs(IncidentSources)},
		{"sweepBuiltin", doc.Defs["sweepBuiltin"].Enum, strs(SweepBuiltins)},
		{"OwnerApproval.kind", doc.Defs["OwnerApproval"].Properties["kind"].Enum, strs(ApprovalKinds)},
		{"probeBuiltin", doc.Defs["probeBuiltin"].Enum, strs(ProbeBuiltins)},
		{"ReleaseEnv.inconclusive", doc.Defs["ReleaseEnv"].Properties["inconclusive"].Enum, strs(Inconclusives)},
	}
	for _, c := range checks {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("schema %s enum = %v, Go closed set = %v", c.where, c.got, c.want)
		}
	}
}
