package kcleval_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/fluxdelivery/internal/kcleval"
)

func TestEvalDecodesTheTopLevelResult(t *testing.T) {
	slow(t)
	var out struct {
		Name  string   `json:"name"`
		Ports []int    `json:"ports"`
		Tags  []string `json:"tags"`
	}
	main := "name = \"flux\"\nports = [80, 443]\ntags = [\"a\", \"b\"]\n"
	if err := kcleval.Eval(t.TempDir(), main, &out); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if out.Name != "flux" || len(out.Ports) != 2 || out.Ports[1] != 443 || strings.Join(out.Tags, ",") != "a,b" {
		t.Errorf("decoded %+v", out)
	}
}

func TestEvalCanImportTheEmbeddedForgeModule(t *testing.T) {
	slow(t)
	var out struct {
		Template string `json:"template"`
	}
	if err := kcleval.Eval(t.TempDir(), "import forge\n\ntemplate = forge.FLUX_DELIVERY_TEMPLATE\n", &out); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if out.Template == "" {
		t.Error("`import forge` resolved but FLUX_DELIVERY_TEMPLATE came back empty")
	}
}

func TestEvalLeavesNoScratchDirectoryBehind(t *testing.T) {
	slow(t)
	tmp := t.TempDir()
	var out map[string]any
	if err := kcleval.Eval(tmp, "x = 1\n", &out); err != nil {
		t.Fatal(err)
	}
	// A failing evaluation must clean up too.
	if err := kcleval.Eval(tmp, "x = (\n", &out); err == nil {
		t.Fatal("a syntax error evaluated successfully")
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("Eval left %d entries in tmp: %v", len(entries), entries)
	}
}

func TestEvalReportsAFailedRender(t *testing.T) {
	slow(t)
	var out map[string]any
	err := kcleval.Eval(t.TempDir(), "x = undefined_name\n", &out)
	if err == nil || !strings.HasPrefix(err.Error(), "kcleval: render:") {
		t.Fatalf("Eval = %v, want a 'kcleval: render:' error", err)
	}
	if !strings.Contains(err.Error(), "undefined_name") {
		t.Errorf("error %q does not name the offending identifier", err)
	}
}

func TestEvalReportsAResultThatDoesNotFitTheTarget(t *testing.T) {
	slow(t)
	var out struct {
		Name int `json:"name"`
	}
	err := kcleval.Eval(t.TempDir(), "name = \"not a number\"\n", &out)
	if err == nil || !strings.Contains(err.Error(), "kcleval: decoding the render") {
		t.Fatalf("Eval = %v, want a decode error", err)
	}
	if !strings.Contains(err.Error(), "not a number") {
		t.Errorf("error %q does not include the rendered document", err)
	}
}

func TestEvalFailsWhenTheScratchDirectoryCannotBeCreated(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	var out map[string]any
	if err := kcleval.Eval(missing, "x = 1\n", &out); err == nil {
		t.Fatal("Eval succeeded although tmp does not exist")
	}
}

func TestTemplateIsNonEmptyAndMentionsItsSchema(t *testing.T) {
	slow(t)
	got, err := kcleval.Template(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "forge.dev/flux-delivery/v1") {
		t.Errorf("template does not carry the flux-delivery schema:\n%s", got)
	}
}

func TestTemplateFailsWhenTheScratchDirectoryIsUnusable(t *testing.T) {
	if _, err := kcleval.Template(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("Template succeeded although tmp does not exist")
	}
}
