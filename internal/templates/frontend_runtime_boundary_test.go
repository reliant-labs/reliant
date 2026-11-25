// frontend_runtime_boundary_test.go — the scaffold may not re-declare what
// @reliantlabs/forge-web-runtime already exports.
//
// Born red on `stripServerFraming`, which existed TWICE: once in
// web-runtime/src/errors.ts and once in the scaffold's
// src/lib/format-utils.ts, each carrying a comment promising to keep the
// other in sync. The scaffold copy is written once and never overwritten, so
// the promise was unkeepable in one direction: a correction to the framing
// regex could only ever reach projects generated AFTER it, and every project
// already shipped kept the old behaviour forever.
//
// The rule this guard enforces: when a symbol is owned by the package, the
// scaffold CONSUMES it. It does not carry a second implementation, and it
// does not carry a re-export shim either — a shim is still a scaffold-once
// file holding forge's wiring, so it drifts exactly like the copy did.
package templates

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// runtimeBarrelExport matches a name in an `export { ... } from "./x"` block
// of the web-runtime barrel. Type-only entries carry a `type ` prefix.
var runtimeBarrelExport = regexp.MustCompile(`^\s*(?:type\s+)?([A-Za-z_$][\w$]*)\s*,?\s*$`)

// scaffoldDeclaration matches a top-level exported declaration in a frontend
// template: `export function foo`, `export const FOO`, `export class Foo`,
// `export interface Foo`, `export type Foo`.
var scaffoldDeclaration = regexp.MustCompile(`(?m)^export\s+(?:async\s+)?(?:function|const|let|var|class|interface|type|enum)\s+([A-Za-z_$][\w$]*)`)

// repoRoot is two levels above this package's source directory, which is the
// working directory `go test` runs it in.
//
// Not runtime.Caller: under -trimpath (the workspace image's GOFLAGS default)
// that reports "github.com/reliant-labs/forge/internal/...", a module-relative
// path that is not a file on disk. internal/trimpathguard holds the tree to it.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve the forge checkout root: %v", err)
	}
	return root
}

// webRuntimeExports reads the names the package's public barrel exports.
func webRuntimeExports(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join(repoRoot(t), "web-runtime", "src", "index.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	out := map[string]bool{}
	inBlock := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "export {"):
			inBlock = true
			continue
		case inBlock && strings.HasPrefix(trimmed, "}"):
			inBlock = false
			continue
		case !inBlock:
			// Single-line form: `export { Resource } from "./resource";`
			continue
		}
		if m := runtimeBarrelExport.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s: parsed zero exports — the barrel's shape changed and this guard is now blind", path)
	}
	return out
}

// TestScaffoldDoesNotRedeclareRuntimeExports walks every frontend template
// and fails on any top-level declaration whose name the runtime package
// already exports.
func TestScaffoldDoesNotRedeclareRuntimeExports(t *testing.T) {
	t.Parallel()

	owned := webRuntimeExports(t)

	// Every root the frontend tree can compose from, plus the page emitters.
	roots := []string{
		"shared", "shared-web", "nextjs", "vite-spa", "react-native",
		"pages", "vite-spa-pages", "mocks",
	}
	for _, root := range roots {
		files, err := listTemplates(filepath.Join("frontend", root), true)
		if err != nil {
			t.Fatalf("list %s: %v", root, err)
		}
		for _, rel := range files {
			ext := filepath.Ext(strings.TrimSuffix(rel, ".tmpl"))
			if ext != ".ts" && ext != ".tsx" {
				continue
			}
			raw, err := FrontendTemplates().Get(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("read %s/%s: %v", root, rel, err)
			}
			for _, m := range scaffoldDeclaration.FindAllStringSubmatch(string(raw), -1) {
				if !owned[m[1]] {
					continue
				}
				t.Errorf("%s/%s declares %q, which @reliantlabs/forge-web-runtime already exports.\n"+
					"A scaffold-once file is written once and never overwritten, so a second copy of a package symbol "+
					"can never be corrected in a project that has already shipped. Import it from the package instead — "+
					"and do NOT leave a re-export shim behind, because a shim drifts for the same reason.",
					root, rel, m[1])
			}
		}
	}
}

// TestRuntimeOwnsTheErrorFramingContract is the named regression for the
// duplicate this guard was born on: the backend-framing strippers live in
// the package, and the scaffold's format-utils no longer carries them.
func TestRuntimeOwnsTheErrorFramingContract(t *testing.T) {
	t.Parallel()

	owned := webRuntimeExports(t)
	for _, name := range []string{"stripServerFraming", "userMessage"} {
		if !owned[name] {
			t.Errorf("@reliantlabs/forge-web-runtime no longer exports %q — the scaffold's pages import it from there", name)
		}
	}

	fu, err := FrontendTemplates().Get(filepath.Join("shared", "src", "lib", "format-utils.ts"))
	if err != nil {
		t.Fatalf("read format-utils: %v", err)
	}
	// The framing REGEX, not the word — the file's header comment names
	// forge/pkg/svcerr on purpose, to say where the contract went.
	if strings.Contains(string(fu), `svcerr:\s*`) {
		t.Error("shared/src/lib/format-utils.ts carries the `svcerr:` stripping regex again — " +
			"that contract belongs to @reliantlabs/forge-web-runtime, in ONE implementation")
	}

	// The pages that SHOW an error must reach the package for the copy.
	for _, rel := range []string{
		filepath.Join("pages", "create-page.tsx.tmpl"),
		filepath.Join("pages", "detail-page.tsx.tmpl"),
		filepath.Join("pages", "edit-page.tsx.tmpl"),
		filepath.Join("vite-spa-pages", "create-page.tsx.tmpl"),
		filepath.Join("vite-spa-pages", "detail-page.tsx.tmpl"),
		filepath.Join("vite-spa-pages", "edit-page.tsx.tmpl"),
	} {
		raw, err := FrontendTemplates().Get(rel)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(raw), `import { userMessage } from "@reliantlabs/forge-web-runtime";`) {
			t.Errorf("%s does not import userMessage from @reliantlabs/forge-web-runtime", rel)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, `from "@/lib/format-utils"`) && strings.Contains(line, "userMessage") {
				t.Errorf("%s still sources userMessage from the scaffold:\n  %s", rel, strings.TrimSpace(line))
			}
		}
	}
}
