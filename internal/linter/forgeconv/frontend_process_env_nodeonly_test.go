package forgeconv

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// TestLintFrontendProcessEnv_NodeOnlyBuildFilesAreExempt is the reported
// case (reliant's frontends/web): a Vite plugin that only vite.config.ts
// imports, its own helper, and a root scripts/ directory of Node-run
// Playwright drivers all run in Node, so no bundler inlines their reads.
func TestLintFrontendProcessEnv_NodeOnlyBuildFilesAreExempt(t *testing.T) {
	t.Parallel()
	root, feDir := newFrontendWithConfig(t)
	writeFE(t, feDir, "vite.config.ts",
		`import { defineConfig } from "vite";
import demoBundle from "./vite-plugin-demo-bundle";
export default defineConfig({ plugins: [demoBundle()] });
`)
	writeFE(t, feDir, "vite-plugin-demo-bundle.ts",
		`import { report } from "./plugins/report.js";
export default function demoBundle() { return report(process.env.DEMO_BUNDLE_REPORT); }
`)
	writeFE(t, feDir, "plugins/report.ts", `export function report(x?: string) { return process.env.REPORT_DIR ?? x; }`)
	writeFE(t, feDir, "scripts/demo/smoke-demo-site.mjs",
		`import { helper } from "../lib/helper.mjs";
const base = process.env.DEMO_BASE_URL ?? "http://localhost:3000";
helper(base);
`)
	writeFE(t, feDir, "scripts/lib/helper.mjs", `export const helper = (b) => process.env.CI ? b : b;`)
	// An app entry that imports nothing Node-only keeps the graph honest.
	writeFE(t, feDir, "src/main.tsx", `import "./app";`)
	writeFE(t, feDir, "src/app.tsx", `export const App = 1;`)

	res := LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
	if len(res.Findings) != 0 {
		t.Fatalf("Node-only build files must produce NO findings, got %d:\n%s",
			len(res.Findings), res.FormatText())
	}
}

// TestLintFrontendProcessEnv_BundledImportOfBuildFileIsScanned pins the
// other half: a module reachable from build config is still scanned when the
// app imports it too, because then the bundler ships it.
func TestLintFrontendProcessEnv_BundledImportOfBuildFileIsScanned(t *testing.T) {
	t.Parallel()
	root, feDir := newFrontendWithConfig(t)
	writeFE(t, feDir, "vite.config.ts", `import { origin } from "./shared/origin";
export default { server: { proxy: origin() } };
`)
	writeFE(t, feDir, "shared/origin.ts", `export const origin = () => process.env.SHARED_ORIGIN;`)
	writeFE(t, feDir, "scripts/flags.ts", `export const flag = process.env.SCRIPT_FLAG;`)
	writeFE(t, feDir, "src/main.tsx", `import { origin } from "../shared/origin";
import { flag } from "../scripts/flags";
console.log(origin(), flag);
`)
	// Under src/ reachability never exempts, even when only config imports it.
	writeFE(t, feDir, "src/lib/only-config.ts", `export const v = process.env.SRC_VALUE;`)
	writeFE(t, feDir, "vitest.config.ts", `import { v } from "./src/lib/only-config";
export default { v };
`)

	res := LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
	got := map[string]bool{}
	for _, f := range res.Findings {
		got[f.File[strings.LastIndex(f.File, "web/")+len("web/"):]] = true
	}
	for _, want := range []string{"shared/origin.ts", "scripts/flags.ts", "src/lib/only-config.ts"} {
		if !got[want] {
			t.Errorf("%s is bundled (or under src/) and must still be scanned; findings:\n%s", want, res.FormatText())
		}
	}
	if len(res.Findings) != 3 {
		t.Errorf("expected 3 findings, got %d:\n%s", len(res.Findings), res.FormatText())
	}
}

// TestLintFrontendProcessEnv_TestImportDoesNotBundle — a vitest file that
// imports a Vite plugin to test it is not app code, so it does not pull the
// plugin into the bundled set.
func TestLintFrontendProcessEnv_TestImportDoesNotBundle(t *testing.T) {
	t.Parallel()
	root, feDir := newFrontendWithConfig(t)
	writeFE(t, feDir, "vite.config.ts", `import p from "./plugins/devlog";
export default { plugins: [p()] };
`)
	writeFE(t, feDir, "plugins/devlog.ts", `export default () => process.env.LOG_DIR;`)
	writeFE(t, feDir, "plugins/devlog.test.ts", `import p from "./devlog";
p();
`)

	res := LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
	if len(res.Findings) != 0 {
		t.Fatalf("a plugin imported only by config and its test must produce NO findings, got:\n%s", res.FormatText())
	}
}

// TestLintFrontendProcessEnv_UnresolvedAppImportsFailClosed: what the
// resolver cannot follow from the app's side never exempts a file. An app
// import through an alias it does not know (`~/shared/origin`) keeps the file
// it could name scanned, and an app file with a non-literal import() exempts
// nothing at all.
func TestLintFrontendProcessEnv_UnresolvedAppImportsFailClosed(t *testing.T) {
	t.Parallel()
	nodeOnly := func(t *testing.T, feDir string) {
		t.Helper()
		writeFE(t, feDir, "vite.config.ts", `import { origin } from "./shared/origin";
import p from "./plugins/devlog";
export default { server: { proxy: origin() }, plugins: [p()] };
`)
		writeFE(t, feDir, "shared/origin.ts", `export const origin = () => process.env.SHARED_ORIGIN;`)
		writeFE(t, feDir, "plugins/devlog.ts", `export default () => process.env.LOG_DIR;`)
	}

	t.Run("an alias the resolver does not know", func(t *testing.T) {
		root, feDir := newFrontendWithConfig(t)
		nodeOnly(t, feDir)
		writeFE(t, feDir, "src/main.tsx", `import { origin } from "~/shared/origin";
console.log(origin());
`)
		res := LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
		var files []string
		for _, f := range res.Findings {
			files = append(files, f.File)
		}
		if len(res.Findings) != 1 || !strings.HasSuffix(filepathSlash(files[0]), "shared/origin.ts") {
			t.Fatalf("want shared/origin.ts scanned (an unknown alias may name it) and plugins/devlog.ts exempt, got:\n%s", res.FormatText())
		}
	})
	t.Run("a non-literal import", func(t *testing.T) {
		root, feDir := newFrontendWithConfig(t)
		nodeOnly(t, feDir)
		writeFE(t, feDir, "src/main.tsx", "const name = location.hash.slice(1);\nimport(`../plugins/${name}`);\n")
		res := LintFrontendProcessEnv(root, []string{feDir}, finding.SeverityWarning)
		if len(res.Findings) != 2 {
			t.Fatalf("a non-literal app import must leave every file scanned, got %d:\n%s", len(res.Findings), res.FormatText())
		}
	})
}

func filepathSlash(p string) string { return strings.ReplaceAll(p, `\`, "/") }
