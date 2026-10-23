// File: internal/codegen/user_scaffold_gofmt_test.go
//
// Every .go file forge leaves in a user's project must already be a fixed point
// of the project's own formatter. A freshly scaffolded project runs `forge lint`
// as its first act; if forge writes Go that golangci's formatter rejects, the
// project fails its own gate on arrival, and the failure reads as the user's.
//
// The specific shape this guards: forge INJECTS text into files whose other
// contents it does not own — a Deps field spliced into the user's struct, a
// check spliced into validateDeps, an import spliced before a closing paren. An
// injector cannot know the alignment of a field set it did not author, so
// alignment must be DERIVED at the write, never padded at the call site.

package codegen

import (
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeModule lays down a minimal module so the canonical formatter can resolve
// the local-import prefix the same way the pipeline's goimports pass does.
func writeModule(t *testing.T, dir, modulePath string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+modulePath+"\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
}

// assertGofmtFixedPoint is the assertion the whole file exists for: the bytes on
// disk equal their own format.Source output.
func assertGofmtFixedPoint(t *testing.T, path string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	want, err := format.Source(got)
	if err != nil {
		t.Fatalf("emitted %s does not parse: %v\n%s", path, err, got)
	}
	if string(got) != string(want) {
		t.Errorf("emitted %s is not gofmt-clean — a fresh project fails its own `forge lint` on arrival.\n--- emitted ---\n%s\n--- gofmt ---\n%s",
			filepath.Base(path), got, want)
	}
}

// TestEnsureDepsDBField_EmitsGofmtCleanGo drives the real injector against a
// Deps struct whose OTHER fields the injector did not write — the case a
// hand-padded `DB` + spaces + `orm.Context` literal cannot get right, because
// the correct column depends on the longest field name present.
func TestEnsureDepsDBField_EmitsGofmtCleanGo(t *testing.T) {
	for _, tc := range []struct {
		name string
		deps string
	}{
		{
			name: "a long sibling field name moves the alignment column",
			deps: "\tLogger *slog.Logger\n\tNotificationDispatcher Dispatcher\n\t// Add your dependencies here.\n",
		},
		{
			name: "short sibling names pull the column back in",
			deps: "\tLog *slog.Logger\n\t// Add your dependencies here.\n",
		},
		{
			name: "no marker comment — injection lands after the opening brace",
			deps: "\tConfigurationProvider Provider\n",
		},
		{
			name: "an empty Deps struct",
			deps: "\t// Add your dependencies here.\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeModule(t, dir, "example.com/demo")
			svcDir := filepath.Join(dir, "internal", "handlers", "item")
			if err := os.MkdirAll(svcDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			src := "package item\n\nimport (\n\t\"log/slog\"\n)\n\ntype Dispatcher interface{ Send() }\ntype Provider interface{ Get() }\n\ntype Deps struct {\n" +
				tc.deps + "}\n"
			path := filepath.Join(svcDir, "service.go")
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatalf("write service.go: %v", err)
			}

			if err := ensureDepsDBField(svcDir); err != nil {
				t.Fatalf("ensureDepsDBField: %v", err)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if !strings.Contains(string(got), "orm.Context") {
				t.Fatalf("the injector did not run — nothing to assert about its output:\n%s", got)
			}
			assertGofmtFixedPoint(t, path)
		})
	}
}

// TestWriteUserScaffold_DerivesFormatting pins the chokepoint itself, so a NEW
// injector added later inherits the guarantee without knowing it exists. The
// input here is deliberately mangled the way a text splice mangles things:
// wrong indentation, hand-guessed column padding, a blank line where none
// belongs.
func TestWriteUserScaffold_DerivesFormatting(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "example.com/demo")
	path := filepath.Join(dir, "spliced.go")
	mangled := "package demo\n\ntype Deps struct {\n" +
		"DB         orm.Context\n" +
		"        Logger *slog.Logger\n" +
		"\tVeryLongFieldName string\n" +
		"}\n"
	if err := writeUserScaffold(path, []byte(mangled)); err != nil {
		t.Fatalf("writeUserScaffold: %v", err)
	}
	assertGofmtFixedPoint(t, path)
}

// TestWriteUserScaffold_UnparseableGoIsWrittenVerbatim pins the escape hatch.
// Swallowing a broken render at the write would hide it; it must reach
// `go build ./...` and fail there with a real compiler error.
func TestWriteUserScaffold_UnparseableGoIsWrittenVerbatim(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "example.com/demo")
	path := filepath.Join(dir, "broken.go")
	broken := []byte("package demo\n\nfunc Nope( {\n")
	if err := writeUserScaffold(path, broken); err != nil {
		t.Fatalf("writeUserScaffold: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(broken) {
		t.Errorf("unparseable Go must reach the compiler unchanged; got:\n%s", got)
	}
}

// TestWriteUserScaffold_NonGoIsUntouched — the formatter applies to Go and
// nothing else. A YAML override or a Dockerfile passing through here must come
// out byte-identical.
func TestWriteUserScaffold_NonGoIsUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "values.yaml")
	body := []byte("a:   1\nb:\n      - x\n")
	if err := writeUserScaffold(path, body); err != nil {
		t.Fatalf("writeUserScaffold: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("non-Go content must be written verbatim; got:\n%s", got)
	}
}
