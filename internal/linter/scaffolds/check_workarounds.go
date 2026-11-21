// Package scaffolds — `forge lint --check-workarounds` rule.
//
// Detects the canonical cross-lane workarounds that shipped to cpnext
// during the v0.2 rebuild (see FORGE_REVIEW_PROCESS.md §2 "Inline
// workarounds shipped in cpnext"). These are ad-hoc patterns that
// compile and ship clean but are real anti-patterns: they paper over a
// missing forge primitive rather than wait for it. Surfacing them as
// warnings turns the catch-bar from "will the next reviewer notice?"
// into "lint flags it before merge."
//
// All findings are SeverityWarning, not error — some workarounds are
// legitimate in some projects (a project might genuinely want a
// `cmd/maintenance.go` cli tool that's not modeled in forge.yaml). The
// warning includes a "should be:" pointer so the reader knows the
// canonical replacement.
//
// A "should be:" pointer written in the FUTURE TENSE is a liability once
// the primitive it waits on ships: it reads as an open gap, and an open
// gap is the standing justification for keeping the fork alive. Rule 1
// below sat stale for a month after its gap closed and was cited as
// live evidence in an ownership audit. When a primitive lands, the
// message it obsoletes has to move with it.
//
// Two rules today, each tracking a workaround actually shipped to
// cpnext:
//
//  1. `testingExtras` — flags `pkg/app/testing_extras.go` files, the
//     hand-rolled stub-repo workaround for the "scaffold-test factory
//     doesn't fill required Deps" gap (FORGE_REVIEW_PROCESS.md §2.2).
//     That gap is CLOSED: `computeDepsDoubles` (internal/codegen/
//     bootstrap_gen.go) fills every interface-typed Deps field — with
//     the forge mock_gen.go mock where one exists, a synthesized stub
//     otherwise; bare identifiers and cross-package selectors alike —
//     and `bootstrap_testing.go.tmpl` emits them. The rule is therefore
//     sharper than when it was written, not obsolete: the file it flags
//     no longer has a gap to justify it, so the finding is now "delete
//     this", not "wait for the fix". Only unresolvable selectors still
//     need hand-rolling, and those are named individually by a TODO in
//     NewTest<Svc> rather than carried in a wholesale bridge file.
//
//  2. `cmdNotInBinaries` — flags `cmd/<name>.go` files that aren't
//     declared in `forge.yaml`'s `binaries:` block. Today every
//     non-server second binary is hand-written (cpnext's
//     `cmd/workspace_proxy.go` is 270 LOC of cobra/k8s/signal-handling
//     boilerplate). R5-2's `forge scaffold binary` command makes the
//     declaration explicit.
//
// Wiring: `runCheckWorkaroundsLint` invokes `LintWorkaroundsRoot` from
// the project root; `lint.go` registers the `--check-workarounds` flag
// and includes the rule in the default `forge lint` run.
package scaffolds

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LintWorkaroundsRoot walks the project tree rooted at root and applies
// the three workaround-detection rules. Returns a Result whose Findings
// are all SeverityWarning.
//
// The walker reuses the same skipDir set as the scaffolds linter so we
// don't churn through `gen/`, `node_modules/`, or `vendor/`.
func LintWorkaroundsRoot(root string) (Result, error) {
	var result Result

	// Rule 2 is path-based — `pkg/app/testing_extras.go` is the canonical
	// site. Check explicitly rather than waiting for the walker to
	// stumble onto it; this also fires when the walker would otherwise
	// skip the directory (defensive).
	testingExtrasPath := filepath.Join(root, "pkg", "app", "testing_extras.go")
	if _, err := os.Stat(testingExtrasPath); err == nil {
		rel := relPath(testingExtrasPath, root)
		result.Findings = append(result.Findings, Finding{
			Rule:     "workaround-testing-extras",
			Severity: SeverityWarning,
			Path:     rel,
			Message: "pkg/app/testing_extras.go is a hand-rolled stub-repo file forked to bridge `app.NewTest<Svc>` not filling required Deps " +
				"(FORGE_REVIEW_PROCESS.md §2.2). That gap IS CLOSED: computeDepsDoubles fills every interface-typed Deps field (with its forge mock, or a synthesized stub) — " +
				"including cross-package selector types like `repo.Repository` — and With<Svc>Deps overrides any of them. " +
				"Should be: delete this file and inject real behavior through With<Svc>Deps(...) in your own _test.go files. " +
				"A field forge could not resolve is called out by a TODO in NewTest<Svc>, so what still needs hand-rolling is named at the point of use, not carried wholesale here.",
		})
	}

	// Rule 3 needs the forge.yaml binaries: block AND the cmd/ dir.
	// Build the declared-binaries set first; if forge.yaml has no
	// binaries: block, the rule effectively reports every cmd/<name>.go
	// other than `server.go` (the canonical first binary).
	declaredBinaries := readDeclaredBinaries(filepath.Join(root, "forge.yaml"))

	cmdDir := filepath.Join(root, "cmd")
	if entries, err := os.ReadDir(cmdDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			if strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			base := strings.TrimSuffix(e.Name(), ".go")
			// `server.go` is the canonical first-binary forge ships.
			// `root.go`, `version.go`, and `_shared.go` style helpers
			// are also exempt — they're conventional cobra harness
			// scaffolding rather than independent binaries.
			if isExemptCmdFile(base) {
				continue
			}
			// Match either kebab (forge.yaml convention) or snake
			// (Go file convention). `workspace-proxy` in forge.yaml
			// pairs with `workspace_proxy.go` on disk; both should
			// satisfy the binaries: declaration.
			if declaredBinaries[base] || declaredBinaries[strings.ReplaceAll(base, "_", "-")] {
				continue
			}
			rel := relPath(filepath.Join(cmdDir, e.Name()), root)
			result.Findings = append(result.Findings, Finding{
				Rule:     "workaround-cmd-not-in-binaries",
				Severity: SeverityWarning,
				Path:     rel,
				Message: fmt.Sprintf(
					"cmd/%s is a hand-written second binary not declared in forge.yaml's `binaries:` block "+
						"(FORGE_REVIEW_PROCESS.md §2.4 workspace_proxy pattern). "+
						"Should be: declare in forge.yaml binaries: block (post-`forge scaffold binary`), or remove if unused.",
					e.Name(),
				),
			})
		}
	}

	return result, nil
}

// isExemptCmdFile returns true for cmd/<name>.go files that conventionally
// belong to the cobra harness rather than to an independent binary.
// Keep this list synced with the forge templates that emit cmd/* —
// `forge project new --kind service` ships server.go, db.go, and otel.go, all
// of which are generated cobra subcommands rather than independent
// second-binary scaffolds.
//
//   - server.go: forge's canonical first-binary scaffold
//   - db.go: cobra `<binary> db migrate ...` subcommand (Tier-1 from
//     internal/templates/project/cmd-db.go.tmpl)
//   - otel.go: OpenTelemetry init shim shared by all binaries
//   - root.go, version.go: cobra root + version subcommand
//   - main.go: cobra harness entry
//   - <name>_shared.go suffix: shared helper file
func isExemptCmdFile(base string) bool {
	switch base {
	case "server", "root", "version", "main", "db", "otel":
		return true
	}
	return strings.HasSuffix(base, "_shared")
}

// readDeclaredBinaries returns the set of binary-kind component names
// declared in forge.yaml's `components:` block (entries with
// `kind: binary`). Binaries used to live in a dedicated `binaries:`
// block; the component-model unification folded them into `components:`
// keyed on `kind:`.
//
// We parse forge.yaml manually to avoid pulling the full config package
// into the linter's dependency graph. The components block is shallow
// enough that a line-oriented reader works: we walk each `- name:`
// entry, remember its name, and emit it only once we see a sibling
// `kind: binary` line within the same entry.
func readDeclaredBinaries(path string) map[string]bool {
	out := make(map[string]bool)
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	inComponents := false
	curName := ""
	curKind := ""
	flush := func() {
		if curName != "" && curKind == "binary" {
			out[curName] = true
		}
		curName = ""
		curKind = ""
	}
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		// New top-level key resets the in-block flag (and flushes any
		// pending entry from the components block we're leaving).
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "-") && trimmed != "" {
			flush()
			inComponents = strings.HasPrefix(trimmed, "components:")
			continue
		}
		if !inComponents {
			continue
		}
		// A new list element starts a new entry — flush the previous one.
		if strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
			flush()
		}
		if name, ok := scalarYAMLValue(trimmed, "name"); ok {
			curName = name
			continue
		}
		if kind, ok := scalarYAMLValue(trimmed, "kind"); ok {
			curKind = kind
			continue
		}
	}
	flush()
	return out
}

// scalarYAMLValue extracts the value of `key` from a line shaped like
// `key: value` or `- key: value` (the first key of a list element),
// stripping quotes. Returns ok=false when the line isn't that key.
func scalarYAMLValue(trimmed, key string) (string, bool) {
	body := strings.TrimPrefix(trimmed, "- ")
	prefix := key + ":"
	if !strings.HasPrefix(body, prefix) {
		return "", false
	}
	v := strings.TrimSpace(strings.TrimPrefix(body, prefix))
	return strings.Trim(v, `"'`), true
}
