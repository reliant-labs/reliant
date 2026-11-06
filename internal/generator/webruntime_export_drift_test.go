package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestTemplatesOnlyImportPublishedWebRuntimeExports is the guard that would
// have caught the v0.3.0 defect.
//
// THE GAP IT CLOSES. Every other check in this repo compares the templates
// against the LOCAL web-runtime source. fa0bf04d added installDevLogging to
// web-runtime/src AND to five templates in one commit, so every local check
// saw a consistent world — while the registry, which is what a scaffolded
// project actually installs, had never received the module at all. The only
// thing that could see the difference was a real `npm install` in the scaffold
// E2E, and the web-runtime devlink masked even that by substituting the local
// checkout. Result: a shipped forge whose scaffolds could not typecheck.
//
//	src/app/providers.tsx(5,3): error TS2305: Module
//	'"@reliantlabs/forge-web-runtime"' has no exported member
//	'installDevLogging'.
//
// So this test deliberately reaches for the PUBLISHED artifact — the same
// tarball npm would hand a user resolving webRuntimePublishedRange — and
// asserts the templates only name symbols it actually exports.
//
// NETWORK. It shells out to `npm view`/`npm pack`, needs no credentials (the
// package is public), and is skipped under -short so the inner loop stays
// fast. It is also skipped when npm is absent or the network is unreachable:
// a checkout on a plane must not fail its suite, and CI runs it in full mode
// where a real drift will surface.
func TestTemplatesOnlyImportPublishedWebRuntimeExports(t *testing.T) {
	if testing.Short() {
		t.Skip("network + npm; skipped in -short (runs in the full suite and CI)")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}

	imported := importedWebRuntimeSymbols(t)
	if len(imported) == 0 {
		t.Fatal("found no imports of the web-runtime package in the templates — " +
			"the scanner has drifted from the template layout and this guard is now inert")
	}

	exported := publishedWebRuntimeExports(t, webRuntimePublishedRange)

	var missing []string
	for _, sym := range imported {
		if !exported[sym] {
			missing = append(missing, sym)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("the scaffold templates import %d symbol(s) that the PUBLISHED "+
			"web-runtime (%s) does not export: %v\n\n"+
			"Every project scaffolded against the registry fails to typecheck with TS2305.\n"+
			"Publishing a web-runtime that exports them, and moving webRuntimePublishedRange\n"+
			"to it, is what fixes this — adding the export locally is not enough, which is\n"+
			"the exact trap this test exists to catch.",
			len(missing), webRuntimePublishedRange, missing)
	}
}

// webRuntimeImportRe matches an import from the runtime package in either
// shape the templates use: a single-line `import { a, b } from "…"` and the
// braces-across-lines form prettier produces once the list grows.
// The symbol list may span lines, but it must not swallow a neighbouring
// import: [^{}] stops the match at the closing brace of THIS clause, so a
// preceding `import { X } from "@connectrpc/connect"` cannot be absorbed into
// a later web-runtime import. (An earlier `(?s).*?` version did exactly that
// and reported `} from "@connectrpc/connect";` as an imported symbol.)
var webRuntimeImportRe = regexp.MustCompile(
	`import\s+(?:type\s+)?\{([^{}]*)\}\s*from\s*"@reliantlabs/forge-web-runtime"`)

// importedWebRuntimeSymbols returns every symbol the frontend templates import
// from the runtime, deduplicated.
//
// It reads the template tree from disk rather than the embedded FS so the set
// reflects what a maintainer just edited, which is the thing under test.
func importedWebRuntimeSymbols(t *testing.T) []string {
	t.Helper()

	seen := map[string]bool{}
	root := filepath.Join("..", "templates", "frontend")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		switch filepath.Ext(path) {
		case ".ts", ".tsx", ".tmpl":
		default:
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range webRuntimeImportRe.FindAllStringSubmatch(string(body), -1) {
			for _, raw := range strings.Split(m[1], ",") {
				if sym := normalizeImportedSymbol(raw); sym != "" {
					seen[sym] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}

	out := make([]string, 0, len(seen))
	for sym := range seen {
		out = append(out, sym)
	}
	sort.Strings(out)
	return out
}

// normalizeImportedSymbol reduces one clause of an import list to the symbol
// the runtime must export: it drops a per-clause `type` prefix and resolves
// `X as Y` to X, since X is the name being imported.
func normalizeImportedSymbol(clause string) string {
	s := strings.TrimSpace(clause)
	s = strings.TrimSpace(strings.TrimPrefix(s, "type "))
	if idx := strings.Index(s, " as "); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	// A Go template action inside the import list is not a symbol.
	if s == "" || strings.Contains(s, "{{") {
		return ""
	}
	return s
}

// publishedWebRuntimeExports downloads the version npm resolves for the given
// range and returns the symbols its dist/index.d.ts declares.
func publishedWebRuntimeExports(t *testing.T, semverRange string) map[string]bool {
	t.Helper()

	const pkg = "@reliantlabs/forge-web-runtime"
	spec := pkg + "@" + semverRange

	// Resolve the range to a concrete version first, so a failure says which
	// version was judged rather than just "something did not match".
	//
	// A 404 here is its own diagnosis and deserves better than raw npm output:
	// it means webRuntimePublishedRange was moved ahead of the publish, which
	// is the ordinary state between tagging a release and the registry
	// accepting it.
	resolved := strings.TrimSpace(runNPMAllowing404(t, spec, "view", spec, "version"))
	if resolved == "" {
		t.Fatalf("npm resolved no version for %s — webRuntimePublishedRange names a "+
			"range with nothing published in it", spec)
	}
	// `npm view <range> version` prints one line per match when the range is
	// satisfied by several; the last is the highest, which is what installs.
	lines := strings.Fields(resolved)
	version := strings.Trim(lines[len(lines)-1], "'\"")
	t.Logf("%s resolves to %s", spec, version)

	dir := t.TempDir()
	out := runNPM(t, dir, "pack", pkg+"@"+version, "--silent")
	tarball := strings.TrimSpace(out)
	if i := strings.LastIndex(tarball, "\n"); i >= 0 {
		tarball = strings.TrimSpace(tarball[i+1:])
	}

	if err := exec.Command("tar", "-xzf", filepath.Join(dir, tarball), "-C", dir).Run(); err != nil {
		t.Fatalf("extract %s: %v", tarball, err)
	}

	dts := filepath.Join(dir, "package", "dist", "index.d.ts")
	body, err := os.ReadFile(dts)
	if err != nil {
		t.Fatalf("published tarball has no dist/index.d.ts (%v) — the build emitted "+
			"nothing usable and no consumer can typecheck against it", err)
	}
	return parseExportedSymbols(string(body))
}

// exportClauseRe matches the re-export lists a rolled-up index.d.ts is made
// of: `export { a, b } from "./x.js"` and `export type { C } from "./y.js"`.
var exportClauseRe = regexp.MustCompile(`(?s)export\s+(?:type\s+)?\{(.*?)\}`)

// exportDeclRe matches a direct declaration form, e.g. `export declare function
// installDevLogging(...)` or `export interface Foo`.
var exportDeclRe = regexp.MustCompile(
	`export\s+(?:declare\s+)?(?:async\s+)?(?:function|const|let|var|class|interface|type|enum)\s+([A-Za-z_$][\w$]*)`)

// parseExportedSymbols collects the names a .d.ts makes available to importers.
// Both forms are needed: the bundler emits re-export lists for most modules and
// direct declarations for others, and a symbol present in either is importable.
func parseExportedSymbols(dts string) map[string]bool {
	out := map[string]bool{}
	for _, m := range exportClauseRe.FindAllStringSubmatch(dts, -1) {
		for _, raw := range strings.Split(m[1], ",") {
			if sym := normalizeImportedSymbol(raw); sym != "" {
				out[sym] = true
			}
		}
	}
	for _, m := range exportDeclRe.FindAllStringSubmatch(dts, -1) {
		out[m[1]] = true
	}
	return out
}

// runNPMAllowing404 wraps runNPM so a "no such published version" answer fails
// with the reason rather than eight lines of npm 404 boilerplate.
func runNPMAllowing404(t *testing.T, spec string, args ...string) string {
	t.Helper()
	cmd := exec.Command("npm", args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out)
	}
	text := string(out)
	if strings.Contains(text, "404") || strings.Contains(text, "No match found") {
		t.Fatalf("%s is not on the registry.\n\n"+
			"webRuntimePublishedRange names a range npm cannot resolve, so every scaffolded\n"+
			"project fails `npm install` with ETARGET. Either the publish has not happened\n"+
			"yet (tag pushed, registry not updated), or the range was moved too early.",
			spec)
	}
	// Not a 404 — hand it back to the shared path for the offline check.
	return runNPM(t, ".", args...)
}

// runNPM executes an npm subcommand, skipping the test when the failure looks
// like an unreachable network rather than a real drift.
func runNPM(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("npm", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := string(out)
		for _, offline := range []string{"ENOTFOUND", "EAI_AGAIN", "ETIMEDOUT", "ECONNREFUSED", "network"} {
			if strings.Contains(text, offline) {
				t.Skipf("npm %s: registry unreachable (%s); skipping", strings.Join(args, " "), offline)
			}
		}
		t.Fatalf("npm %s failed: %v\n%s", strings.Join(args, " "), err, text)
	}
	return string(out)
}

// TestParseExportedSymbols covers the .d.ts shapes the guard must understand.
// A parser that silently matched nothing would make the drift check pass for
// every symbol, so this pins it independently of the network.
func TestParseExportedSymbols(t *testing.T) {
	t.Parallel()
	got := parseExportedSymbols(`
export { installDevLogging, uninstallDevLogging } from "./devlog.js";
export type { DevLoggingOptions } from "./devlog.js";
export declare function buildRuntimeInterceptors(o: unknown): void;
export interface RuntimeToastSink { push(): void }
`)
	for _, want := range []string{
		"installDevLogging", "uninstallDevLogging", "DevLoggingOptions",
		"buildRuntimeInterceptors", "RuntimeToastSink",
	} {
		if !got[want] {
			t.Errorf("parseExportedSymbols did not find %q; got %v", want, sortedSymbolNames(got))
		}
	}
}

func sortedSymbolNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
