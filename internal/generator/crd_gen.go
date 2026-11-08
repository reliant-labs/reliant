package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// CRDShape is the reconciler scaffold style chosen at scaffold time.
//
// state-machine — the CRD has a Spec.State field that drives the
// reconcile loop through observable phases (Pending → Running →
// Suspended → Failed). Generated controller carries reconcileRunning /
// reconcileSuspended / reconcileDeleting helpers and a switch over
// Spec.State.
//
// config — declarative-only CRD with no observable state. The
// reconciler's job is to project Spec into a runtime store. Generated
// controller has a single ReconcileSpec.
//
// composite — the CRD owns a set of sub-resources whose lifetime is
// coupled to the parent. Generated controller has a ReconcileSpec
// that's structured as a series of ensure* helpers.
type CRDShape string

const (
	CRDShapeStateMachine CRDShape = "state-machine"
	CRDShapeConfig       CRDShape = "config"
	CRDShapeComposite    CRDShape = "composite"
)

// IsValid returns true if s is one of the recognised CRD shapes.
func (s CRDShape) IsValid() bool {
	switch s {
	case CRDShapeStateMachine, CRDShapeConfig, CRDShapeComposite:
		return true
	}
	return false
}

// CRDGenInput collects the parameters needed to scaffold a CRD into
// an existing operator.
type CRDGenInput struct {
	Root         string   // project root
	ModulePath   string   // go module path (e.g. "github.com/example/opt")
	OperatorName string   // target operator (display form, may have hyphens)
	TypeName     string   // CRD type name (PascalCase, e.g. "Workspace")
	Group        string   // API group (e.g. "reliant.dev")
	Version      string   // API version (e.g. "v1alpha1")
	Shape        CRDShape // reconciler scaffold style
}

// GenerateCRDFiles scaffolds a single CRD into an existing operator:
//
//   - api/<version>/<lower-name>_types.go    — CRD spec/status types
//   - internal/operators/<operator>/<lower-name>_controller.go    — thin shim
//   - internal/operators/<operator>/<lower-name>_controller_test.go — fake-client unit test
//
// The operator must already exist; generation is idempotent in the
// sense that re-running with the same inputs overwrites the three
// generated files (no merge), but it does NOT rebuild the operator's
// types.go (placeholder CRD or otherwise).
func GenerateCRDFiles(in CRDGenInput) error {
	if !in.Shape.IsValid() {
		return fmt.Errorf("invalid CRD shape %q (valid: state-machine, config, composite)", in.Shape)
	}
	if in.TypeName == "" {
		return fmt.Errorf("TypeName is required")
	}
	if in.OperatorName == "" {
		return fmt.Errorf("OperatorName is required")
	}
	if in.Group == "" {
		return fmt.Errorf("Group is required")
	}
	if in.Version == "" {
		return fmt.Errorf("Version is required")
	}
	if in.ModulePath == "" {
		return fmt.Errorf("ModulePath is required")
	}

	operatorPackage := naming.ServicePackage(in.OperatorName)
	operatorDir := filepath.Join(in.Root, "internal", "operators", operatorPackage)
	if _, err := os.Stat(operatorDir); err != nil {
		return fmt.Errorf("operator %q not found at %s: run `forge scaffold operator %s` first", in.OperatorName, operatorDir, in.OperatorName)
	}

	apiPackage := apiPackageName(in.Version)
	apiDir := filepath.Join(in.Root, "api", in.Version)
	if err := os.MkdirAll(apiDir, 0755); err != nil {
		return fmt.Errorf("create api directory: %w", err)
	}

	typeName := naming.ToPascalCase(in.TypeName)
	lowerName := naming.ToSnakeCase(typeName)
	controllerType := typeName + "Controller"
	pluralLower := strings.ToLower(naming.Pluralize(typeName))
	finalizerConst := lowerName + "Finalizer"

	data := struct {
		Module          string
		OperatorName    string
		OperatorPackage string
		APIPackage      string
		TypeName        string
		LowerName       string
		PluralLower     string
		Group           string
		Version         string
		Shape           string
		ControllerType  string
		FinalizerConst  string
	}{
		Module:          in.ModulePath,
		OperatorName:    in.OperatorName,
		OperatorPackage: operatorPackage,
		APIPackage:      apiPackage,
		TypeName:        typeName,
		LowerName:       lowerName,
		PluralLower:     pluralLower,
		Group:           in.Group,
		Version:         in.Version,
		Shape:           string(in.Shape),
		ControllerType:  controllerType,
		FinalizerConst:  finalizerConst,
	}

	// groupversion.go (written once per api/<version> package — first
	// CRD to land creates it; subsequent CRDs leave it alone). The
	// shared GroupVersion + SchemeBuilder live here; per-type files
	// register their AddKnownTypes via init().
	gvPath := filepath.Join(apiDir, "groupversion.go")
	if _, err := os.Stat(gvPath); os.IsNotExist(err) {
		gvContent, err := renderOperatorTemplate("crd/groupversion.go.tmpl", data)
		if err != nil {
			return fmt.Errorf("render crd groupversion.go: %w", err)
		}
		if err := atomicfile.WriteFile(gvPath, gvContent, 0644); err != nil {
			return fmt.Errorf("write %s: %w", gvPath, err)
		}
	}

	// types.go (one per CRD; we emit a unique filename so multiple
	// CRDs can coexist without overwriting each other).
	typesContent, err := renderOperatorTemplate("crd/types.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render crd types.go: %w", err)
	}
	typesPath := filepath.Join(apiDir, lowerName+"_types.go")
	if err := atomicfile.WriteFile(typesPath, typesContent, 0644); err != nil {
		return fmt.Errorf("write %s: %w", typesPath, err)
	}

	// controller.go shim
	controllerContent, err := renderOperatorTemplate("crd/controller.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render crd controller.go: %w", err)
	}
	controllerPath := filepath.Join(operatorDir, lowerName+"_controller.go")
	if err := atomicfile.WriteFile(controllerPath, controllerContent, 0644); err != nil {
		return fmt.Errorf("write %s: %w", controllerPath, err)
	}

	// controller_test.go
	testContent, err := renderOperatorTemplate("crd/controller_test.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render crd controller_test.go: %w", err)
	}
	testPath := filepath.Join(operatorDir, lowerName+"_controller_test.go")
	if err := atomicfile.WriteFile(testPath, testContent, 0644); err != nil {
		return fmt.Errorf("write %s: %w", testPath, err)
	}

	// deploy/kcl/lib/<lower>_crd.k — a starter KCL CRD manifest. The
	// scaffold ships with a permissive `x-kubernetes-preserve-unknown-fields`
	// schema so it installs immediately; the Go types in
	// api/<version>/<lower>_types.go stay the source of truth and
	// users can tighten the schema (or wire controller-gen) when
	// the CRD shape stabilises. Only emit when the KCL deploy tree
	// exists (every forge service project has one; CLI-only projects
	// might not).
	kclLibDir := filepath.Join(in.Root, "deploy", "kcl", "lib")
	kclDeployDir := filepath.Join(in.Root, "deploy", "kcl")
	if _, err := os.Stat(kclDeployDir); err == nil {
		if err := os.MkdirAll(kclLibDir, 0755); err != nil {
			return fmt.Errorf("create deploy/kcl/lib directory: %w", err)
		}
		crdKclContent, err := renderOperatorTemplate("crd/crd.k.tmpl", data)
		if err != nil {
			return fmt.Errorf("render crd crd.k: %w", err)
		}
		kclPath := filepath.Join(kclLibDir, lowerName+"_crd.k")
		// Don't overwrite a hand-extended CRD KCL on re-run.
		if _, err := os.Stat(kclPath); os.IsNotExist(err) {
			if err := atomicfile.WriteFile(kclPath, crdKclContent, 0644); err != nil {
				return fmt.Errorf("write %s: %w", kclPath, err)
			}
		}
	}

	return nil
}

// apiPackageName returns the Go package name for an api/<version>
// directory. We strip non-letter chars so "v1alpha1" stays as-is and
// "v1" becomes "v1" (both valid Go identifiers).
func apiPackageName(version string) string {
	out := strings.ToLower(version)
	// Replace any non-alphanumeric runes with empty string to keep
	// the package name a valid Go identifier.
	var b strings.Builder
	for _, r := range out {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
