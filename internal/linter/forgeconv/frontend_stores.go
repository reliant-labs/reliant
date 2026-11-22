// File: internal/linter/forgeconv/frontend_stores.go
//
// The forgeconv-frontend-stores-no-server-data analyzer warns when a
// Zustand store file ALSO imports a generated Connect client. The two
// shapes don't compose: server state belongs in React Query (with
// generated hooks); Zustand is for client-only state (modals, theme,
// per-user UI prefs). Mixing the two creates two sources of truth that
// inevitably diverge.
//
// Scope: the store directory of every frontend — `src/stores/` (what the
// scaffold writes) and `src/store/` (what many existing apps use, and what
// pre-workspaces projects shipped) — under both `frontends/<name>/` and the
// historic single-frontend `web/`. A store directory the scan never reads is
// a gate that passes by construction: an adopted frontend at
// `frontends/web/src/store/` was invisible to this rule, so every run over it
// reported clean however much server state its stores held.
//
// Detection heuristic (string-level, no TS parser):
//
//  1. file matches reZustandCreate     `create\s*<` (Zustand's generic
//     factory; matches `create<State>(...)`, `create<State, [...]>`, etc.)
//
//  2. file reaches a server: it imports a generated CLIENT from a `/gen/`
//     path with one of the suffix tells `-grpc`, `_pb`, `_connect`
//     (canonical Connect-TS / buf-gen output paths), or it builds one
//     (`createClient(` / `connectClient(`). From a `-grpc` / `_connect`
//     file any value import is a client. From a `_pb` file only a service
//     descriptor is (`FooService`, protobuf-es v2's client input) — or a
//     namespace import, which can reach one. Messages, enums and anything
//     under `import type` describe a store's shape but cannot reach a
//     server, so they are not the smell: a store holding a selected
//     `ChatState` is client state.
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

	// reGenImport matches an import statement whose path goes through a
	// `/gen/` segment AND ends in one of the canonical Connect-TS / buf-gen
	// shape suffixes (-grpc, _pb, _connect). Anchored to the `import ...
	// from` statement to avoid false-firing on URL strings sitting
	// elsewhere in the file. Groups: 1 a leading `type` (the whole import
	// is type-only), 2 the import clause, 3 the suffix tell.
	reGenImport = regexp.MustCompile(
		`\bimport\s+(type\s+)?([^;'"]*?)\s*\bfrom\s+["'][^"']*\/gen\/[^"']*(-grpc|_pb|_connect)[^"']*["']`)

	// reServiceName matches an imported binding that is a service
	// descriptor, with an optional inline `type` modifier (group 1).
	reServiceName = regexp.MustCompile(`(?:^|[{,\s])(type\s+)?\w*Service\b`)

	// reClientFactory matches a store building a Connect client itself.
	reClientFactory = regexp.MustCompile(`\b(?:createClient|connectClient)\s*\(`)
)

// frontendStoreDirs are the directory names, under a frontend's src/, that
// hold its stores: `stores` is what forge scaffolds, `store` is the other
// common spelling (and the pre-workspaces web/ shape).
var frontendStoreDirs = []string{"stores", "store"}

// LintFrontendStores scans every frontend's src/stores/ and src/store/
// (frontends/<name>/ and web/) for files that both spin up a Zustand store
// AND pull in a generated Connect client. Warnings only. A project with no
// frontends at all gets an empty result.
//
// Findings are sorted by (file, line, rule) for stable output. Line is
// the line where reZustandCreate hits (the user's eye goes to the
// store-factory call, not the import line).
func LintFrontendStores(rootDir string) (Result, error) {
	// Every frontend root: frontends/<name>/ (the workspaces layout) and
	// the pre-workspaces single web/.
	var frontendRoots []string
	frontendsDir := filepath.Join(rootDir, "frontends")
	if _, err := os.Stat(frontendsDir); err == nil {
		entries, readErr := os.ReadDir(frontendsDir)
		if readErr != nil {
			return Result{}, fmt.Errorf("read %s: %w", frontendsDir, readErr)
		}
		for _, e := range entries {
			if e.IsDir() {
				frontendRoots = append(frontendRoots, filepath.Join(frontendsDir, e.Name()))
			}
		}
	}
	frontendRoots = append(frontendRoots, filepath.Join(rootDir, "web"))

	var targets []string
	for _, fe := range frontendRoots {
		for _, dir := range frontendStoreDirs {
			ts, err := collectTSFiles(filepath.Join(fe, "src", dir))
			if err != nil {
				return Result{}, err
			}
			targets = append(targets, ts...)
		}
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

	if !reachesServer(body) {
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

// reachesServer reports whether a store file imports or builds a Connect
// client (see the package doc, gate 2).
func reachesServer(body string) bool {
	if reClientFactory.MatchString(body) {
		return true
	}
	for _, m := range reGenImport.FindAllStringSubmatch(body, -1) {
		typeOnly, clause, tell := m[1] != "", m[2], m[3]
		if typeOnly {
			continue
		}
		if tell != "_pb" {
			return true
		}
		if strings.Contains(clause, "*") {
			return true // `import * as pb`: it can reach any service
		}
		for _, name := range reServiceName.FindAllStringSubmatch(clause, -1) {
			if name[1] == "" {
				return true
			}
		}
	}
	return false
}
