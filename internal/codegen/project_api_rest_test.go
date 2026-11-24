package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProjectAPIRESTEnabled_EveryYAMLSpelling pins that the `api.rest`
// answer comes from a real YAML parse, not a line scan.
//
// `api: {rest: true}` is the same document as the block-style spelling to a
// YAML parser, and the hand-rolled scanner this helper replaced read it as
// OFF: `rest:` sat on the `api:` line, so the scan never entered the block.
// A silent false there means the vanguard REST transcoder is not wired and
// the CRUD protos ship without `google.api.http` annotations — a wrong answer
// that surfaces as "REST just doesn't work", nowhere near forge.yaml.
//
// The quoted-scalar case pins the opposite half of the trade. The scanner was
// LENIENT — it stripped quotes and string-compared, so `rest: "true"` turned
// REST on. The schema says bool, so the canonical loader rejects it, and the
// command that loaded forge.yaml has already failed with a real
// "cannot unmarshal !!str into bool" before this best-effort helper is
// reached. One answer for one document, in both directions.
func TestProjectAPIRESTEnabled_EveryYAMLSpelling(t *testing.T) {
	const head = "name: demo\nmodule_path: github.com/example/demo\n"

	cases := []struct {
		name string
		yaml string
		want bool
	}{
		{"block style", head + "api:\n  rest: true\n", true},
		{"flow style", head + "api: {rest: true}\n", true},
		{"quoted scalar is not a bool", head + "api:\n  rest: \"true\"\n", false},
		{"trailing comment", head + "api:\n  rest: true # transcode\n", true},
		{"explicitly off", head + "api:\n  rest: false\n", false},
		{"no api block", head, false},
		{"sibling rest key outside api", head + "docs:\n  format: markdown\n", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(tc.yaml), 0o600); err != nil {
				t.Fatalf("write forge.yaml: %v", err)
			}
			if got := projectAPIRESTEnabled(dir); got != tc.want {
				t.Errorf("projectAPIRESTEnabled = %v, want %v for:\n%s", got, tc.want, tc.yaml)
			}
		})
	}
}

// TestProjectAPIRESTEnabled_BestEffort pins the fallback contract: a project
// with no forge.yaml at all (the initial scaffold pass) and one whose
// forge.yaml cannot be loaded both resolve to "REST off" rather than
// panicking or erroring out of codegen.
func TestProjectAPIRESTEnabled_BestEffort(t *testing.T) {
	if got := projectAPIRESTEnabled(t.TempDir()); got {
		t.Errorf("missing forge.yaml: got true, want false")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: demo\nno_such_key: 1\n"), 0o600); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	if got := projectAPIRESTEnabled(dir); got {
		t.Errorf("invalid forge.yaml: got true, want false")
	}
}
