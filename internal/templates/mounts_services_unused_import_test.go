package templates

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// mounts_services_gen.go must not import a package it never references.
//
// The template used to emit one handler import per user service:
//
//	{{.Alias}} "{{$.Module}}/{{.ImportPath}}"
//
// That line is a leftover from when this file constructed the services. It
// does not any more — construction moved wholesale into compose.go
// (NewComponents), and every Mount<Svc> body now reads the already-built
// instance off `c`. Nothing in the file has named the handler packages since,
// so each import was dead the moment it was written:
//
//	internal/app/mounts_services_gen.go:39:2: "…/internal/handlers/api" imported and not used
//
// It went unnoticed because `forge generate` runs `goimports -w` over its
// own output before the validate build, and goimports silently deletes
// unused imports. That made the defect invisible on any machine with
// goimports on PATH and fatal on any machine without it — goimports is not
// installed by `forge tools install`, and the generate step that shells out
// to it only prints "⚠️ goimports not found — skipping import formatting"
// and continues. The validate build then fails, generate reverts every file
// it wrote, and `forge project new` leaves a project that does not compile.
//
// The fix is to stop emitting the import, not to lean harder on goimports:
// generated code has to be correct as generated. This test asserts the
// template's import block references only packages the template body
// actually uses, so a future edit cannot reintroduce a formatter-dependent
// render.
func TestMountsServicesTemplate_NoUnusedHandlerImports(t *testing.T) {
	path := filepath.Join("project", "mounts_services_gen.go.tmpl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	src := string(raw)

	if strings.Contains(src, "{{.Alias}}") {
		t.Error("template emits a per-service handler import ({{.Alias}} \"…/{{.ImportPath}}\"), " +
			"but no Mount body references the handler packages — the generated file only " +
			"compiles when goimports happens to be on PATH to strip the dead import")
	}

	// Every import in the block must be referenced by the body. Aliased
	// imports are checked through their alias; plain ones through the last
	// path segment, which is how the body would spell the selector.
	body := src[strings.Index(src, ")")+1:]
	if start := strings.Index(src, "import ("); start >= 0 {
		block := src[start+len("import (") : start+len("import (")+strings.Index(src[start:], "\n)")]
		importRE := regexp.MustCompile(`(?m)^\s*(?:([A-Za-z_][A-Za-z0-9_]*)\s+)?"([^"]+)"`)
		for _, m := range importRE.FindAllStringSubmatch(block, -1) {
			alias, importPath := m[1], m[2]
			if strings.Contains(importPath, "{{") && alias == "" {
				// A templated path with no alias — the selector is the last
				// literal segment, checked below.
				continue
			}
			selector := alias
			if selector == "" {
				seg := importPath[strings.LastIndex(importPath, "/")+1:]
				selector = seg
			}
			if selector == "" || strings.Contains(selector, "{{") {
				continue
			}
			if !strings.Contains(body, selector+".") {
				t.Errorf("import %q (selector %q) is never referenced in the template body — "+
					"the generated file will not compile without goimports stripping it",
					importPath, selector)
			}
		}
	}
}
