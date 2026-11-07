//go:build e2e

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/components"
)

// componentLintWarningAllowlist names the eslint WARNINGS a library
// component may carry under a scaffold's config, keyed "<file> <rule>", each
// with the reason it stays. Errors are never allowed: they fail the
// project's own `npm run lint`. Delete an entry once the warning is gone; add
// one only with a reason a reviewer would accept.
var componentLintWarningAllowlist = map[string]string{
	// The trigger render prop and the children render prop receive callbacks
	// that close over refs (the trigger's callback ref, close() restoring
	// focus to triggerRef). The React Compiler's heuristic reports any ref
	// reachable from a function argument; neither callback reads a ref during
	// render.
	"popover.tsx react-hooks/refs": "render props receive callbacks that close over refs; none is read during render",
}

// TestE2EComponentLibraryLintsCleanInEveryScaffold installs EVERY library
// component into a fresh scaffold of each browser frontend kind and runs that
// scaffold's own eslint config and tsc over them.
//
// A component is copied into a project by `forge component install` and is
// the project's file from then on, so a lint or type error in it is an error
// in the project's `npm run lint` / `npm run typecheck` gate on the day it is
// installed. The static guards in pkg/components (library_lint_gate_test.go)
// catch known shapes; this runs the real tools. Born red on:
//
//	tooltip.tsx            react-hooks/immutability (x2), react-hooks/refs (x2)
//	combobox.tsx           jsx-a11y/role-supports-aria-props (aria-invalid on a button)
//	pricing_table.tsx      jsx-a11y/anchor-is-valid (href="#")
//	concentric_circles.tsx TS18048 (x4), flow_horizontal.tsx TS2345 (x2),
//	org_chart.tsx          TS2322 — noUncheckedIndexedAccess
//	markdown_editor.tsx    @typescript-eslint/no-unused-vars, under the Vite config only
//
// found while building growth's frontend (tooltip failed `pnpm lint` and was
// removed rather than disable the rules).
//
// Components the scaffold already ships (its own link.tsx, button.tsx, ...)
// are left alone: the scaffold-lint e2e tests cover those, and the scaffold's
// link.tsx is framework-aware where the library's is not.
func TestE2EComponentLibraryLintsCleanInEveryScaffold(t *testing.T) {
	t.Parallel()
	requireTool(t, "node", "npm", "pnpm")
	forgeBin := buildforgeBinary(t)
	prebuildWebRuntimeE2E(t)

	base := filepath.Join(t.TempDir(), "libapp")
	runCmd(t, filepath.Dir(base), forgeBin, "project", "new", "libapp", "--mod", "example.com/libapp", "--service", "orders")
	addCorpusForgePkgReplace(t, base)

	registry := components.NewLibrary().Registry()
	if len(registry) == 0 {
		t.Fatal("component library lists no components — this guard has gone blind")
	}

	for _, tc := range []struct {
		kind, frontend string
		args           []string
	}{
		{kind: "nextjs", frontend: "web", args: []string{"scaffold", "frontend", "web"}},
		{kind: "vite-spa", frontend: "spa", args: []string{"scaffold", "frontend", "spa", "--kind", "vite-spa"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			projectDir := filepath.Join(t.TempDir(), "libapp")
			runCmd(t, filepath.Dir(projectDir), "cp", "-a", base, projectDir)
			runCmdTimeout(t, projectDir, 10*time.Minute, forgeBin, tc.args...)
			feDir := filepath.Join(projectDir, "frontends", tc.frontend)
			uiDir := filepath.Join(feDir, "src", "components", "ui")

			scaffolded := map[string]bool{}
			before, err := os.ReadDir(uiDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range before {
				scaffolded[e.Name()] = true
			}
			var names []string
			for _, entry := range registry {
				if !scaffolded[entry.Name+".tsx"] {
					names = append(names, entry.Name)
				}
			}
			if len(names) == 0 {
				t.Fatal("every library component is already in the scaffold — nothing was installed, so nothing was checked")
			}
			runCmdTimeout(t, projectDir, 2*time.Minute, forgeBin,
				append([]string{"component", "install", "--dir", feDir}, names...)...)
			// What the install wrote: the requested components and the sibling
			// imports it adds for them.
			installed := map[string]bool{}
			after, err := os.ReadDir(uiDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range after {
				if !scaffolded[e.Name()] {
					installed[e.Name()] = true
				}
			}

			runCmdTimeout(t, feDir, 5*time.Minute, "pnpm", "install", "--no-frozen-lockfile", "--prefer-offline")

			report := filepath.Join(t.TempDir(), "eslint.json")
			// eslint exits non-zero on an error; the report is the evidence.
			out, lintErr := runCmdCombined(feDir, 5*time.Minute, "pnpm", "exec", "eslint", "--format", "json", "--output-file", report, "src/components/ui")
			body, err := os.ReadFile(report)
			if err != nil {
				t.Fatalf("eslint wrote no report (%v):\n%s", lintErr, out)
			}
			var results []struct {
				FilePath string `json:"filePath"`
				Messages []struct {
					RuleID   string `json:"ruleId"`
					Severity int    `json:"severity"`
					Line     int    `json:"line"`
					Message  string `json:"message"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(body, &results); err != nil {
				t.Fatalf("parse eslint report: %v\n%s", err, body)
			}
			var findings []string
			for _, r := range results {
				file := filepath.Base(r.FilePath)
				if !installed[file] {
					continue
				}
				for _, m := range r.Messages {
					if m.Severity < 2 {
						if _, ok := componentLintWarningAllowlist[file+" "+m.RuleID]; ok {
							continue
						}
					}
					level := "warning"
					if m.Severity >= 2 {
						level = "error"
					}
					first, _, _ := strings.Cut(m.Message, "\n")
					findings = append(findings, file+":"+strconv.Itoa(m.Line)+" "+level+" "+m.RuleID+": "+first)
				}
			}
			if len(findings) > 0 {
				sort.Strings(findings)
				t.Errorf("library components fail the %s scaffold's own eslint config. "+
					"`forge component install` copies them into a project, where every one is a "+
					"finding the project did not write. Fix the component; a warning that must "+
					"stay goes in componentLintWarningAllowlist with its reason:\n  %s",
					tc.kind, strings.Join(findings, "\n  "))
			}

			if out, err := runCmdCombined(feDir, 5*time.Minute, "pnpm", "run", "typecheck"); err != nil {
				t.Errorf("`pnpm run typecheck` fails with every library component installed into the %s scaffold "+
					"(its tsconfig sets strict and noUncheckedIndexedAccess):\n%s", tc.kind, out)
			}
		})
	}
}
