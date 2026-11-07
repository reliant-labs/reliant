package generator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// GenerateOperatorFiles generates all files for a single operator:
//   - internal/operators/<package>/types.go           (from operator/types.go.tmpl)
//   - internal/operators/<package>/controller.go      (from operator/controller.go.tmpl)
//   - internal/operators/<package>/controller_test.go (from operator/controller_test.go.tmpl)
//
// The CLI/display name (which may contain hyphens) is translated to a
// Go-package-safe form for the directory and `package` declaration.
//
// Both the "new project" and "scaffold operator" flows delegate here so the
// generated output is always identical.
func GenerateOperatorFiles(root, modulePath, name, group, version string) error {
	return GenerateOperatorFilesWithAPI(root, modulePath, name, group, version, "", "")
}

// GenerateOperatorFilesWithAPI is GenerateOperatorFiles with explicit overrides
// for the CRD type's Go package name and type name. This is the path used when
// the operator name and CRD type diverge — e.g. a `workspace-controller`
// operator reconciling a `Workspace` CRD. When apiPackage and crdType are both
// empty this behaves identically to GenerateOperatorFiles.
//
// apiPackage selects the Go-package directory + `package` declaration for the
// CRD types. When empty, the CRD types live in the operator's own package
// (the original shape). When set, types are written to
// api/<version>/<apiPackage>/types.go with `package <apiPackage>`, and the
// controller imports them from there. crdType selects the Go type name; when
// empty it defaults to PascalCase(apiPackage) when apiPackage is set, or
// PascalCase(name) otherwise (preserving original behaviour).
func GenerateOperatorFilesWithAPI(root, modulePath, name, group, version, apiPackage, crdType string) error {
	operatorPackage := naming.ServicePackage(name)
	operatorDir := filepath.Join(root, "internal", "operators", operatorPackage)

	if err := os.MkdirAll(operatorDir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", operatorDir, err)
	}

	// Resolve CRD type name. Default to PascalCase of apiPackage when set,
	// else PascalCase of the operator name (the original shape).
	resolvedCRDType := crdType
	if resolvedCRDType == "" {
		if apiPackage != "" {
			resolvedCRDType = naming.ToPascalCase(apiPackage)
		} else {
			resolvedCRDType = naming.ToPascalCase(name)
		}
	}

	// When the user passed --api-package, types.go lives in a separate
	// `api/<version>/<apiPackage>/` package and the controller imports it.
	// Otherwise, types.go lives in the operator's own package (the original
	// single-package shape).
	splitAPI := apiPackage != ""
	apiPackageSlug := ""
	apiPackageDir := operatorDir
	apiPackageDecl := operatorPackage
	apiImportPath := ""
	apiTypeRef := resolvedCRDType
	if splitAPI {
		apiPackageSlug = naming.ServicePackage(apiPackage)
		apiPackageDir = filepath.Join(root, "api", version, apiPackageSlug)
		apiPackageDecl = apiPackageSlug
		apiImportPath = fmt.Sprintf("%s/api/%s/%s", modulePath, version, apiPackageSlug)
		apiTypeRef = apiPackageSlug + "." + resolvedCRDType
		if err := os.MkdirAll(apiPackageDir, 0755); err != nil {
			return fmt.Errorf("create directory %s: %w", apiPackageDir, err)
		}
	}

	data := struct {
		Name          string // display form, may contain hyphens
		Package       string // package declaration for the file being rendered
		TypeName      string // bare CRD type name (e.g. "Workspace")
		TypeRef       string // qualified type ref usable from the controller package
		Group         string
		Version       string
		Module        string
		APIImportPath string // empty when CRD lives in the operator package
		SplitAPI      bool   // true when --api-package was passed
	}{
		Name:          name,
		Package:       operatorPackage, // controller-side package
		TypeName:      resolvedCRDType,
		TypeRef:       apiTypeRef,
		Group:         group,
		Version:       version,
		Module:        modulePath,
		APIImportPath: apiImportPath,
		SplitAPI:      splitAPI,
	}
	// The api-side render uses a different package declaration.
	apiData := data
	apiData.Package = apiPackageDecl

	// -- types.go (via operator/types.go.tmpl) --
	// When --api-package is set, types.go is written to api/<version>/<pkg>/.
	// Otherwise it lives alongside the controller (legacy shape).
	typesContent, err := renderOperatorTemplate("operator/types.go.tmpl", apiData)
	if err != nil {
		return fmt.Errorf("render types.go: %w", err)
	}
	if err := atomicfile.WriteFile(filepath.Join(apiPackageDir, "types.go"), typesContent, 0644); err != nil {
		return err
	}

	// -- controller.go (via operator/controller.go.tmpl) --
	controllerContent, err := renderOperatorTemplate("operator/controller.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render controller.go: %w", err)
	}
	if err := atomicfile.WriteFile(filepath.Join(operatorDir, "controller.go"), controllerContent, 0644); err != nil {
		return err
	}

	// -- controller_test.go (via operator/controller_test.go.tmpl) --
	testContent, err := renderOperatorTemplate("operator/controller_test.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render controller_test.go: %w", err)
	}
	if err := atomicfile.WriteFile(filepath.Join(operatorDir, "controller_test.go"), testContent, 0644); err != nil {
		return err
	}

	return nil
}

// renderOperatorTemplate renders an operator template from the embedded FS.
func renderOperatorTemplate(name string, data interface{}) ([]byte, error) {
	engine, err := getTemplateEngine()
	if err != nil {
		return nil, err
	}
	result, err := engine.RenderTemplate(name, data)
	if err != nil {
		return nil, err
	}
	return []byte(result), nil
}
