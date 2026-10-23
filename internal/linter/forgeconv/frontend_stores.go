// File: internal/linter/forgeconv/frontend_stores.go
//
// The forgeconv-frontend-stores-no-server-data analyzer warns when a
// Zustand store file ALSO imports a generated Connect client. The two
// shapes don't compose: server state belongs in React Query (with
// generated hooks); Zustand is for client-only state (modals, theme,
// per-user UI prefs). Mixing the two creates two sources of truth that
// inevitably diverge.
//
// Scope: `frontends/*/src/stores/*.ts` and the historic single-frontend
// `web/src/store/*.ts` shape. Pre-workspaces projects (forge.yaml
// frontend.workspaces == false) ship a single `web/` frontend; the
// workspaces layout ships `frontends/<name>/`. The analyzer scans both
// so a mid-migration project gets findings against whichever shape
// happens to be on disk.
//
// Detection heuristic (string-level, no TS parser):
//
//  1. file matches reZustandCreate     `create\s*<` (Zustand's generic
//     factory; matches `create<State>(...)`, `create<State, [...]>`, etc.)
//
//  2. file matches reGenImportFrom     `from\s+["'][^"']*\/gen\/[^"']*["']`
//     AND the same import string contains one of the suffix tells
//     `-grpc`, `_pb`, `_connect` (canonical Connect-TS / buf-gen output
//     paths).
//
// Both must be true — a Zustand store with no gen imports is fine, and
// a file that just re-exports generated types without spinning up a
// store is also fine.
//
// The rule is a warning. Forcing a hard error would block legitimate
// edge cases (e.g. a Zustand store that caches a one-shot lookup before
// the React Query layer comes online), and the broader signal — "this
// file has the smell" — is more valuable as a soft nudge.

package forgeconv

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Compiled regexes. Kept package-level so the walk doesn't re-compile
// per file. See the package doc for the detection-heuristic rationale.
var (
	// reZustandCreate matches Zustand's `create<...>(...)` factory.
	// Tolerant of whitespace around the angle bracket; doesn't try to
	// parse the full generic args. The factory may be imported under
	// any alias (`import { create } from 'zustand'` is canonical, but
	// `import { create as makeStore }` is in the wild) — once the
	// `<...>` form appears, the file is almost always a Zustand store.
	reZustandCreate = regexp.MustCompile(`\bcreate\s*<`)

	// reGenImport matches an import whose path goes through a `/gen/`
	// segment AND ends in one of the canonical Connect-TS / buf-gen
	// shape suffixes (-grpc, _pb, _connect). Anchored to a `from`
	// import-statement to avoid false-firing on URL strings sitting
	// elsewhere in the file.
	reGenImport = regexp.MustCompile(
		`from\s+["'][^"']*\/gen\/[^"']*(?:-grpc|_pb|_connect)[^"']*["']`)
)

// LintFrontendStores scans frontends/<name>/src/stores/*.ts and
// web/src/store/*.ts for files that both spin up a Zustand store AND
// pull in a generated Connect client. Warnings only. A project with no
// frontends at all gets an empty result.
//
// Findings are sorted by (file, line, rule) for stable output. Line is
// the line where reZustandCreate hits (the user's eye goes to the
// store-factory call, not the import line).
func LintFrontendStores(rootDir string) (Result, error) {
	var targets []string

	// Workspaces / per-frontend layout: frontends/<name>/src/stores/*.ts
	frontendsDir := filepath.Join(rootDir, "frontends")
	if _, err := os.Stat(frontendsDir); err == nil {
		entries, readErr := os.ReadDir(frontendsDir)
		if readErr != nil {
			return Result{}, fmt.Errorf("read %s: %w", frontendsDir, readErr)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			storesDir := filepath.Join(frontendsDir, e.Name(), "src", "stores")
			ts, err := collectTSFiles(storesDir)
			if err != nil {
				return Result{}, err
			}
			targets = append(targets, ts...)
		}
	}

	// Pre-workspaces single-frontend layout: web/src/store/*.ts
	// Note: singular `store` — that's the shape projects scaffolded
	// before the workspaces flag landed.
	webStoreDir := filepath.Join(rootDir, "web", "src", "store")
	if _, err := os.Stat(webStoreDir); err == nil {
		ts, err := collectTSFiles(webStoreDir)
		if err != nil {
			return Result{}, err
		}
		targets = append(targets, ts...)
	}

	sort.Strings(targets)

	var result Result
	for _, path := range targets {
		findings, err := lintFrontendStoreFile(path, rootDir)
		if err != nil {
			return Result{}, err
		}
		result.Findings = append(result.Findings, findings...)
	}

	sort.SliceStable(result.Findings, func(i, j int) bool {
		if result.Findings[i].File != result.Findings[j].File {
			return result.Findings[i].File < result.Findings[j].File
		}
		if result.Findings[i].Line != result.Findings[j].Line {
			return result.Findings[i].Line < result.Findings[j].Line
		}
		return result.Findings[i].Rule < result.Findings[j].Rule
	})
	return result, nil
}

// collectTSFiles returns every *.ts file directly under dir (and its
// subdirectories), excluding *.d.ts (declaration-only files don't run
// at runtime so a `create<State>` in a .d.ts is impossible by
// construction) and *.test.ts (tests legitimately mock the gen client
// alongside Zustand stores under test).
func collectTSFiles(dir string) ([]string, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if shouldSkipFrontendSubdir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		base := d.Name()
		if !strings.HasSuffix(base, ".ts") {
			return nil
		}
		if strings.HasSuffix(base, ".d.ts") || strings.HasSuffix(base, ".test.ts") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	return out, nil
}

// shouldSkipFrontendSubdir is shared with the frontend_hook_tests
// analyzer — see frontend_hook_tests.go for the canonical skip list.

// lintFrontendStoreFile applies the two-gate heuristic to a single .ts
// file. relRoot is used to produce a stable, project-relative path in
// the finding output.
func lintFrontendStoreFile(path, relRoot string) ([]Finding, error) {
	src, err := os.ReadFile(path) //nolint:gosec // lint walker drives paths
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	body := string(src)

	if !reGenImport.MatchString(body) {
		return nil, nil
	}
	loc := reZustandCreate.FindStringIndex(body)
	if loc == nil {
		return nil, nil
	}

	// Convert byte offset to 1-indexed line number. Cheap newline count
	// over the prefix — no need to scan the whole body.
	line := 1 + strings.Count(body[:loc[0]], "\n")

	rel, relErr := filepath.Rel(relRoot, path)
	if relErr != nil {
		rel = path
	}

	return []Finding{{
		Rule:     "forgeconv-frontend-stores-no-server-data",
		Severity: SeverityWarning,
		File:     rel,
		Line:     line,
		Message: "server data should live in React Query / generated hooks, not Zustand; " +
			"see frontend/state skill",
		Remediation: "move server-derived state into the generated React Query hooks " +
			"(`packages/hooks/src/generated/<svc>-hooks.ts` or `frontends/<name>/src/hooks/<svc>-hooks.ts`); " +
			"reserve Zustand for client-only UI state (modals, theme, transient form state)",
	}}, nil
}
