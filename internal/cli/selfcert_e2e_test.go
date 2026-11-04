//go:build e2e

// Red-first proofs for the self-certifying generated-file redesign
// (mission O1): generated Tier-1 files embed their own content hash in
// the DO-NOT-EDIT header (`forge:hash=<sha256>`), the pristine check is
// purely local (recompute vs embedded), and the global
// .forge/checksums.json manifest dies for Tier-1.
//
// The disease these tests pin (kalshi fr-9a54388f0b, P0): the manifest
// is shared mutable state OUTSIDE the files it describes. Two lanes in
// one checkout → manifest committed from the WIP lane → on a clean
// clone of green HEAD, committed Tier-1 files match no recorded hash →
// generate hard-refuses: a green tree that cannot reproduce itself.
//
// Written FAILING FIRST against the manifest mechanism. Each test
// asserts the DESIRED post-redesign behavior:
//
//  1. TestE2ESelfCertCloneReproduces — THE kalshi scenario, made nasty:
//     scaffold → generate → hand-edit ONE generated file → commit →
//     fresh `git clone` → `forge generate` must fail naming exactly the
//     hand-edited file; after restoring it, generate must run clean.
//  2. TestE2ESelfCertParallelLaneSubsetCommit — the WIP-lane corruption:
//     green HEAD, then a second lane regenerates with extra uncommitted
//     changes and only a SUBSET (the .forge/ state) is committed. A
//     clone of HEAD must still reproduce itself cleanly.
//  3. TestE2ESelfCertLegacyManifestMigration — a project carrying a
//     legacy .forge/checksums.json (including kalshi's exact mess: an
//     entry recorded from a different lane than the committed bytes,
//     plus a real hand-edit) migrates: pristine files get stamped, the
//     hand-edit is guarded by name, and the legacy manifest is deleted.
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// sha256HexE2E returns the lowercase hex sha256 of content — the digest
// shape the legacy manifest recorded. The E2E suffix is the package's
// convention for a helper that exists only behind the e2e build tag
// (see gitE2E, freePortE2E): package cli already has an untagged
// sha256Hex in release_artifacts_test.go with a different signature, and
// an untagged build cannot see this file to notice the collision.
func sha256HexE2E(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

// gitE2E runs a git command in dir, failing the test on error. A local
// committer identity is configured per-repo so the tests don't depend
// on global git config.
func gitE2E(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=forge-e2e", "GIT_AUTHOR_EMAIL=e2e@forge.local",
		"GIT_COMMITTER_NAME=forge-e2e", "GIT_COMMITTER_EMAIL=e2e@forge.local",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// initGitRepoE2E initializes a repo and makes the initial commit of
// everything currently in the project dir. It commits with --no-verify:
// the scaffold installs the project's own pre-commit hook, which runs
// whatever hook tools this machine has (goimports over the vendored forge
// tree), and these tests are about forge's self-certification, not the
// project's hooks.
func initGitRepoE2E(t *testing.T, dir, msg string) {
	t.Helper()
	gitE2E(t, dir, "init", "-q")
	gitE2E(t, dir, "add", "-A")
	gitE2E(t, dir, "commit", "-q", "--no-verify", "-m", msg)
}

// The self-certification machinery is file-agnostic: these tests need A
// forge-owned Tier-1 file, not a particular one. Two are named as
// constants so the whole suite moves in one edit when the generated shape
// changes again — and so the WHY of each choice is written down.
//
//	tier1SelfCertGo   — the composition graph's generated route sets (the
//	                    successor to mounts_services_gen.go and, before it,
//	                    pkg/app/wire_gen.go). Path does not embed the
//	                    project or binary name.
//	tier1SelfCertOther — a NON-Go stamped file, so the "hash marker is
//	                    present in representative formats" claim is about
//	                    more than Go comment syntax.
//	tier1SelfCertGoAlt — a SECOND stamped Go file, needed wherever a case
//	                    edits/corrupts one Go file and must still name a
//	                    pristine one. Its path embeds neither the project
//	                    nor the binary name, so it is usable verbatim, and
//	                    it is git-TRACKED: the clone case reproduces a
//	                    project from committed state, so a scaffold the
//	                    .gitignore drops (pkg/middleware/*_gen.go) is not
//	                    there to assert against.
//
// All three are asserted to still BE self-certified (assertSelfCertifiedE2E)
// before anything is concluded from them: if any is retired the way
// wire_gen.go was, these tests must fail loudly rather than quietly stop
// exercising the guard. pkg/app/migrate.go used to serve as the alt; it was
// retired into forge/pkg's migratekit (its ceremony was library code parked
// in the user's tree), so the cases below moved to a file forge still writes.
const (
	tier1SelfCertGo    = "internal/app/routes_gen.go"
	tier1SelfCertOther = "deploy/kcl/config_gen.k"
	tier1SelfCertGoAlt = "db/source_gen.go"
)

// assertSelfCertifiedE2E returns rel's bytes, failing the test unless the
// file exists AND carries the embedded forge:hash marker that makes the
// stomp guard, the drift report and the rollback have anything to act on.
func assertSelfCertifiedE2E(t *testing.T, projectDir, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(projectDir, rel))
	if err != nil {
		t.Fatalf("read %s: %v\nThis test needs a forge-owned Tier-1 file. If %s was retired, re-point the tier1SelfCert* constants at a current one — do not delete the coverage.", rel, err, rel)
	}
	if !strings.Contains(string(data), "forge:hash=") {
		t.Fatalf("%s carries no forge:hash marker, so it is not self-certifying and every guard assertion below would be vacuous; re-point the tier1SelfCert* constants", rel)
	}
	return data
}

// scaffoldSelfCertProject scaffolds a minimal one-service backend
// project, wires the local forge/pkg replaces, and runs the first
// generate. Returns the project dir and the forge binary path.
func scaffoldSelfCertProject(t *testing.T, name string) (projectDir, forgeBin string) {
	t.Helper()
	forgeBin = buildforgeBinary(t)
	dir := t.TempDir()
	runCmd(t, dir, forgeBin, "project", "new", name,
		"--mod", "example.com/"+name, "--service", "api")
	projectDir = filepath.Join(dir, name)
	addCorpusForgePkgReplace(t, projectDir)
	runCmd(t, projectDir, forgeBin, "generate")
	return projectDir, forgeBin
}

// cloneProject clones the project repo into a fresh temp dir (the
// "clean checkout of green HEAD" — no .forge state beyond what's
// committed) and re-wires the local module replaces that scaffolding
// state (.forge-pkg vendor dir) doesn't travel through git.
func cloneProject(t *testing.T, projectDir string) string {
	t.Helper()
	cloneParent := t.TempDir()
	clone := filepath.Join(cloneParent, "clone")
	gitE2E(t, filepath.Dir(projectDir), "clone", "-q", projectDir, clone)
	addCorpusForgePkgReplace(t, clone)
	return clone
}

// runGenerate runs `forge generate` and returns (combined output, err).
func runGenerate(t *testing.T, projectDir, forgeBin string, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{"generate"}, extra...)
	cmd := exec.Command(forgeBin, args...)
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// tier1DriftSummaryRE matches the stomp guard's summary line
// (tier1DriftSummaryLine): the count, then the drifted paths.
var tier1DriftSummaryRE = regexp.MustCompile(`(\d+) hand-edited Tier-1 file\(s\): (.+?) — move each edit`)

// assertTier1DriftIsExactly fails unless every stomp-guard summary line in
// out names exactly the files in want, in order, and at least one does.
func assertTier1DriftIsExactly(t *testing.T, out string, want ...string) {
	t.Helper()
	matches := tier1DriftSummaryRE.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		t.Fatalf("no stomp-guard summary line (%q) in the output, so the drift set cannot be checked:\n%s",
			"N hand-edited Tier-1 file(s): …", out)
	}
	for _, m := range matches {
		if got := strings.Split(m[2], ", "); m[1] != strconv.Itoa(len(want)) || strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("the guard reports %s hand-edited file(s) %q, want exactly %q (manifest-era false positive):\n%s",
				m[1], got, want, out)
		}
	}
}

// TestE2ESelfCertCloneReproduces is THE kalshi scenario with the nasty
// twist: one generated file is hand-edited before the commit. The
// clone-side guard must name exactly that file — using only state that
// travels with the files themselves — and after restoring the pristine
// bytes the clone must regenerate cleanly with no residual complaints.
func TestE2ESelfCertCloneReproduces(t *testing.T) {
	t.Parallel()
	projectDir, forgeBin := scaffoldSelfCertProject(t, "selfcertclone")

	// Hand-edit ONE generated Tier-1 file before committing.
	const editedRel = tier1SelfCertGo
	editedPath := filepath.Join(projectDir, editedRel)
	pristine := assertSelfCertifiedE2E(t, projectDir, editedRel)
	if err := os.WriteFile(editedPath,
		append(append([]byte(nil), pristine...), []byte("\n// hand-edit: smuggled past regen\n")...),
		0o644); err != nil {
		t.Fatalf("hand-edit %s: %v", editedRel, err)
	}

	initGitRepoE2E(t, projectDir, "green-ish HEAD with one smuggled hand-edit")
	clone := cloneProject(t, projectDir)

	// Clone-side generate must fail and must name exactly the edited
	// file as the hand-edited one.
	out, genErr := runGenerate(t, clone, forgeBin)
	if genErr == nil {
		t.Fatalf("expected the stomp guard to refuse on the hand-edited %s; generate ran clean:\n%s", editedRel, out)
	}
	if !strings.Contains(out, editedRel) {
		t.Fatalf("guard error must name the hand-edited file %s; output:\n%s", editedRel, out)
	}
	// No OTHER generated file may be reported as hand-edited drift: the
	// committed tree minus the one edit is pristine by construction. The
	// guard's own drift list must be exactly the edited file. It is read
	// from the guard's summary line, not from the whole output: the guard
	// adjudicates after the emitters have run, so the rollback report that
	// follows it rightly lists every file the run wrote and reverted,
	// pristine ones included. Each name below is a file that EXISTS and is
	// stamped — a false-positive check against a file forge no longer
	// writes proves nothing.
	for _, pristine := range []string{tier1SelfCertGoAlt, "pkg/config/config_gen.go", tier1SelfCertOther} {
		assertSelfCertifiedE2E(t, clone, pristine)
	}
	assertTier1DriftIsExactly(t, out, editedRel)

	// Restore the pristine bytes in the clone; generate must now run
	// clean — a green tree reproduces itself from committed state alone.
	if err := os.WriteFile(filepath.Join(clone, editedRel), pristine, 0o644); err != nil {
		t.Fatalf("restore %s: %v", editedRel, err)
	}
	out, genErr = runGenerate(t, clone, forgeBin)
	if genErr != nil {
		t.Fatalf("clean clone of green HEAD must reproduce itself; generate failed:\n%s", out)
	}
}

// TestE2ESelfCertParallelLaneSubsetCommit reproduces the WIP-lane
// corruption that produced kalshi fr-9a54388f0b: a second lane
// regenerates with extra (uncommitted) input changes, and only the
// .forge/ bookkeeping subset of the resulting diff gets committed.
// Under the manifest mechanism the committed manifest then describes
// renders that were never committed, and a clean clone of HEAD
// hard-refuses to generate. Under self-certifying files, committed
// Tier-1 files carry their own proof of pristineness — the clone must
// regenerate cleanly no matter what stale bookkeeping was committed.
func TestE2ESelfCertParallelLaneSubsetCommit(t *testing.T) {
	t.Parallel()
	projectDir, forgeBin := scaffoldSelfCertProject(t, "selfcertlane")
	initGitRepoE2E(t, projectDir, "green HEAD")

	// WIP lane: add a service (changes forge.yaml + proto inputs), then
	// regenerate. Tier-1 outputs and any forge bookkeeping now describe
	// the WIP shape.
	runCmd(t, projectDir, forgeBin, "scaffold", "service", "billing")
	if out, err := runGenerate(t, projectDir, forgeBin); err != nil {
		t.Fatalf("WIP-lane generate failed: %v\n%s", err, out)
	}

	// Commit ONLY the .forge/ state (the manifest-era "bookkeeping
	// commit"), leaving every other WIP change uncommitted — the exact
	// subset-commit mistake from the kalshi incident. If nothing under
	// .forge/ changed (the post-redesign steady state), that absence is
	// itself the fix; commit whatever IS there to keep the scenario
	// honest.
	gitE2E(t, projectDir, "add", "-f", ".forge")
	staged := gitE2E(t, projectDir, "diff", "--cached", "--name-only")
	if strings.TrimSpace(staged) != "" {
		gitE2E(t, projectDir, "commit", "-q", "--no-verify", "-m", "bookkeeping from WIP lane")
	}

	// A clean clone of HEAD: the committed Tier-1 files are the green
	// scaffold's pristine renders; the committed bookkeeping (if any)
	// came from the WIP lane. Generate must run clean.
	clone := cloneProject(t, projectDir)
	out, genErr := runGenerate(t, clone, forgeBin)
	if genErr != nil {
		t.Fatalf("green HEAD cannot reproduce itself after a WIP-lane bookkeeping commit (the fr-9a54388f0b disease):\n%s", out)
	}
	_ = out
}

// TestE2ESelfCertLegacyManifestMigration drives the one-time, automatic
// migration off a legacy .forge/checksums.json. The fixture carries
// kalshi's exact mess:
//
//   - pristine Tier-1 files whose manifest entry is CORRUPT (hash +
//     history recorded from a different lane → match nothing), and
//   - one genuinely hand-edited Tier-1 file (manifest never updated).
//
// Desired behavior: pristine files (whether their legacy entry matches
// or not) end up stamped with an embedded forge:hash header and
// regenerate cleanly; the hand-edited file is reported loudly BY NAME
// with the standard guard remedies; the legacy manifest is deleted.
func TestE2ESelfCertLegacyManifestMigration(t *testing.T) {
	t.Parallel()
	projectDir, forgeBin := scaffoldSelfCertProject(t, "selfcertmigrate")

	manifestPath := filepath.Join(projectDir, ".forge", "checksums.json")

	// Build the legacy fixture with the CURRENT (pre-redesign) mechanism
	// when it's still in place; once the redesign lands, synthesize the
	// legacy manifest shape so the migration path stays covered forever.
	if _, err := os.Stat(manifestPath); err != nil {
		synthesizeLegacyManifest(t, projectDir)
	}
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("legacy fixture must carry .forge/checksums.json: %v", err)
	}

	// Corruption 1 (the different-lane shape): replace the recorded hash
	// AND history for a pristine file with values from "another lane" —
	// the committed bytes match nothing in the manifest.
	const corruptRel = tier1SelfCertGoAlt
	corruptManifestEntry(t, manifestPath, corruptRel)

	// Corruption 2: a real hand-edit the manifest never saw, on a stamped
	// NON-Go file (CI workflows are write-once scaffolds and buf.yaml is
	// unstamped, so neither is a valid drift-guard subject).
	const editedRel = tier1SelfCertOther
	editedPath := filepath.Join(projectDir, editedRel)
	ciBytes, err := os.ReadFile(editedPath)
	if err != nil {
		t.Fatalf("read %s: %v", editedRel, err)
	}
	if err := os.WriteFile(editedPath,
		append(append([]byte(nil), ciBytes...), []byte("\n# hand-tuned: do not regenerate me\n")...), 0o644); err != nil {
		t.Fatalf("hand-edit %s: %v", editedRel, err)
	}

	// First post-migration generate: the hand-edited file must be
	// guarded BY NAME. The pristine-but-corrupt-entry file must NOT be
	// stomp-blocked forever — it either regenerates cleanly this run or
	// is named once with a working --force remedy.
	out, genErr := runGenerate(t, projectDir, forgeBin)
	if genErr == nil {
		t.Fatalf("expected the migration/guard to refuse on hand-edited %s; generate ran clean:\n%s", editedRel, out)
	}
	if !strings.Contains(out, editedRel) {
		t.Fatalf("migration must name the hand-edited file %s; output:\n%s", editedRel, out)
	}

	// The legacy manifest must be gone (migration is one-time and
	// durable, even when the run ends in a guard refusal).
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Errorf("legacy .forge/checksums.json must be deleted by the migration (stat err=%v)", err)
	}

	// Resolve the hand-edit the sanctioned way and converge.
	out, genErr = runGenerate(t, projectDir, forgeBin, "--force")
	if genErr != nil {
		t.Fatalf("generate --force after migration must converge: %v\n%s", genErr, out)
	}
	out, genErr = runGenerate(t, projectDir, forgeBin)
	if genErr != nil {
		t.Fatalf("steady-state generate after migration must be clean: %v\n%s", genErr, out)
	}

	// Post-migration: Tier-1 files are self-certifying — the embedded
	// hash marker must be present in representative formats.
	for _, rel := range []string{tier1SelfCertGo, corruptRel, editedRel} {
		data, err := os.ReadFile(filepath.Join(projectDir, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(data), "forge:hash=") {
			t.Errorf("%s must carry an embedded forge:hash marker after migration", rel)
		}
	}
}

// corruptManifestEntry rewrites the legacy manifest entry for relPath
// so hash and history match nothing on disk — the "recorded from a
// different lane" corruption.
func corruptManifestEntry(t *testing.T, manifestPath, relPath string) {
	t.Helper()
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "\""+relPath+"\"") {
		t.Fatalf("legacy manifest has no entry for %s", relPath)
	}
	// Brute-force but shape-agnostic: replace every 64-hex digest in the
	// entry's JSON object with a fabricated one. Locate the object by
	// scanning from the key to the closing brace.
	keyIdx := strings.Index(content, "\""+relPath+"\"")
	end := strings.Index(content[keyIdx:], "}")
	if end < 0 {
		t.Fatalf("malformed manifest around %s", relPath)
	}
	seg := content[keyIdx : keyIdx+end]
	fab := strings.Repeat("deadbeef", 8)
	var out strings.Builder
	i := 0
	for i < len(seg) {
		c := seg[i]
		if isHexLower(c) {
			j := i
			for j < len(seg) && isHexLower(seg[j]) {
				j++
			}
			if j-i == 64 {
				out.WriteString(fab)
			} else {
				out.WriteString(seg[i:j])
			}
			i = j
			continue
		}
		out.WriteByte(c)
		i++
	}
	content = content[:keyIdx] + out.String() + content[keyIdx+end:]
	if err := os.WriteFile(manifestPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write corrupted manifest: %v", err)
	}
}

func isHexLower(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}

// synthesizeLegacyManifest reconstructs the legacy checksums.json shape
// for a project generated by post-redesign forge: every file carrying a
// forge:hash marker gets a manifest entry whose hash is the sha256 of
// the MARKER-STRIPPED bytes (what a legacy forge would have recorded),
// and the marker line is removed from the file (legacy projects have no
// markers). Used once the manifest mechanism no longer exists in the
// binary under test.
func synthesizeLegacyManifest(t *testing.T, projectDir string) {
	t.Helper()
	entries := map[string]string{}
	skipDirs := map[string]bool{".git": true, ".forge": true, ".forge-pkg": true,
		"node_modules": true, "gen": true, "vendor": true}
	err := filepath.Walk(projectDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if !strings.Contains(string(data), "forge:hash=") {
			return nil
		}
		// Strip the marker line wholesale.
		lines := strings.SplitAfter(string(data), "\n")
		var kept []string
		for _, l := range lines {
			if strings.Contains(l, "forge:hash=") {
				continue
			}
			kept = append(kept, l)
		}
		stripped := strings.Join(kept, "")
		if werr := os.WriteFile(path, []byte(stripped), info.Mode()); werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(projectDir, path)
		if rerr != nil {
			return rerr
		}
		entries[filepath.ToSlash(rel)] = sha256HexE2E([]byte(stripped))
		return nil
	})
	if err != nil {
		t.Fatalf("synthesize legacy fixture: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("synthesize legacy fixture: no marker-bearing files found — cannot build a legacy manifest")
	}
	var b strings.Builder
	b.WriteString("{\n  \"forge_version\": \"legacy-test\",\n  \"files\": {\n")
	first := true
	for rel, h := range entries {
		if !first {
			b.WriteString(",\n")
		}
		first = false
		b.WriteString("    \"" + rel + "\": {\"hash\": \"" + h + "\", \"history\": [\"" + h + "\"], \"tier\": 1}")
	}
	b.WriteString("\n  }\n}\n")
	if err := os.MkdirAll(filepath.Join(projectDir, ".forge"), 0o755); err != nil {
		t.Fatalf("mkdir .forge: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".forge", "checksums.json"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write synthesized manifest: %v", err)
	}
}

// TestE2EGenerateRollbackOnValidateFailure pins the stage-then-validate
// rollback (fr-40f7ec9bd9): when `forge generate` rewrites Tier-1 files
// and then its own `go build` validate step fails, the tree must be left
// EXACTLY as it was before the run — not mid-regen with recovery left to
// a `git checkout`.
//
// Construction: scaffold + generate clean + commit (green HEAD). Then
// introduce a compile error in a USER-owned file (not regenerated) and
// independently drift a Tier-1 file so a `--force` run actually rewrites
// it. The run rewrites the Tier-1 file, runs go build, and fails on the
// user file. The rollback must restore the Tier-1 file's pre-run bytes,
// so `git status` is clean except for the deliberate user-file edit.
func TestE2EGenerateRollbackOnValidateFailure(t *testing.T) {
	t.Parallel()
	projectDir, forgeBin := scaffoldSelfCertProject(t, "genrollback")
	initGitRepoE2E(t, projectDir, "green HEAD")

	// 1. Drift a Tier-1 file so the upcoming --force run rewrites it.
	const tier1Rel = tier1SelfCertGo
	tier1Path := filepath.Join(projectDir, tier1Rel)
	pristineTier1 := assertSelfCertifiedE2E(t, projectDir, tier1Rel)
	if err := os.WriteFile(tier1Path,
		append(append([]byte(nil), pristineTier1...), []byte("\n// drift: forces a --force rewrite\n")...),
		0o644); err != nil {
		t.Fatalf("drift %s: %v", tier1Rel, err)
	}

	// 2. Inject a compile error into a user-owned, non-regenerated file so
	//    the final `go build ./...` validate step fails AFTER the Tier-1
	//    rewrite lands.
	brokenRel := filepath.Join("internal", "rollbackbreak", "break.go")
	brokenPath := filepath.Join(projectDir, brokenRel)
	if err := os.MkdirAll(filepath.Dir(brokenPath), 0o755); err != nil {
		t.Fatalf("mkdir for broken file: %v", err)
	}
	if err := os.WriteFile(brokenPath,
		[]byte("package rollbackbreak\n\nfunc Broken() { this is not valid go }\n"), 0o644); err != nil {
		t.Fatalf("write broken user file: %v", err)
	}

	// Commit the green-plus-deliberate-edits state so `git status` after
	// the run reflects ONLY what the failed generate left behind.
	gitE2E(t, projectDir, "add", "-A")
	gitE2E(t, projectDir, "commit", "-q", "--no-verify", "-m", "deliberate drift + broken user file")

	// 3. Run generate --force: it rewrites the drifted Tier-1 file, then
	//    fails go build on the broken user file.
	out, genErr := runGenerate(t, projectDir, forgeBin, "--force")
	if genErr == nil {
		t.Fatalf("generate should have failed its validate step on the broken user file; it succeeded:\n%s", out)
	}

	// 4. The rollback must have reverted the Tier-1 rewrite. git status
	//    must show NO changes (the broken file + drift were committed; the
	//    Tier-1 file is back to its committed bytes).
	status := gitE2E(t, projectDir, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("tree must be clean after a rolled-back generate (no mid-regen residue); git status:\n%s\n\ngenerate output:\n%s", status, out)
	}

	// 5. The Tier-1 file specifically must be byte-identical to its
	//    committed (drifted) bytes — the run never converted it to the
	//    fresh render that briefly landed mid-run.
	afterTier1, err := os.ReadFile(tier1Path)
	if err != nil {
		t.Fatalf("read %s after rollback: %v", tier1Rel, err)
	}
	if !strings.Contains(string(afterTier1), "// drift: forces a --force rewrite") {
		t.Fatalf("%s was not rolled back to its pre-run (drifted) state after validate failure", tier1Rel)
	}

	// 6. The user must be told the tree was reverted (loud, not silent).
	if !strings.Contains(out, "reverted") && !strings.Contains(out, "pre-run state") {
		t.Errorf("rollback should announce the revert to the user; output:\n%s", out)
	}
}
