package codegen

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Two writer tiers, made legible.
//
// Generated output splits into two ownership tiers, and the tier dictates the
// write mechanism:
//
//   - forge-owned: files forge stamps and re-stamps on every `forge generate`
//     (wire_gen.go, bootstrap.go, *_gen.go, the rendered CI/deploy configs).
//     These go through checksums.WriteGeneratedFile so the checksum ledger can
//     later tell "stale codegen forge may overwrite" apart from "a user edit
//     forge must preserve". Always forced here (force=true) — the generate
//     pipeline owns these paths.
//
//   - user-scaffold: one-time starting points forge writes once and then
//     leaves to the user (service.go, the per-RPC rpc_<name>.go, the local
//     k3d ingress override, etc.). These are raw os.WriteFile — deliberately
//     NOT checksum-tracked, because forge is not the steward of their later
//     contents.
//
// The distinction used to be implicit in the choice of call (WriteGeneratedFile
// vs os.WriteFile). These thin wrappers name the intent at each call site
// without changing which files are checksummed.

// writeForgeOwned writes a forge-owned, checksum-tracked file via the checksum
// ledger (force=true — the generate pipeline owns this path).
func writeForgeOwned(root, relPath string, content []byte, cs *checksums.FileChecksums) error {
	_, err := checksums.WriteGeneratedFile(root, relPath, content, cs, true)
	return err
}

// writeSourceDerived is writeForgeOwned for a file that is a pure function of
// hand-written Go beside it (a component's helpers_gen_test.go, rendered from
// its Deps struct). A pristine older render of such a file is stale by
// construction once the source moves, so it is refreshed rather than left for
// --heal — see checksums.WriteDerivedFileOutcome.
func writeSourceDerived(root, relPath string, content []byte, cs *checksums.FileChecksums) error {
	_, err := checksums.WriteDerivedFileOutcome(root, relPath, content, cs, true)
	return err
}

// writeUserScaffold writes a one-time user-scaffold file. Deliberately not
// checksum-tracked: forge writes it once and leaves later edits to the user.
//
// It IS rollback-journaled (RecordPreWriteAbs): when a `forge generate`
// run fails after writes and rewinds the tree, the scaffold files written
// this run must rewind with it — a shim that survives while the Tier-1
// file it delegates to is reverted leaves a tree that compiles against
// neither the pre-run nor the post-run world. Journaling never converts
// the file to forge stewardship: on the SUCCESS path the journal is
// dropped and later user edits remain invisible to forge.
func writeUserScaffold(path string, content []byte) error {
	checksums.RecordPreWriteAbs(path)
	return atomicfile.WriteFile(path, canonicalizeUserGo(path, content), 0o644)
}

// canonicalizeUserGo runs a user-scaffold .go write through the same canonical
// formatter the pipeline's `goimports -local <module>` pass and the scaffolded
// .golangci.yml agree on, so the bytes forge leaves behind are already a fixed
// point of the project's own formatter.
//
// WHY THE WRITE CHOKEPOINT AND NOT THE CALL SITES. Several of these writes are
// TEXT INJECTIONS into a file forge does not own the rest of: ensureDepsDBField
// splices `DB orm.Context` into the user's Deps struct, injectValidateDepsDBCheck
// splices a check into validateDeps, the import fixups splice a line before a
// closing paren. An injector cannot know the alignment of a struct whose other
// fields it did not write — `"\tDB         orm.Context\n"` was padded by hand for
// one field set and shipped misaligned Go the moment the field set differed. So
// alignment is DERIVED here, once, for every injector at once, instead of being
// authored at each site and getting it wrong at each site independently.
//
// Best-effort by the same discipline as the forge-owned writer: a path that is
// not Go, or content that does not parse, is written verbatim. A template
// emitting broken Go should fail at the pipeline's `go build ./...` step with a
// real compiler error, not be silently swallowed at the write.
func canonicalizeUserGo(path string, content []byte) []byte {
	if !strings.HasSuffix(strings.ToLower(path), ".go") {
		return content
	}
	formatted, err := checksums.CanonicalGoSource(
		checksums.GoImportsLocalPrefix(goModuleRoot(path)), path, content)
	if err != nil {
		return content
	}
	return formatted
}

// goModuleRoot walks up from path's directory to the nearest go.mod. Returns ""
// when there is none — GoImportsLocalPrefix then yields an empty prefix, which
// is exactly what a bare `goimports` pass would do.
func goModuleRoot(path string) string {
	dir := filepath.Dir(path)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// writeForgeScaffoldOnce writes a scaffold-once, USER-OWNED file addressed by
// (root, relPath): forge emits it exactly ONCE, when it does not yet exist, and
// then never regenerates or overwrites it. This is the write tier for the
// command tree + component WIRING (cmd/<bin>/main.go, internal/app/compose.go,
// internal/app/lifecycle.go, and the per-worker/operator subcommands): once
// scaffolded, the file is owned code the user hand-maintains (adding a component
// is a hand-edit / `forge scaffold` append, never a re-derivation). It creates parent
// dirs, is deliberately NOT checksum-tracked, and returns true when it wrote a
// fresh file (false when one already existed).
func writeForgeScaffoldOnce(root, relPath string, content []byte) (bool, error) {
	abs := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return false, err
	}
	return writeUserScaffoldIfAbsent(abs, content)
}

// writeUserScaffoldIfAbsent writes a one-time user-scaffold file ONLY at its
// BIRTH. Once forge has scaffolded a path, the user owns it and a later `forge
// generate` must never write it again — neither clobbering their edits nor
// resurrecting a copy they deleted. Returns true when it wrote a fresh file.
// Like writeUserScaffold, it is deliberately not checksum-tracked.
//
// Birth is decided by the ledger, not by os.Stat: see
// checksums.ScaffoldOnceDecision for why presence is a two-state answer to a
// three-state question, and why the third state (scaffolded-then-deleted) is
// the one carrying the user's intent.
func writeUserScaffoldIfAbsent(path string, content []byte) (bool, error) {
	root, rel, ok := checksums.SplitScaffoldPath(path)
	if !ok {
		// Outside any project we can key a ledger to. Fall back to the
		// historical presence check rather than refusing the write.
		if _, err := os.Stat(path); err == nil {
			return false, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	} else if !checksums.ScaffoldOnceDecision(root, rel) {
		return false, nil // exists, deliberately deleted, or unreadable
	}
	// Journal the (absent) pre-run state so a failed pipeline run deletes
	// this file along with everything else it wrote — see writeUserScaffold.
	checksums.RecordPreWriteAbs(path)
	if err := atomicfile.WriteFile(path, canonicalizeUserGo(path, content), 0o644); err != nil {
		return false, err
	}
	if ok {
		checksums.RecordScaffold(root, rel)
	}
	return true, nil
}
