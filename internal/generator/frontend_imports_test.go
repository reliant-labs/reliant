package generator

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestFrontendScaffoldImportsResolve is the guard the frontend scaffolds never
// had: every module a scaffolded file imports must resolve to either a file
// the SAME scaffold emitted or a dependency the scaffold's own package.json
// declares.
//
// Three shipped bugs are exactly this invariant broken, and all three survived
// because the tests around them asserted on template STRINGS rather than on a
// scaffold that resolves:
//
//  1. src/lib/search-schemas.ts imported `zod`; the React Native package.json
//     never declared it (fixed f98fe434).
//  2. @reliantlabs/forge-web-runtime declares @opentelemetry/api as a peer; the
//     React Native package.json never declared it (fixed d11f0f5b).
//  3. src/lib/connect.ts references "@/lib/mock-transport" — a module only
//     `forge generate` emitted — so `forge scaffold frontend` handed back a
//     vite-spa tree that failed `tsc` (TS2307) and a Next.js tree that failed
//     `next build` ("Module not found"), with no generate pass behind the verb
//     to fill the gap.
//
// This runs against GenerateFrontendFilesWithOptions, the single scaffold
// entry point behind BOTH `forge project new --frontend` and `forge project
// scaffold frontend`, so a fix that only lands on one of those paths does not pass.
//
// It costs milliseconds and needs no node, npm, or network — the e2e tests
// that actually run tsc/next build/expo export are `-tags e2e` and minutes
// apiece. This is the one that runs on every `go test ./...`.
func TestFrontendScaffoldImportsResolve(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"", "vite-spa", "mobile"} {
		label := kind
		if label == "" {
			label = "web(default)"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			if err := GenerateFrontendFilesWithOptions(
				root, "example.com/app", "app", "fe", 8080, kind, FrontendGenOptions{},
			); err != nil {
				t.Fatalf("GenerateFrontendFilesWithOptions(kind=%q): %v", kind, err)
			}
			feDir := filepath.Join(root, "frontends", "fe")

			declared := declaredPackages(t, filepath.Join(feDir, "package.json"))

			for _, src := range scaffoldSourceFiles(t, feDir) {
				body, err := os.ReadFile(src)
				if err != nil {
					t.Fatalf("read %s: %v", src, err)
				}
				rel, _ := filepath.Rel(feDir, src)

				for _, spec := range moduleSpecifiers(string(body)) {
					switch {
					case strings.HasPrefix(spec, "."):
						target := filepath.Join(filepath.Dir(src), spec)
						if !resolvesToFile(target) {
							t.Errorf("%s imports %q — the scaffold emits no such file (%s)",
								rel, spec, mustRel(feDir, target))
						}
					case strings.HasPrefix(spec, "@/"):
						// Every kind maps "@/*" → "./src/*" in tsconfig.json.
						target := filepath.Join(feDir, "src", strings.TrimPrefix(spec, "@/"))
						if !resolvesToFile(target) {
							t.Errorf("%s imports %q — the scaffold emits no such file (%s). "+
								"A module only `forge generate` writes leaves `forge scaffold frontend` "+
								"handing back a tree that does not typecheck.",
								rel, spec, mustRel(feDir, target))
						}
					default:
						pkg := packageNameOf(spec)
						if isNodeBuiltin(pkg) {
							continue
						}
						if !declared[pkg] {
							t.Errorf("%s imports %q — package %q is not declared in the scaffold's package.json. "+
								"It resolves today only by accident of hoisting; declare it.",
								rel, spec, pkg)
						}
					}
				}
			}
		})
	}
}

// scaffoldSourceFiles lists every JS/TS source file the scaffold emitted.
func scaffoldSourceFiles(t *testing.T, feDir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(feDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", feDir, err)
	}
	if len(out) == 0 {
		t.Fatalf("no source files emitted under %s", feDir)
	}
	sort.Strings(out)
	return out
}

// declaredPackages reads the package names a package.json declares across
// dependencies / devDependencies / peerDependencies.
func declaredPackages(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read package.json: %v", err)
	}
	var pkg struct {
		Dependencies     map[string]string `json:"dependencies"`
		DevDependencies  map[string]string `json:"devDependencies"`
		PeerDependencies map[string]string `json:"peerDependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, group := range []map[string]string{pkg.Dependencies, pkg.DevDependencies, pkg.PeerDependencies} {
		for name := range group {
			out[name] = true
		}
	}
	return out
}

// Module-specifier extraction. Comment LINES are dropped first — the
// scaffolded files carry JSDoc usage examples ("* import { UserService } from
// '../gen/...'") that name modules the scaffold deliberately does not emit.
// The remaining forms are static import/export-from, side-effect imports, and
// literal require()/import() calls (webpack and Metro both resolve those
// statically, which is why the Next.js mock-transport require() failed the
// build even though tsc was happy with it).
var (
	importFromRe  = regexp.MustCompile(`(?m)^[ \t]*(?:import|export)\b(?s:.{0,400}?)\bfrom[ \t]*["']([^"']+)["']`)
	sideEffectRe  = regexp.MustCompile(`(?m)^[ \t]*import[ \t]*["']([^"']+)["']`)
	requireCallRe = regexp.MustCompile(`\b(?:require|import)\([ \t]*["']([^"']+)["'][ \t]*\)`)
)

func moduleSpecifiers(body string) []string {
	code := dropCommentLines(body)

	seen := map[string]bool{}
	var out []string
	for _, re := range []*regexp.Regexp{importFromRe, sideEffectRe, requireCallRe} {
		for _, m := range re.FindAllStringSubmatch(code, -1) {
			if spec := m[1]; !seen[spec] {
				seen[spec] = true
				out = append(out, spec)
			}
		}
	}
	sort.Strings(out)
	return out
}

// dropCommentLines blanks every line that is wholly a comment. It is a
// heuristic, not a JS parser: a real import can never sit on such a line, so
// the worst case is a missed specifier (a quieter test), never a fabricated
// one (a false failure).
func dropCommentLines(body string) string {
	lines := strings.Split(body, "\n")
	inBlock := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case inBlock:
			if strings.Contains(trimmed, "*/") {
				inBlock = false
			}
			lines[i] = ""
		case strings.HasPrefix(trimmed, "//"):
			lines[i] = ""
		case strings.HasPrefix(trimmed, "/*"):
			if !strings.Contains(trimmed, "*/") {
				inBlock = true
			}
			lines[i] = ""
		case strings.HasPrefix(trimmed, "*"):
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// packageNameOf reduces a bare specifier to its package name:
// "expo/metro-config" → "expo", "@reliantlabs/forge-web-runtime/interceptors" →
// "@reliantlabs/forge-web-runtime".
func packageNameOf(spec string) string {
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") && len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

func isNodeBuiltin(pkg string) bool {
	if strings.HasPrefix(pkg, "node:") {
		return true
	}
	switch pkg {
	case "assert", "buffer", "child_process", "crypto", "events", "fs", "http",
		"https", "os", "path", "process", "stream", "url", "util", "zlib":
		return true
	}
	return false
}

// resolvesToFile applies the bundler-style resolution every one of these
// trees uses: the literal path, then the TS/JS extension candidates, then a
// directory index.
func resolvesToFile(target string) bool {
	if isFile(target) {
		return true
	}
	for _, ext := range []string{".ts", ".tsx", ".d.ts", ".js", ".jsx", ".mjs", ".cjs", ".json", ".css"} {
		if isFile(target + ext) {
			return true
		}
	}
	for _, idx := range []string{"index.ts", "index.tsx", "index.js", "index.jsx"} {
		if isFile(filepath.Join(target, idx)) {
			return true
		}
	}
	return false
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func mustRel(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return fmt.Sprintf("frontends/fe/%s", rel)
}
