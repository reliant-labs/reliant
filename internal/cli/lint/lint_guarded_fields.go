// File: internal/cli/lint/lint_guarded_fields.go
//
// guarded-fields — `forge lint --guarded-fields`.
//
// `// forge:guards <table>.<column>` says a CUSTOM RPC owns a column's
// writes. The page generator honours it: the column comes off the scaffolded
// edit page's update_mask, so the form cannot write it raw. That covers
// every page forge emits from the moment the marker is written.
//
// It does not cover the pages already on disk, and that is what this rule is
// for.
//
// Scaffolded pages are WRITE-IF-ABSENT by design (see generateFrontendPages'
// "yours" contract): once forge has written a route it never writes that path
// again, no flag. So the realistic sequence — scaffold the CRUD pages, write
// the custom RPC, then add the marker once the guard exists — leaves an edit
// page whose mask still names the guarded column, and regenerating does
// nothing about it. The author has declared the guard, forge has agreed, and
// the form still bypasses it.
//
// Nothing else catches that:
//
//   - `forge generate` is silent: it skipped the file, which is correct.
//   - The frontend builds and typechecks. The column is a real field on a
//     real request; writing it is well-typed.
//   - No test fails. The generated CRUD lifecycle tests exercise the RPCs,
//     not the page.
//
// The symptom is a user editing a form and getting a 500 out of a raw CHECK
// constraint — the exact failure the marker was introduced to prevent,
// surviving in the one place regeneration cannot reach.
//
// ── What it reads, and why it is a text scan ─────────────────────────────
//
// Two halves, correlated:
//
//  1. The guards. Read off the proto SOURCE with codegen.GuardTargets, the
//     same grammar the descriptor path uses, so the check and the generator
//     cannot disagree about what a target is. Source rather than descriptor
//     because a marker added but not yet generated is precisely the state
//     this rule reports on.
//  2. The masks. Read off the scaffolded edit pages as text: the emitted
//     mask is a literal (`paths: ["a", "b"]`) written by forge's own
//     template, so the shape is known exactly rather than guessed. Parsing
//     TSX properly would need a TypeScript toolchain this process does not
//     have, and would buy nothing — a hand-rewritten page that no longer
//     has that literal is a page the user now owns, and silence is the
//     right answer for it.
//
// The table half of the target is matched against the page's ROUTE SLUG, not
// against a schema introspection. The slug is derived from the entity name
// by the same naming rules that produce the table name, so `invoices` in the
// marker and `src/app/invoices/edit/page.tsx` on disk are the same
// fact — and it means the rule needs neither a database nor a migration
// parse. A page whose slug resolves to no guard is silent.
//
// As with its read-only twin, a case this scan cannot resolve yields
// SILENCE, not a finding: a rule that misses one defect costs one defect,
// while a rule that invents one costs every future finding it would have
// reported.
//
// Severity is warning. The fix is an edit to a file the USER owns — forge
// must not rewrite it — so gating the build on it would block a project on
// a change only a human can make.

package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/naming"
)

// guardedFieldFinding is one scaffolded page that still writes a guarded
// column. File/Line point at the PAGE — the proto is correct, and the stale
// scaffold is the thing to edit.
type guardedFieldFinding struct {
	File   string
	Line   int
	Table  string
	Column string
	// GuardedBy is the rpc that owns the column. Carried so the message can
	// name the API the author should be calling instead, which is the whole
	// difference between "this is wrong" and "do this".
	GuardedBy string
	// ProtoFile / ProtoLine locate the marker itself, so a reader who
	// disagrees with the finding can go read the declaration.
	ProtoFile string
	ProtoLine int
}

// guardedFieldFixHint renders the remediation. It names the rpc first,
// because "call RecordPayment" is the actionable half and "remove this from
// the mask" alone would leave the author with a form that silently drops a
// value the user typed.
func guardedFieldFixHint(f guardedFieldFinding) string {
	return fmt.Sprintf(
		"This page's update_mask names %s.%s, but that column is declared `%s %s.%s` in %s:%d — "+
			"%s owns its writes. Saving this form writes the column raw through Update, bypassing "+
			"whatever %s enforces, and the first thing the user sees is a 500 from a CHECK "+
			"constraint. Scaffolded pages are written once and never regenerated, so `forge "+
			"generate` cannot fix this for you. Remove %q from the mask paths and drop the input, "+
			"then wire the value to %s's generated hook — a disabled row naming the rpc is what a "+
			"freshly scaffolded page emits. If the guard is wrong, delete the marker instead.",
		f.Table, f.Column, codegen.ProtoMarkerGuards, f.Table, f.Column,
		f.ProtoFile, f.ProtoLine, f.GuardedBy, f.GuardedBy, f.Column, f.GuardedBy)
}

// guardDecl is one `forge:guards` target with the rpc that declared it and
// the site it was written at.
type guardDecl struct {
	rpc       string
	protoFile string
	protoLine int
}

// collectGuardedFieldFindings is the shared engine behind text mode and
// `forge lint --json`. A project with no proto tree, or no guards declared,
// yields nothing — without both halves there is no correlation to make.
func collectGuardedFieldFindings(projectDir string, frontendDirs []string) ([]guardedFieldFinding, error) {
	protoRoot := filepath.Join(projectDir, protoDirDefault)
	if _, err := os.Stat(protoRoot); os.IsNotExist(err) {
		return nil, nil
	}
	guards, err := guardDeclsFromProtos(projectDir, protoRoot)
	if err != nil {
		return nil, err
	}
	if len(guards) == 0 {
		return nil, nil
	}

	var findings []guardedFieldFinding
	for _, feDir := range frontendDirs {
		fs, err := guardedMaskWritesIn(projectDir, feDir, guards)
		if err != nil {
			return nil, err
		}
		findings = append(findings, fs...)
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Line < findings[j].Line
	})
	return findings, nil
}

// guardRPCDeclRE captures the rpc NAME and its request message from an `rpc
// X(XRequest) returns (...)` line, so a guard found inside a request message
// can be attributed to the call that owns it. A guard whose message no rpc
// takes as input names nothing, and is skipped rather than reported as
// "managed by (unknown)".
var guardRPCDeclRE = regexp.MustCompile(`(?m)^\s*rpc\s+(\w+)\s*\(\s*(?:stream\s+)?([\w.]+)\s*\)`)

// guardMessageOpenRE captures a top-level message name at its opening brace.
var guardMessageOpenRE = regexp.MustCompile(`(?m)^\s*message\s+(\w+)\s*\{`)

// guardDeclsFromProtos indexes every `<table>.<column>` guard declared
// across the project's protos, keyed by target.
//
// The walk is per-file and line-oriented, at the same grade as the sibling
// proto checks: a guard is attributed to the message whose body it sits in,
// and that message to the rpc that takes it as input. Two rpcs guarding one
// column keeps the first seen, matching the page generator's own tie-break —
// the two must agree, or lint would name a different rpc than the scaffold.
func guardDeclsFromProtos(projectDir, protoRoot string) (map[string]guardDecl, error) {
	var files []string
	if err := filepath.WalkDir(protoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".proto") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk %s: %w", protoRoot, err)
	}
	sort.Strings(files)

	guards := map[string]guardDecl{}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		content := string(data)

		// request message → the rpc that takes it.
		rpcFor := map[string]string{}
		for _, m := range guardRPCDeclRE.FindAllStringSubmatch(content, -1) {
			req := m[2]
			if i := strings.LastIndex(req, "."); i >= 0 {
				req = req[i+1:]
			}
			if _, taken := rpcFor[req]; !taken {
				rpcFor[req] = m[1]
			}
		}

		currentMsg := ""
		for i, line := range strings.Split(content, "\n") {
			if m := guardMessageOpenRE.FindStringSubmatch(line); m != nil {
				currentMsg = m[1]
			}
			targets := codegen.GuardTargets(line)
			if len(targets) == 0 {
				continue
			}
			rpc, ok := rpcFor[currentMsg]
			if !ok {
				// A guard in a message no rpc takes as input has no call
				// to name. Reporting it as owned by nothing would send the
				// reader looking for an API that does not exist.
				continue
			}
			for _, target := range targets {
				if _, taken := guards[target]; taken {
					continue
				}
				guards[target] = guardDecl{
					rpc:       rpc,
					protoFile: relToProject(projectDir, path),
					protoLine: i + 1,
				}
			}
		}
	}
	return guards, nil
}

// guardMaskPathsRE captures the whole `paths: [...]` literal forge's edit
// template emits. Matching the literal rather than parsing the file is
// deliberate: this is forge's OWN emitted shape, so it is known exactly, and
// a page that no longer carries it has been rewritten by its owner and is
// none of this rule's business.
var guardMaskPathsRE = regexp.MustCompile(`paths:\s*\[([^\]]*)\]`)

// guardMaskEntryRE captures one quoted column name inside that literal.
var guardMaskEntryRE = regexp.MustCompile(`"([\w.]+)"`)

// guardedMaskWritesIn reports every scaffolded edit page under one frontend
// whose update_mask names a guarded column.
//
// The entity is resolved from the page's ROUTE SLUG — the directory forge
// named after the entity — and compared against the guard's table half via
// the same naming rules that produced both. That keeps the check free of any
// database or migration dependency, and it is what makes a guard on
// `payments.amount_cents` correctly silent about a page editing invoices.
func guardedMaskWritesIn(projectDir, feDir string, guards map[string]guardDecl) ([]guardedFieldFinding, error) {
	root := filepath.Join(projectDir, feDir)
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}
	var findings []guardedFieldFinding
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipGoScanDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".tsx") {
			return nil
		}
		table, ok := guardTableForPage(path)
		if !ok {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		for i, line := range strings.Split(string(data), "\n") {
			m := guardMaskPathsRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			for _, entry := range guardMaskEntryRE.FindAllStringSubmatch(m[1], -1) {
				decl, guarded := guards[table+"."+entry[1]]
				if !guarded {
					continue
				}
				findings = append(findings, guardedFieldFinding{
					File: relToProject(projectDir, path), Line: i + 1,
					Table: table, Column: entry[1], GuardedBy: decl.rpc,
					ProtoFile: decl.protoFile, ProtoLine: decl.protoLine,
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return findings, nil
}

// guardTableForPage resolves the table a scaffolded page edits from its
// path, or ok=false when the path is not one of forge's emitted edit routes.
//
// Next.js emits `<slug>/edit/page.tsx` (an older forge, `<slug>/[id]/edit/
// page.tsx`, which projects still carry) and Vite `<slug>/Edit.tsx`, so the
// slug is the directory above the route marker in every layout. The slug is
// kebab-case of the entity name; the table is its pluralized snake_case —
// the same two derivations the generator applies, run here in the same
// order so the two spellings of one entity cannot drift.
func guardTableForPage(path string) (string, bool) {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) < 2 {
		return "", false
	}
	base := parts[len(parts)-1]

	var slug string
	switch {
	case base == "Edit.tsx":
		slug = parts[len(parts)-2]
	case base == "page.tsx" && len(parts) >= 4 &&
		parts[len(parts)-2] == "edit" && parts[len(parts)-3] == "[id]":
		slug = parts[len(parts)-4]
	case base == "page.tsx" && len(parts) >= 3 && parts[len(parts)-2] == "edit":
		slug = parts[len(parts)-3]
	default:
		return "", false
	}
	if slug == "" {
		return "", false
	}
	return naming.Pluralize(naming.ToSnakeCase(strings.ReplaceAll(slug, "-", "_"))), true
}
