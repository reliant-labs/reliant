package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNextJSConfig_TracingRootNeverWritesIntoNodeModules is the real gate
// on the build-poisons-the-next-build defect. It is not a string check:
// it EVALUATES the outputFileTracingRoot expression the template emits,
// against a filesystem laid out the way forge lays a project out, and
// asserts the resulting root is an ancestor of the frontend.
//
// Why an ancestor is the whole property. Next.js writes each traced file
// to `<distDir>/standalone/<path relative to outputFileTracingRoot>`. If
// the root is NOT an ancestor of a traced file, that relative path begins
// with `../` and the write escapes the dist directory. forge's dev bridge
// makes the project root an npm workspace root, which hoists the real
// `next` to `<project>/node_modules` — above the frontend. With the root
// pinned to the frontend (`path.join(__dirname)`, the previous scaffold
// default) that install traced as `../../node_modules/next/...`, so the
// copy landed back in `frontends/<name>/node_modules/next`: 29 files of
// `dist/{compiled,pages,server}` and no package.json, sitting in the
// nearest node_modules Node consults. The following build resolved the
// stub instead of the complete hoisted install and failed inside Next's
// own package:
//
//	./node_modules/next/dist/pages/_app.js
//	Module not found: Can't resolve '../shared/lib/utils'
//
// So consecutive `forge build` runs alternated pass/fail deterministically,
// each one poisoning its successor — and the error named a file the user
// never wrote, in a package they never installed, mentioning neither
// tracing nor workspaces. Alternation reads as flake; a user re-runs, sees
// green, and ships.
//
// A `strings.Contains` assertion cannot see any of this, because the
// defect is in where the expression RESOLVES, not in how it is spelled.
func TestNextJSConfig_TracingRootNeverWritesIntoNodeModules(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates the Next.js config under node; runs in task test")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH — skipping tracing-root evaluation")
	}

	content, err := FrontendTemplates().Render(
		filepath.Join("nextjs", "next.config.ts.tmpl"),
		FrontendTemplateData{
			FrontendName: "dashboard",
			ProjectName:  "testproject",
			Output:       "standalone",
		},
	)
	if err != nil {
		t.Fatalf("render nextjs/next.config.ts.tmpl: %v", err)
	}
	expr := tracingRootExpr(t, string(content))

	// Two layouts, because the answer must differ between them and both
	// are real. The workspace layout is every forge project on a host;
	// the bare layout is the Docker build context, where the frontend is
	// copied alone under WORKDIR /app with nothing above it.
	for _, tc := range []struct {
		name         string
		workspaceDir bool // write a workspaces-declaring package.json above the frontend
	}{
		{"workspace root above the frontend (host build)", true},
		{"frontend alone (docker build context)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			frontend := filepath.Join(project, "frontends", "dashboard")
			if err := os.MkdirAll(frontend, 0o755); err != nil {
				t.Fatalf("mkdir frontend: %v", err)
			}
			// The frontend's own manifest declares no workspaces — the
			// walk must not mistake it for the root.
			writeJSON(t, filepath.Join(frontend, "package.json"), `{"name":"dashboard"}`)
			if tc.workspaceDir {
				writeJSON(t, filepath.Join(project, "package.json"),
					`{"name":"root","private":true,"workspaces":["frontends/*",".forge-link/*"]}`)
			}

			got := resolve(t, evalTracingRoot(t, node, expr, frontend))
			frontend = resolve(t, frontend)

			// The load-bearing assertion: the frontend must be at or
			// below the root. Anything else means a traced path can
			// begin with `../` and escape into a resolution path.
			rel, err := filepath.Rel(got, frontend)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Fatalf("outputFileTracingRoot resolved to %q, which is NOT an ancestor of the frontend %q "+
					"(rel=%q err=%v). Traced files would be written to <distDir>/standalone/../… and escape "+
					"the dist directory — the write that lands a partial `next` in the frontend's node_modules "+
					"and poisons the following build.", got, frontend, rel, err)
			}

			if tc.workspaceDir {
				// Must reach the workspace root specifically: that is
				// where npm hoists `next` to, and a root any lower
				// leaves the hoisted install outside it.
				if got != resolve(t, project) {
					t.Errorf("with a workspace root above it, outputFileTracingRoot must resolve to the workspace "+
						"root %q so the hoisted node_modules is a descendant; got %q", resolve(t, project), got)
				}
			} else {
				// No workspace above: the frontend is self-contained
				// and the bundle must stay flat, or the Dockerfile's
				// `COPY /app/.next-prod/standalone ./` finds no server.js.
				if got != resolve(t, frontend) {
					t.Errorf("with no workspace root above it (the docker build context), outputFileTracingRoot "+
						"must resolve to the frontend itself %q so the bundle lands flat at "+
						".next-prod/standalone/server.js; got %q", resolve(t, frontend), got)
				}
			}
		})
	}
}

// tracingRootExpr lifts the outputFileTracingRoot value out of the
// rendered config so it can be evaluated on its own. The expression is
// plain JavaScript (no TypeScript annotations), which is what makes
// evaluating it — rather than pattern-matching its text — possible.
func tracingRootExpr(t *testing.T, config string) string {
	t.Helper()
	const key = "outputFileTracingRoot:"
	i := strings.Index(config, key)
	if i < 0 {
		t.Fatalf("rendered next.config.ts has no outputFileTracingRoot:\n%s", config)
	}
	rest := config[i+len(key):]

	// Scan to the comma that closes this property, tracking nesting so a
	// comma inside the expression does not end it early.
	depth := 0
	for pos, r := range rest {
		switch r {
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(rest[:pos])
			}
		}
		if depth < 0 {
			t.Fatalf("unbalanced brackets scanning outputFileTracingRoot at %d", pos)
		}
	}
	t.Fatalf("could not find the end of the outputFileTracingRoot expression:\n%s", rest)
	return ""
}

// evalTracingRoot runs the lifted expression under node with __dirname
// bound to the frontend, exactly as Next.js evaluates next.config.ts.
func evalTracingRoot(t *testing.T, node, expr, frontend string) string {
	t.Helper()
	script := `const fs = require("node:fs");
const path = require("node:path");
const __dirname = process.env.FORGE_TEST_FRONTEND_DIR;
process.stdout.write(String(` + expr + `));`

	// __dirname is a real binding in CJS, so shadowing it needs a scope
	// where it is not already defined — hence the IIFE. The frontend path
	// arrives by env rather than argv because argv indexing under `-e`
	// differs from a script file.
	cmd := exec.Command(node, "-e", "(function(){"+script+"})()")
	cmd.Env = append(os.Environ(), "FORGE_TEST_FRONTEND_DIR="+frontend)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("evaluating outputFileTracingRoot expression failed: %v\nexpression:\n%s\noutput:\n%s", err, expr, out)
	}
	return strings.TrimSpace(string(out))
}

func writeJSON(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// resolve follows symlinks so comparisons hold on macOS, where t.TempDir()
// hands back /var/... while node reports /private/var/....
func resolve(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return real
}
