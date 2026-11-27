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
// sees, typed by the same YAML tags the parser reads: a plain 42 is a
// number, "42" a string. JSON has only string keys, so every key becomes
// its text (`true:` is the key "true"), and a later duplicate key replaces
// the earlier one, as an editor's YAML-to-JSON reading does.
func asJSON(t *testing.T, data []byte) any {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		return nodeJSON(t, doc.Content[0])
	}
	return nil
}

func nodeJSON(t *testing.T, n *yaml.Node) any {
	t.Helper()
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	switch n.Kind {
	case yaml.MappingNode:
		m := map[string]any{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yaml.AliasNode {
				k = k.Alias
			}
			m[k.Value] = nodeJSON(t, n.Content[i+1])
		}
		return m
	case yaml.SequenceNode:
		out := make([]any, len(n.Content))
		for i, item := range n.Content {
			out[i] = nodeJSON(t, item)
		}
		return out
	}
	switch n.ShortTag() {
	case "!!int", "!!float":
		var f float64
		if err := n.Decode(&f); err != nil {
			t.Fatalf("yaml number %q: %v", n.Value, err)
		}
		return f
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err != nil {
			t.Fatalf("yaml boolean %q: %v", n.Value, err)
		}
		return b
	case "!!null":
		return nil
	default:
		return n.Value
	}
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
		reflect.TypeFor[Stages](), reflect.TypeFor[Stage](),
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
	if n := len(fieldCache); n != 19 {
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
	// gate.stages takes exactly the closed set of stages.
	var stages struct {
		Defs map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(JSONSchema(), &stages); err != nil {
		t.Fatal(err)
	}
	var got []string
	for k := range stages.Defs["Stages"].Properties {
		got = append(got, k)
	}
	want := strs(StageNames)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("schema Stages keys = %v, StageNames = %v", got, want)
	}
}

// TestParserAndSchemaAgree pins the parser and the editor schema to one
// contract for every field written as a string or a mapping (gate, check,
// gate stages, sweep checks, probes), for scalar types and for keys: each input is
// accepted by both or refused by both. A case marked parserOnly is a rule
// JSON Schema cannot state, so only the parser is held to it: a duplicate
// key (JSON has no such thing; an editor sees the last value), a non-string
// key whose text is a valid key (JSON keys are all strings, so `true:` and
// `"true":` look alike to it), and uniqueness across list entries.
func TestParserAndSchemaAgree(t *testing.T) {
	rs := resolvedSchema(t)
	const head = "version: 1\n"
	env := func(s string) string { return head + "gate: {run: x, env: " + s + "}\n" }
	sweep := func(s string) string { return base + "sweep: {checks: [" + s + "]}\n" }
	probes := func(s string) string { return base + "release: {envs: [{name: prod, probes: [" + s + "]}]}\n" }
	stagesDoc := func(s string) string { return head + "gate: {stages: " + s + "}\n" }
	cases := []struct {
		name       string
		doc        string
		accept     bool
		parserOnly string
	}{
		// gate: a command string or a mapping.
		{"gate string", head + "gate: npm test\n", true, ""},
		{"gate quoted number", head + "gate: \"42\"\n", true, ""},
		{"gate integer", head + "gate: 42\n", false, ""},
		{"gate float", head + "gate: 4.5\n", false, ""},
		{"gate boolean", head + "gate: true\n", false, ""},
		{"gate null", head + "gate: null\n", false, ""},
		{"gate empty", head + "gate:\n", false, ""},
		{"gate list", head + "gate: [a]\n", false, ""},
		{"gate mapping", head + "gate: {run: x}\n", true, ""},
		{"gate run integer", head + "gate: {run: 42}\n", false, ""},
		{"gate run boolean", head + "gate: {run: false}\n", false, ""},
		{"gate pool integer", head + "gate: {run: x, pool: 1}\n", false, ""},
		{"gate pool quoted", head + "gate: {run: x, pool: \"p1\"}\n", true, ""},
		{"gate checks integer", head + "gate: {run: x, checks: [1]}\n", false, ""},
		{"gate tagged string", head + "gate: !!str 42\n", true, ""},

		// gate.env: any scalar but null.
		{"env scalars", env("{PORT: 8080, DEBUG: true, NAME: x, RATIO: 1.5}"), true, ""},
		{"env null value", env("{A: null}"), false, ""},
		{"env list value", env("{A: [x]}"), false, ""},
		{"env integer key", env("{1: x}"), false, ""},
		{"env boolean key", env("{true: x}"), false, "the key's text, true, is a valid name"},
		{"env null key", env("{null: x}"), false, "the key's text, null, is a valid name"},
		{"env duplicate key", env("{A: x, A: y}"), false, "duplicate key"},

		// check: a command string or a mapping.
		{"check string", base + "check: npm run lint\n", true, ""},
		{"check quoted number", base + "check: \"42\"\n", true, ""},
		{"check integer", base + "check: 42\n", false, ""},
		{"check float", base + "check: 1.5\n", false, ""},
		{"check boolean", base + "check: false\n", false, ""},
		{"check null", base + "check: null\n", false, ""},
		{"check empty", base + "check:\n", false, ""},
		{"check mapping", base + "check: {checks: [Lint]}\n", true, ""},
		{"check checks integer", base + "check: {checks: [1]}\n", false, ""},

		// sweep.checks: a built-in name, or {name: <built-in>}, or a
		// repo command {name, run} whose name is not a built-in.
		{"sweep builtin", sweep("stale_pr"), true, ""},
		{"sweep builtin quoted", sweep("\"pin_drift\""), true, ""},
		{"sweep unknown builtin", sweep("stale"), false, ""},
		{"sweep integer", sweep("42"), false, ""},
		{"sweep float", sweep("1.5"), false, ""},
		{"sweep boolean", sweep("true"), false, ""},
		{"sweep null", sweep("null"), false, ""},
		{"sweep builtin by name", sweep("{name: stale_pr}"), true, ""},
		{"sweep unknown by name", sweep("{name: licenses}"), false, ""},
		{"sweep probe name", sweep("{name: health}"), false, ""},
		{"sweep command", sweep("{name: licenses, run: task licenses}"), true, ""},
		{"sweep command shadows builtin", sweep("{name: stale_pr, run: x}"), false, ""},
		{"sweep command empty run", sweep("{name: licenses, run: \"\"}"), false, ""},
		{"sweep command null run", sweep("{name: licenses, run: null}"), false, ""},
		{"sweep command integer run", sweep("{name: licenses, run: 42}"), false, ""},
		{"sweep command integer name", sweep("{name: 42, run: x}"), false, ""},
		{"sweep command without name", sweep("{run: x}"), false, ""},
		{"sweep empty mapping", sweep("{}"), false, ""},
		{"sweep builtin twice", sweep("stale_pr, {name: stale_pr}"), false, "uniqueness across entries"},

		// probes: the same rules over the probe built-ins.
		{"probe builtin", probes("health"), true, ""},
		{"probe integer", probes("42"), false, ""},
		{"probe boolean", probes("true"), false, ""},
		{"probe null", probes("null"), false, ""},
		{"probe builtin by name", probes("{name: synthetic}"), true, ""},
		{"probe unknown by name", probes("{name: smoke}"), false, ""},
		{"probe sweep name", probes("{name: stale_pr}"), false, ""},
		{"probe command", probes("{name: smoke, run: task smoke}"), true, ""},
		{"probe command shadows builtin", probes("{name: health, run: x}"), false, ""},
		{"probe command empty run", probes("{name: smoke, run: \"\"}"), false, ""},
		{"probe command integer name", probes("{name: 7, run: x}"), false, ""},
		{"probe empty mapping", probes("{}"), false, ""},

		// gate.stages: each stage is {run: <command>} or the exact
		// string off; the test stage's command is written once.
		{"stages valid", stagesDoc("{lint: {run: \"go vet ./...\"}, test: {run: \"go test ./...\"}, build: off}"), true, ""},
		{"stages empty mapping", head + "gate: {run: x, stages: {}}\n", true, ""},
		{"stages beside gate.run", head + "gate: {run: x, stages: {lint: off, sbom_scan: {run: scan.sh}}}\n", true, ""},
		{"stage off", stagesDoc("{test: {run: x}, sbom_scan: off}"), true, ""},
		{"stage quoted off", stagesDoc("{test: {run: x}, lint: \"off\"}"), true, ""},
		{"test stage off without run", stagesDoc("{test: off}"), true, ""},
		{"stage bad string", stagesDoc("{test: {run: x}, lint: \"go vet ./...\"}"), false, ""},
		{"stage capitalised off", stagesDoc("{test: {run: x}, lint: Off}"), false, ""},
		{"stage boolean", stagesDoc("{test: {run: x}, lint: false}"), false, ""},
		{"stage integer", stagesDoc("{test: {run: x}, lint: 0}"), false, ""},
		{"stage null", stagesDoc("{test: {run: x}, lint: null}"), false, ""},
		{"stage list", stagesDoc("{test: {run: x}, lint: [a]}"), false, ""},
		{"stage empty mapping", stagesDoc("{test: {run: x}, lint: {}}"), false, ""},
		{"stage run empty", stagesDoc("{test: {run: x}, lint: {run: \"\"}}"), false, ""},
		{"stage run integer", stagesDoc("{test: {run: x}, lint: {run: 1}}"), false, ""},
		{"stage unknown key", stagesDoc("{test: {run: x}, lint: {run: y, timeout: 1m}}"), false, ""},
		{"unknown stage", stagesDoc("{test: {run: x}, fmt: off}"), false, ""},
		{"stages list", head + "gate: {run: x, stages: [lint]}\n", false, ""},
		{"stages without a test command", stagesDoc("{lint: off}"), false, ""},
		{"gate.run and stages.test", head + "gate: {run: x, stages: {test: {run: y}}}\n", false, ""},
		{"gate.run and test stage off", head + "gate: {run: x, stages: {test: off}}\n", false, ""},
		{"every stage off", stagesDoc("{lint: off, test: off, build: off, sbom_scan: off}"), false, ""},
		{"stages on runner checks", head + "gate: {runner: checks, checks: [CI], stages: {lint: off}}\n", false, ""},
		{"stages with only checks", head + "gate: {checks: [CI], stages: {test: {run: x}}}\n", true, ""},
		{"stages on runner machine", head + "gate: {runner: machine, stages: {test: {run: x}}}\n", true, ""},
		{"duplicate stage", stagesDoc("{test: {run: x}, lint: off, lint: off}"), false, "duplicate key"},

		// Other string fields take only strings.
		{"regen integer", base + "generated: [a]\nregen: 42\n", false, ""},
		{"generated integer", base + "generated: [1]\n", false, ""},
		{"recurring name integer", base + "recurring: [{name: 1, schedule: \"0 3 * * 1\", run: x}]\n", false, ""},
		{"recurring name quoted", base + "recurring: [{name: \"1\", schedule: \"0 3 * * 1\", run: x}]\n", true, ""},

		// Keys.
		{"top-level boolean key", base + "true: x\n", false, ""},
		{"top-level integer key", base + "1: x\n", false, ""},
		{"queue integer key", base + "queue: {1: 2}\n", false, ""},
		{"playbooks boolean key", base + "playbooks: {true: p.md}\n", false, ""},
		{"playbooks string key", base + "playbooks: {main: p.md}\n", true, ""},
		{"duplicate top-level key", base + "gate: \"go test ./...\"\n", false, "duplicate key"},
		{"duplicate nested key", base + "queue: {batch_max: 2, batch_max: 3}\n", false, "duplicate key"},
		{"duplicate playbook", base + "playbooks: {main: a.md, main: b.md}\n", false, "duplicate key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, perr := Load([]byte(tc.doc))
			if got := perr == nil; got != tc.accept {
				t.Errorf("Parse accepted = %v, want %v (error: %v)\n%s", got, tc.accept, perr, tc.doc)
			}
			if tc.parserOnly != "" {
				return
			}
			serr := rs.Validate(asJSON(t, []byte(tc.doc)))
			if got := serr == nil; got != tc.accept {
				t.Errorf("the JSON schema accepted = %v, want %v (error: %v)\n%s", got, tc.accept, serr, tc.doc)
			}
		})
	}
}
