package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateAPIDeepCopy_RetiringAKindLeavesACompilingPackage is the defect
// this generator exists for: a project deletes a kind's _types.go (the normal
// way to retire one, e.g. moving onto forge's deploy-tier types), and the old
// zz_generated.deepcopy.go still references it. Before, `forge generate`
// never touched deepcopy, so the package stayed broken.
func TestGenerateAPIDeepCopy_RetiringAKindLeavesACompilingPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("loads real Go packages; skipped under -short")
	}
	root := writeCRDFixture(t, "")
	dc := filepath.Join(root, "api", "v1alpha1", DeepCopyFile)

	// A stale deepcopy naming a type that no longer exists.
	stale := "package v1alpha1\n\nfunc (in *Retired) DeepCopy() *Retired { return in }\n"
	if err := os.WriteFile(dc, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	wrote, err := GenerateAPIDeepCopy(root)
	if err != nil {
		t.Fatalf("GenerateAPIDeepCopy: %v", err)
	}
	if len(wrote) != 1 || wrote[0] != filepath.Join("api", "v1alpha1", DeepCopyFile) {
		t.Fatalf("wrote %v", wrote)
	}
	got, err := os.ReadFile(dc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "Retired") {
		t.Error("the retired kind's deepcopy survived regeneration")
	}
	if !strings.Contains(string(got), "func (in *Gadget) DeepCopyObject()") {
		t.Errorf("the live kind's deepcopy was not generated:\n%s", got)
	}
}
