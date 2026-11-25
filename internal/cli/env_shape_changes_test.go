package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/changesdecl"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/pkg/release"
)

const shapeChangesYAML = `version: 1
changes:
  new-checkout:
    description: Shorter checkout flow
    type: bool
    default: control
    variants:
      control: false
      treatment: true
`

// Every env's shape carries the project's changes.yaml, and only when the
// project has one: without it the shape is exactly what it was before the
// field. An invalid file refuses the projection with the file and line.
func TestRenderEnvShape_CarriesTheProjectsChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	kclplugin.Register()
	dir := writeRenderStdoutProject(t)
	t.Chdir(dir)
	render := func() (envShapeDoc, error) {
		return renderEnvShape(context.Background(), io.Discard, dir, "prod")
	}

	doc, err := render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if doc.Shape.Changes != nil {
		t.Fatalf("a project with no changes.yaml declares changes: %+v", doc.Shape.Changes)
	}
	raw, err := json.Marshal(doc.Shape)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"changes"`)) {
		t.Errorf("a shape with no changes carries the key: %s", raw)
	}

	if err := os.WriteFile(filepath.Join(dir, changesdecl.FileName), []byte(shapeChangesYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err = render()
	if err != nil {
		t.Fatalf("render with changes.yaml: %v", err)
	}
	want := release.ShapeChange{
		Type: release.ChangeTypeBool, Default: "control", Description: "Shorter checkout flow",
		Variants: map[string]json.RawMessage{"control": json.RawMessage(`false`), "treatment": json.RawMessage(`true`)},
	}
	got, ok := doc.Shape.Changes["new-checkout"]
	if !ok || len(doc.Shape.Changes) != 1 || got.Type != want.Type || got.Default != want.Default || got.Description != want.Description ||
		string(got.Variants["control"]) != "false" || string(got.Variants["treatment"]) != "true" {
		t.Errorf("shape changes = %+v, want new-checkout = %+v", doc.Shape.Changes, want)
	}
	var summary strings.Builder
	writeShapeSummary(&summary, doc)
	if !strings.Contains(summary.String(), "new-checkout [bool]: 2 variant(s), default control") {
		t.Errorf("the summary does not list the change:\n%s", summary.String())
	}

	if err := os.WriteFile(filepath.Join(dir, changesdecl.FileName), []byte("version: 1\nchanges:\n  k:\n    type: int\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := render(); err == nil || !strings.Contains(err.Error(), "changes.yaml:4:") {
		t.Errorf("an invalid changes.yaml: got %v, want a refusal naming changes.yaml:4", err)
	}
}
