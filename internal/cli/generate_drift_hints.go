// Per-file extension-point hints for Tier-1 drift.
//
// When the stomp guard catches a hand-edited Tier-1 file, the worst
// thing the error can do is lead with `forge project disown`: agents take the
// path of least resistance, disown the file, and permanently lose
// regeneration (the failure chain this whole subsystem exists to
// prevent). The right answer is almost always "your customization has
// a designated user-owned home" — these hints name that home, per
// file shape, so the error message teaches the extension point first
// and the disown one-way door last.
//
// ── Why the hint must be TOTAL ────────────────────────────────────────
//
// An audit of every disown in the fleet found ZERO that were deliberate,
// permanent ownership choices. Every one was a forge gap wearing an
// ownership hat: a missing extension point (the whole Connect
// interceptor / CORS / extra-route edge has no user-owned surface at
// all), a migration shield, or a gap that has since been closed and left
// the disown behind as dead weight.
//
// The mechanism that produced that outcome is this function returning
// "". A blank hint reads as "forge has no opinion, do what you like",
// and the only remedy left on screen is the one-way door. So the hint
// is TOTAL: every Tier-1 path resolves to exactly one of two answers.
//
//	SEAM EXISTS  → name the user-owned file and the hook. Editing the
//	               generated file is simply the wrong move.
//	NO SEAM      → say so, in those words, and name the seam that is
//	               missing. A file with no extension point is a FORGE
//	               DEFECT, not a project problem; the user's next action
//	               is to report the named gap, not to quietly fork.
//
// The NO-SEAM branch is the feature that replaces `forge project
// disown`. Disown's real payload was always the `--reason` string — the
// design feedback saying "forge could not express this". Naming the gap
// at the moment of friction captures that payload without also
// suppressing regeneration forever.
package cli

import (
	"fmt"
	"path"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
)

// noSeam formats the NO-SEAM verdict. `missing` names the extension
// point that does not exist, in the vocabulary a forge issue would use.
// The wording is deliberately blunt about whose bug it is: a Tier-1 file
// with no extension point is forge's defect, and a user who reads this
// should reach for the issue tracker before the escape hatch.
func noSeam(missing string) string {
	return "NO EXTENSION POINT EXISTS: forge owns every byte here and exposes no user-owned seam for " +
		missing + ". That is a forge gap, not a project problem — report it (a one-line repro plus the seam you need) " +
		"rather than forking the file, because a fork here silently stops receiving every future fix to it"
}

// seamAt formats the SEAM-EXISTS verdict: the user-owned file that
// survives every regenerate, and the hook inside it to reach for.
func seamAt(file, hook string) string {
	return "this change belongs in " + file + " (" + hook + ") — user-owned, survives every regenerate"
}

// tier1ExtensionPointHint returns the designated user-owned extension
// point for a Tier-1 path, or — when forge exposes none — the named gap.
// It is TOTAL: it never returns "" for a path the stomp guard can hand
// it. relPath is project-relative with forward slashes.
//
// Every branch below is grounded in a seam that was VERIFIED to exist
// (or verified to be absent) in the current tree, not in what the
// templates once documented. When a seam lands, move its path from the
// noSeam branch to a seamAt branch in the same commit — that pairing is
// what keeps this table from rotting into the stale advice that made
// disowning look reasonable in the first place.
func tier1ExtensionPointHint(relPath string) string {
	rel := strings.TrimPrefix(relPath, "./")
	base := path.Base(rel)
	dir := path.Dir(rel)

	// ── cmd/<bin>/cmd/ — the HTTP edge ────────────────────────────────
	//
	// serve.go carries the entire Connect interceptor chain, the CORS
	// factory literal and the mux mounting, and NOTHING in it is
	// reachable from user-owned code. This one file is the single
	// largest source of forks in the fleet. Each miss is named
	// separately so a reader can quote the exact gap in an issue.
	if isCmdCmdDir(dir) {
		switch base {
		case "serve.go", "server.go":
			return noSeam("the Connect interceptor chain (there is no infra-aware `Interceptors` hook, " +
				"and `observe.Chain` appends `Extras` LAST so no user interceptor can wrap forge's own), " +
				"the CORS factory (`serverkit.Server.CORSMiddleware` is a real field, but the generated " +
				"literal is the only writer), project-level extra HTTP routes (no `ExtraRoutes`; only " +
				"per-service `RegisterHTTP`), and the `AuthDeps` literal (`AnonymousOK` is hardcoded)")
		case "version.go", "db.go", "root.go":
			return seamAt("cmd/<bin>/cmd/commands.go", "userCommands — add cobra subcommands there")
		}
		// register/group anchors for services, workers and operators.
		if strings.HasSuffix(base, "_register.go") || strings.HasSuffix(base, "_group.go") {
			return seamAt("cmd/<bin>/cmd/commands.go", "userCommands — the per-component anchors are a projection of the discovered component set")
		}
	}

	// ── internal/app/ — composition and mounting ──────────────────────
	if dir == "internal/app" {
		switch base {
		case "mounts_services_gen.go", "mounts_services.go":
			// Two answers, because there are two cases: a route that
			// belongs to a service has a seam; a bare project-level route
			// has none.
			return seamAt("internal/handlers/<svc>/service.go", "RegisterHTTP — mount extra HTTP routes on the service that owns them") +
				". For a route with NO owning service, " + noSeam("project-level route mounting (`ExtraRoutes`)")
		case "auth.go":
			return seamAt("internal/app/auth.go", "SetupAuth — this file is scaffold-once and already yours; forge never overwrites it")
		}
	}

	// ── pkg/app/ ──────────────────────────────────────────────────────
	if dir == "pkg/app" {
		switch base {
		case "bootstrap.go", "app_gen.go", "wire_gen.go":
			// Retired DI unit: the live composition moved to internal/app
			// (OpenInfra → NewComponents). A project carrying these legacy
			// files belongs at the new homes — owned infra in providers.go,
			// explicit component wiring in compose.go.
			return "custom wiring belongs in internal/app/providers.go (OpenInfra) + internal/app/compose.go (NewComponents) — the retired pkg/app DI unit no longer runs"
		case "testing.go":
			// This file is RETIRED. It was a non-test .go file importing
			// "testing" in a package cmd/<proj> imports, so it linked
			// package `testing` — and the flags it registers in init() —
			// into every scaffolded project's production binary. forge
			// deletes it on regenerate when it is still forge's own render;
			// a hand-edited copy is the user's, so say what to do with it.
			return "RETIRED — this file put package `testing` in your production binary. " +
				"Its per-service replacement is internal/handlers/<svc>/helpers_gen_test.go " +
				"(a _test.go file, so it never reaches cmd/). Move anything you added there, " +
				"then delete this file; entity factories now live beside the handler that " +
				"owns them, in internal/handlers/<svc>/factories_gen_test.go"
		case "factory_gen.go":
			// Same leak, same retirement: the typed entity factories moved
			// beside the handler package whose tests call them, as a
			// _test.go file the toolchain keeps out of cmd/ by construction.
			return "RETIRED — moved to internal/handlers/<svc>/factories_gen_test.go so `testing` " +
				"stays out of your production binary. The factories are now in that handler " +
				"package itself, so its own tests call New<Entity> unqualified (drop the " +
				"import); delete this file"
		}
	}

	// ── Derived output: the source of truth is upstream ───────────────
	if strings.HasPrefix(dir, "internal/handlers/") {
		switch base {
		case "handlers_gen.go", "mock_gen.go", "handlers_crud_gen.go":
			return "regenerate from contract.go / proto instead of editing — this file is derived output"
		}
	}
	if strings.HasSuffix(base, "_orm_gen.go") { // in any database.orm.package directory
		return "edit the migration + proto that derive it — this file is a projection of the schema, not a source of truth"
	}
	if dir == "pkg/config" && (base == "config_gen.go" || base == "config.go") {
		return seamAt("forge.yaml", "config: / environments[].config — the generated struct is a projection of it")
	}
	if strings.HasPrefix(rel, "frontends/") {
		return "regenerate from proto instead of editing — generated frontend files (hooks, mocks, dashboards, nav) are a projection of the service surface; " +
			"hand-written pages and components alongside them are yours and are never overwritten"
	}
	if strings.HasPrefix(rel, ".claude/skills/") || strings.HasPrefix(rel, "deploy/") {
		return "edit the upstream source instead — forge no longer delivers skills (a leftover copy is retired on generate) and deploy manifests are rendered from forge.yaml + KCL"
	}

	// ── Anything else ─────────────────────────────────────────────────
	//
	// Reaching here means forge Tier-1-owns a path this table has never
	// been taught about. That is itself worth reporting: the honest
	// answer is "forge has no seam here", not silence.
	return noSeam("this file")
}

// isCmdCmdDir reports whether dir is the per-binary command tree,
// cmd/<bin>/cmd. The binary name is project-chosen, so the shape is
// matched structurally rather than by a name list.
func isCmdCmdDir(dir string) bool {
	parts := strings.Split(dir, "/")
	return len(parts) == 3 && parts[0] == "cmd" && parts[2] == "cmd"
}

// tier1DriftSummaryLine renders the single-line drift summary that
// leads the stomp-guard error. The full report follows on later lines,
// but agent harnesses, log pipelines, and wrap-and-rethrow callers
// routinely surface only an error's FIRST line — journey fr-a04f8c0609
// saw a bare 'Tier-1 file-stomp guard:' with no file and no remedy
// because everything actionable lived below the first newline. The
// first line must therefore stand alone: name the files and the escape
// hatches.
//
// The inline path list is capped so the line stays a line; the full
// set is always in the report body below.
func tier1DriftSummaryLine(drift []checksums.Tier1DriftEntry) string {
	const maxInline = 8
	paths := make([]string, 0, len(drift))
	for _, d := range drift {
		paths = append(paths, d.Path)
	}
	more := ""
	if len(paths) > maxInline {
		more = fmt.Sprintf(", … +%d more", len(paths)-maxInline)
		paths = paths[:maxInline]
	}
	return fmt.Sprintf("%d hand-edited Tier-1 file(s): %s%s — move each edit to the extension point named below, or report the gap if the report says none exists; `--force` discards the edits, `forge project disown <path> --reason \"<why>\"` records the gap and freezes the file forever (never an end state); details below",
		len(drift), strings.Join(paths, ", "), more)
}

// formatTier1DriftReport renders the batched stomp-guard error body.
// Extracted from stepCheckTier1Drift so (a) the explain-drift path can
// reuse the identical report and (b) the message can be pinned by unit
// tests without spinning up a pipeline context.
//
// Message design: the extension point leads, the disown door trails.
// The old ordering taught the fork escape hatch as the path of least
// resistance, and agents dutifully took it — permanently losing
// regeneration for the file (.forge/backlog.md 2026-06-03/05). Disown
// stays documented, but as what it is: a one-way transfer to permanent
// user ownership.
func formatTier1DriftReport(drift []checksums.Tier1DriftEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d Tier-1 file(s) modified after last `forge generate`:\n\n", len(drift))
	for _, d := range drift {
		fmt.Fprintf(&b, "  • %s\n", d.Path)
		if d.Unverified {
			// Legacy-manifest migration sentinel: the bytes matched
			// nothing the dead manifest recorded AND no fresh render —
			// forge cannot prove these bytes are its own output.
			fmt.Fprintf(&b, "      embedded: %s (provenance unknown since the legacy checksums.json migration)\n", checksums.UnverifiedMarkerValue)
			fmt.Fprintf(&b, "      current:  %s\n", short(d.OnDiskHash))
		} else {
			fmt.Fprintf(&b, "      embedded: %s (the hash stamped at the last forge write)\n", short(d.RecordedHash))
			fmt.Fprintf(&b, "      current:  %s (recomputed from the file's bytes — the file changed after forge wrote it)\n", short(d.OnDiskHash))
		}
		if hint := tier1ExtensionPointHint(d.Path); hint != "" {
			fmt.Fprintf(&b, "      ↪ %s\n", hint)
		}
	}
	fmt.Fprintf(&b, "\nTier-1 files carry the `// Code generated by forge ... DO NOT EDIT.` banner — forge owns them and regenerates them every run. Options, in order of preference:\n")
	fmt.Fprintf(&b, "  1. Move the customization to the designated extension point named above (user-owned; survives every regenerate) — or any Tier-2 file — then revert the generated file (`git checkout -- <path>`) and re-run.\n")
	fmt.Fprintf(&b, "  2. If the line above says NO EXTENSION POINT EXISTS, stop and report the named gap. That is a forge defect, and the fix is a seam in forge — not a fork in your project.\n")
	fmt.Fprintf(&b, "  3. Re-run with `--explain-drift` to see a diff of each drifted file against a fresh render of the current templates before deciding.\n")
	fmt.Fprintf(&b, "  4. Re-run with `--force` to discard your edits and regenerate from the current templates. `--force` is scoped to exactly the files named above and nothing else.\n")
	fmt.Fprintf(&b, "  5. `forge project disown <path> --reason \"<why>\"` records the gap and unblocks you NOW — but it is not a solution, and it is not an end state. Forge stops updating the file forever, including every future bug fix to it. A fleet-wide audit of every disown in existence found ZERO that were deliberate, permanent choices: each one was a missing seam, a migration shield, or a gap that later closed and left the fork behind. Expect to delete the entry once the seam in option 2 lands.\n")
	return b.String()
}
