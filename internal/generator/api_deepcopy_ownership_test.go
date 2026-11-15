package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `forge generate` owns every api/**/zz_generated.deepcopy.go
// (codegen.GenerateAPIDeepCopy, controller-tools' object generator), and that
// generator ALWAYS emits DeepCopyObject for a `+kubebuilder:object:root=true`
// type. A scaffolded _types.go under api/ that ALSO hand-declares it makes the
// package fail to compile on the very first `forge generate`:
//
//	method Gadget.DeepCopyObject already declared at api/v1alpha1/gadget_types.go:93:19
//
// which is how `forge scaffold crd` / `forge scaffold operator --api-package`
// followed by `forge generate` stopped producing a buildable tree (the e2e
// corpus's TestE2EAddVerbsProduceABuildableTree and
// TestE2EScaffoldIsReviveExportedClean). These pin the ownership line: under
// api/ the generator owns the method; anywhere else the scaffold must still
// provide it, because nothing generates deepcopy there.

func TestScaffoldedCRDTypesLeaveDeepCopyToTheGenerator(t *testing.T) {
	root := t.TempDir()
	if err := GenerateOperatorFiles(root, "example.com/app", "fleet", "example.dev", "v1alpha1"); err != nil {
		t.Fatalf("GenerateOperatorFiles: %v", err)
	}
	if err := GenerateCRDFiles(CRDGenInput{
		Root: root, ModulePath: "example.com/app", OperatorName: "fleet",
		TypeName: "Gadget", Group: "example.dev", Version: "v1alpha1", Shape: CRDShapeStateMachine,
	}); err != nil {
		t.Fatalf("GenerateCRDFiles: %v", err)
	}
	types := readFile(t, filepath.Join(root, "api", "v1alpha1", "gadget_types.go"))
	if strings.Contains(types, "DeepCopyObject()") {
		t.Fatalf("api/v1alpha1/gadget_types.go hand-declares DeepCopyObject; forge generate's "+
			"zz_generated.deepcopy.go declares it too, so the package cannot compile:\n%s", types)
	}
	if !strings.Contains(types, "+kubebuilder:object:root=true") {
		t.Fatal("the root marker is what makes the generator emit DeepCopyObject; it must stay")
	}
}

func TestSplitAPIOperatorTypesLeaveDeepCopyToTheGenerator(t *testing.T) {
	root := t.TempDir()
	if err := GenerateOperatorFilesWithAPI(root, "example.com/app", "workspace-controller",
		"example.dev", "v1alpha1", "workspace", "Workspace"); err != nil {
		t.Fatalf("GenerateOperatorFilesWithAPI: %v", err)
	}
	types := readFile(t, filepath.Join(root, "api", "v1alpha1", "workspace", "types.go"))
	if strings.Contains(types, "DeepCopyObject()") {
		t.Fatalf("api/v1alpha1/workspace/types.go hand-declares DeepCopyObject, colliding with "+
			"the generated deepcopy:\n%s", types)
	}
}

// The co-located shape (types beside the controller, NOT under api/) is not
// covered by the deepcopy generator, so the scaffold must keep providing the
// runtime.Object method or the controller cannot register the type.
func TestColocatedOperatorTypesKeepTheirDeepCopy(t *testing.T) {
	root := t.TempDir()
	if err := GenerateOperatorFiles(root, "example.com/app", "fleet", "example.dev", "v1alpha1"); err != nil {
		t.Fatalf("GenerateOperatorFiles: %v", err)
	}
	path := filepath.Join(root, "internal", "operators", "fleet", "types.go")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected co-located types.go: %v", err)
	}
	types := readFile(t, path)
	if !strings.Contains(types, "func (in *Fleet) DeepCopyObject() runtime.Object") ||
		!strings.Contains(types, "func (in *FleetList) DeepCopyObject() runtime.Object") {
		t.Fatalf("co-located types.go lost DeepCopyObject; nothing generates it outside api/:\n%s", types)
	}
}
