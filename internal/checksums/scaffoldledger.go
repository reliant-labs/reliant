// The scaffold-once BIRTH LEDGER — the record that makes "written once"
// mean once, including across a deletion.
//
// FRICTION: a generated CRUD lifecycle test (handlers_crud_test.go) whose
// own banner reads "yours: scaffolded once, never touched again — forge
// will not overwrite this file" came BACK three times in one session. The
// author had hardened their migrations with CHECK constraints, the
// scaffolded lifecycle test did not satisfy them, and they deleted it.
// Every subsequent `forge generate` wrote it again (15:44, 15:48, 15:49).
//
// The cause was the shape of the guard, not the guard's placement. Every
// scaffold-once writer in forge asked ONE question:
//
//	if _, err := os.Stat(path); err == nil { return }   // exists → skip
//
// which implements "restore if absent", not "write once". Existence is a
// two-state answer to a THREE-state question:
//
//	never scaffolded      → write it (birth)
//	scaffolded, present   → leave it (the user's bytes)
//	scaffolded, DELETED   → leave it DELETED (also the user's decision)
//
// The third state is invisible to os.Stat: a deleted file and a file that
// was never born look identical on disk. Deleting is a legitimate act of
// ownership — an author who does not want forge's lifecycle test should be
// able to remove it and have it stay removed — so the missing state is the
// one that carries the user's intent, and forge was silently overriding it.
//
// This ledger supplies the third state: the set of project-relative paths
// forge has EVER scaffolded. Present in the ledger + absent on disk =
// deliberately deleted, and forge does not write it again.
//
// ── Why a new state file, given the package doc says scaffold files need
// no record ───────────────────────────────────────────────────────────
//
// That claim (see the package comment in checksums.go, and migrate.go's
// DroppedTier2) is exactly the assumption this defect disproves. The
// existing ledgers cannot answer the question:
//
//   - .forge/disowned.json records a DIFFERENT, opposite decision ("forge
//     owns this path but I am taking it") and is keyed to Tier-1 paths.
//   - .forge/hashes/ holds render hashes for comment-incapable Tier-1
//     outputs only; scaffold-once files carry no marker and no hash by
//     design, so there is nothing to look them up by.
//   - the forge:hash marker scan reads certification out of the FILE, and
//     a deleted file has no bytes to read.
//   - the rollback journal is per-run and dropped on commit.
//
// Every one of those is keyed on something the deleted file would have to
// still exist to provide. So the record has to be external, and it has to
// be committed — a teammate cloning the repo must inherit "we removed the
// lifecycle test", not re-acquire it on their first generate.
//
// It is deliberately PATHS ONLY — no hashes, no content, no history. The
// manifest-era failure this package spent a release escaping was a global
// hash ledger that churned on every run and made 22 of 69 commits pure
// bookkeeping. This file changes only when a scaffold is BORN or a
// scaffolded path is deliberately re-adopted, which is a handful of lines
// at project birth and silence forever after.
//
// ── Re-scaffolding on purpose ─────────────────────────────────────────
//
// Deleting the file is no longer the reset (that is the whole point), so
// the reset is dropping the ENTRY and writing the file again: `forge
// project rescaffold <path>` (internal/cli/rescaffold.go). The file is
// human-readable, sorted, and committed, so the resulting diff — one ledger
// line out and back, one file re-created — is an ordinary reviewable change.
package checksums

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// ScaffoldedDir holds the birth ledger, ONE FILE PER PATH: the record of
// internal/app/compose.go is .forge/scaffolded/internal/app/compose.go.json
// (ScaffoldEntryPath). Committed (see the .gitignore negation beside
// disowned.json / hashes/): the decision to delete a scaffold belongs to the
// repository, not to one developer's working copy.
//
// One file per path, not one shared file: every branch that scaffolds
// anything records births, and with one sorted file two branches' births
// were neighbouring insertions in it, which git (and GitHub's server-side
// merge, which runs no merge driver) reported as a conflict. A path's entry
// now changes only when that path is born, declined or re-adopted, so two
// branches conflict here only over the same path.
const ScaffoldedDir = ".forge/scaffolded"

// LegacyScaffoldedFile is the one-file ledger ScaffoldedDir replaces. The
// load still reads it, and the next save writes its entries out and deletes
// it, so one `forge generate` migrates a project.
const LegacyScaffoldedFile = ".forge/scaffolded.json"

// scaffoldEntrySuffix ends every entry file's name.
const scaffoldEntrySuffix = ".json"

// ScaffoldEntryPath is the project-relative entry file recording relPath.
func ScaffoldEntryPath(relPath string) string {
	return ScaffoldedDir + "/" + slashKey(relPath) + scaffoldEntrySuffix
}

// ScaffoldEntryTarget is the inverse of ScaffoldEntryPath: the path an
// entry file at entryRel records, and whether entryRel is one.
func ScaffoldEntryTarget(entryRel string) (string, bool) {
	rest, ok := strings.CutPrefix(slashKey(entryRel), ScaffoldedDir+"/")
	if !ok || !strings.HasSuffix(rest, scaffoldEntrySuffix) {
		return "", false
	}
	target := strings.TrimSuffix(rest, scaffoldEntrySuffix)
	if target == "" || path.Clean(target) != target || strings.HasPrefix(target, "../") {
		return "", false
	}
	return target, true
}

// ScaffoldEntry is one path's birth record. A plain birth records nothing
// but its existence: its entry file holds `{}`. It used to carry the birth's
// timestamp, which nothing branched on and which made the record a function
// of the clock: two branches that each scaffolded the same new path wrote
// two different entries for it, and conflicted.
type ScaffoldEntry struct {
	// BirthHash is the content hash forge wrote, recorded only for the few
	// scaffolds that keep refreshing while untouched (see
	// RecordScaffoldWithHash / ScaffoldIsPristine). Empty for the ordinary
	// write-once scaffolds, which never need to know whether the file still
	// matches what forge produced.
	BirthHash string `json:"birth_hash,omitempty"`
	// Declined, when set, is the project's reason for declaring that forge
	// must never scaffold this path (`forge project disown <path> --absent`).
	//
	// The ledger already treats "recorded + absent" as "leave it absent",
	// but that state was reachable only by a BIRTH that survived its run: a
	// path whose birth broke the generate it was born in (reliant's serve.go,
	// which switched composition on mid-migration) was rolled back with its
	// record every time, so there was never a record to delete it against,
	// and the only escape was hand-writing JSON. A declined entry is that
	// record written on purpose, by a command outside generate — so a failed
	// run's rollback, which restores this file to its pre-run bytes, keeps it.
	//
	// It is a declaration, not a deletion: AbsentScaffolds does not report
	// it (nothing went missing), and `forge project rescaffold <path>`
	// re-adopts the path by dropping the entry, exactly as for a deletion.
	Declined string `json:"declined,omitempty"`
	// AbsentReported marks a path that was absent at the last run that
	// reported the absent set (AbsentScaffoldsIfChanged): what turns the
	// absent-scaffolds notice from a standing banner into an EVENT. It lives
	// on the path's own entry so that two branches deleting different
	// scaffolds change different entry files.
	AbsentReported bool `json:"absent_reported,omitempty"`
}

// IsLocalOnlyScaffold reports whether relPath is a scaffold-once file that is
// gitignored by construction — a developer's secret store, `secrets/<env>.yaml`
// (the scaffolded .gitignore ignores `secrets/`; projects that narrow it use
// `secrets/*.yaml`).
//
// The ledger is committed; such a file is not. Recording it would make the
// committed ledger a function of one checkout's untracked state: a clean
// clone and a developer's tree would write different bytes from the same
// commit. And the ledger's "recorded but absent means deliberately deleted"
// rule is meaningless for a file that never reaches a clone — it would
// leave a fresh checkout without the store it needs. So these paths are
// never recorded, never reported absent, and their birth is decided by
// presence alone.
func IsLocalOnlyScaffold(relPath string) bool {
	rel := filepath.ToSlash(relPath)
	dir, name := path.Split(rel)
	return dir == "secrets/" && strings.HasSuffix(name, ".yaml") && len(name) > len(".yaml")
}

func withoutLocalOnly(paths []string) []string {
	var out []string
	for _, p := range paths {
		if !IsLocalOnlyScaffold(p) {
			out = append(out, p)
		}
	}
	return out
}

// PersistLedgerMigrations rewrites root's ledger when the copy on disk is
// in a shape this forge no longer writes: the legacy one-file ledger
// (LegacyScaffoldedFile), or entries IsLocalOnlyScaffold forbids. Every save
// converges both, but a save happens only when a run records a birth, so a
// project whose upgrade run scaffolded nothing kept its legacy ledger, and
// the first pull request to scaffold anything afterwards carried the whole
// migration. `forge generate` calls this on every successful run, so the
// upgrade run itself migrates. A ledger already in the current shape is not
// touched.
func PersistLedgerMigrations(root string) {
	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	led, reported, _ := readScaffoldLedger(abs)
	dirty := len(withoutLocalOnly(reported)) != len(reported)
	if _, serr := os.Stat(filepath.Join(abs, filepath.FromSlash(LegacyScaffoldedFile))); serr == nil {
		dirty = true
	}
	for p := range led {
		if IsLocalOnlyScaffold(p) {
			dirty = true
		}
	}
	if !dirty {
		return
	}
	loadScaffoldLedgerLocked(abs)
	saveScaffoldLedgerLocked(abs)
}

// scaffoldedJSON is the legacy one-file shape (LegacyScaffoldedFile), read
// until the next save migrates it.
type scaffoldedJSON struct {
	ForgeVersion string                   `json:"forge_version,omitempty"`
	Files        map[string]ScaffoldEntry `json:"files"`
	// AbsentReported is the absent set as of the last run that reported
	// it. It is what turns the absent-scaffolds notice from a standing
	// banner into an EVENT — see AbsentScaffoldsIfChanged.
	//
	// It lives here rather than in a sibling file because it is a fact
	// about the same decisions this ledger already records, and because a
	// second state file is a second thing to gitignore, migrate and
	// explain. It is committed for the same reason the ledger is: the
	// deletion belongs to the repository, so a teammate who clones after
	// the notice was shown should not be shown it again for a set nobody
	// changed.
	AbsentReported []string `json:"absent_reported,omitempty"`
}

// scaffoldLedgers memoizes the per-root ledger for the life of the process,
// keyed by absolute project root. Scaffold writers fire many times per run
// and several of them (the internal/codegen raw writers) hold only a joined
// absolute path, so the load has to be lazy and cached rather than threaded
// through every signature.
var scaffoldLedgers = map[string]map[string]ScaffoldEntry{}

// scaffoldAbsentReported memoizes the per-root "absent set as last
// reported" list, loaded and saved alongside the ledger it lives in.
// Guarded by scaffoldMu, like scaffoldLedgers.
var scaffoldAbsentReported = map[string][]string{}

// scaffoldMu guards scaffoldLedgers AND the per-root ledger maps it hands
// out. loadScaffoldLedger returns the live map rather than a copy, so the
// mutation its callers perform is a mutation of shared process state; with
// `forge generate` fanning scaffold writers out across goroutines that is a
// genuine data race. Every exported entry point below takes this lock and
// calls the *Locked helpers, which must never take it themselves.
var scaffoldMu sync.Mutex

// ResetScaffoldLedgerCache drops the memoized ledgers so the next query
// re-reads from disk. Called at the head of a pipeline run (a long-lived
// process must not answer from a previous invocation's cache) and by tests
// that build several projects in one binary.
func ResetScaffoldLedgerCache() {
	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	scaffoldLedgers = map[string]map[string]ScaffoldEntry{}
	scaffoldAbsentReported = map[string][]string{}
	scaffoldOnDisk = map[string]map[string][]byte{}
}

// loadScaffoldLedger returns root's ledger, reading it from disk on first
// use. A missing or unparseable file yields an EMPTY ledger rather than an
// error: the ledger's only job is to suppress a re-write, so the failure
// mode of an unreadable ledger must be forge's historical behavior
// (scaffold it), never a refusal to scaffold a project that has no ledger.
func loadScaffoldLedgerLocked(root string) map[string]ScaffoldEntry {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	if led, ok := scaffoldLedgers[abs]; ok {
		return led
	}
	led, reported, onDisk := readScaffoldLedger(abs)
	// Entries an older forge committed for local-only paths are dropped in
	// memory here and leave the ledger at the next save (PersistLedgerMigrations
	// forces one), so a project converges on its next generate.
	for p := range led {
		if IsLocalOnlyScaffold(p) {
			delete(led, p)
		}
	}
	scaffoldAbsentReported[abs] = withoutLocalOnly(reported)
	scaffoldOnDisk[abs] = onDisk
	scaffoldLedgers[abs] = led
	return led
}

// scaffoldOnDisk memoizes, per root, the bytes of every entry file as last
// read or written, so a save rewrites only the entries that changed.
// Guarded by scaffoldMu.
var scaffoldOnDisk = map[string]map[string][]byte{}

// readScaffoldLedger reads abs's ledger from disk: the legacy one-file
// ledger, if any, then every entry file under ScaffoldedDir (an entry file
// is newer than the legacy file by construction — the save that wrote it
// deleted the legacy file — so it wins). onDisk maps each entry file read to
// its bytes. A missing or unparseable record yields no entry rather than an
// error: the ledger's only job is to suppress a re-write, so the failure
// mode of an unreadable ledger must be forge's historical behavior (scaffold
// it), never a refusal to scaffold a project that has no ledger.
func readScaffoldLedger(abs string) (led map[string]ScaffoldEntry, reported []string, onDisk map[string][]byte) {
	led = map[string]ScaffoldEntry{}
	onDisk = map[string][]byte{}
	reportedSet := map[string]bool{}
	if data, rerr := os.ReadFile(filepath.Join(abs, filepath.FromSlash(LegacyScaffoldedFile))); rerr == nil {
		var s scaffoldedJSON
		if json.Unmarshal(data, &s) == nil {
			for p, e := range s.Files {
				led[slashKey(p)] = e
			}
			for _, p := range s.AbsentReported {
				reportedSet[slashKey(p)] = true
			}
		}
	}
	dir := filepath.Join(abs, filepath.FromSlash(ScaffoldedDir))
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(abs, p)
		if rerr != nil {
			return nil
		}
		target, ok := ScaffoldEntryTarget(rel)
		if !ok {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		var e ScaffoldEntry
		if json.Unmarshal(data, &e) != nil {
			return nil
		}
		onDisk[filepath.ToSlash(rel)] = data
		reportedSet[target] = e.AbsentReported
		e.AbsentReported = false
		led[target] = e
		return nil
	})
	for p, on := range reportedSet {
		if on {
			reported = append(reported, p)
		}
	}
	sort.Strings(reported)
	return led, reported, onDisk
}

// saveScaffoldLedger persists root's ledger: it makes ScaffoldedDir hold
// exactly one entry file per recorded path, rewriting only the entries that
// changed, removing those of paths no longer recorded (and the directories
// that empties), and deleting the legacy one-file ledger. An empty ledger
// leaves no files at all (the same no-bookkeeping-churn rule Save applies
// to the other state). Best-effort by the same discipline as the load: a
// ledger we cannot write must not abort a write that already succeeded.
func saveScaffoldLedgerLocked(root string) {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	if scaffoldDeferred[abs] > 0 {
		scaffoldDeferredDirty[abs] = true
		return
	}
	led := scaffoldLedgers[abs]
	reported := map[string]bool{}
	for _, p := range scaffoldAbsentReported[abs] {
		reported[p] = true
	}
	onDisk := scaffoldOnDisk[abs]
	if onDisk == nil {
		_, _, onDisk = readScaffoldLedger(abs)
	}
	want := make(map[string][]byte, len(led))
	for p, e := range led {
		e.AbsentReported = reported[p]
		data, merr := json.Marshal(e)
		if merr != nil {
			continue
		}
		want[ScaffoldEntryPath(p)] = append(data, '\n')
	}
	// The ledger is committed state and a write the run made, like the
	// disown record (saveStateFile): journal every write and removal, so a
	// failed generate restores it with everything else instead of leaving
	// the run's births recorded for files the rollback deleted.
	entries := make([]string, 0, len(want))
	for rel := range want {
		entries = append(entries, rel)
	}
	sort.Strings(entries)
	for _, rel := range entries {
		if have, ok := onDisk[rel]; ok && bytes.Equal(have, want[rel]) {
			continue
		}
		full := filepath.Join(abs, filepath.FromSlash(rel))
		RecordPreWriteAbs(full)
		if os.MkdirAll(filepath.Dir(full), 0o755) != nil {
			continue
		}
		if atomicfile.WriteFile(full, want[rel], 0o644) == nil {
			onDisk[rel] = want[rel]
		}
	}
	dirRoot := filepath.Join(abs, filepath.FromSlash(ScaffoldedDir))
	for rel := range onDisk {
		if _, keep := want[rel]; keep {
			continue
		}
		full := filepath.Join(abs, filepath.FromSlash(rel))
		RecordPreWriteAbs(full)
		if err := os.Remove(full); err == nil || os.IsNotExist(err) {
			delete(onDisk, rel)
			pruneEmptyDirs(filepath.Dir(full), dirRoot)
		}
	}
	scaffoldOnDisk[abs] = onDisk
	legacy := filepath.Join(abs, filepath.FromSlash(LegacyScaffoldedFile))
	if _, serr := os.Stat(legacy); serr == nil {
		RecordPreWriteAbs(legacy)
		_ = os.Remove(legacy)
		if len(want) > 0 {
			migrateGitignoreLine(abs, "!"+LegacyScaffoldedFile, "!"+ScaffoldedDir+"/")
		}
	}
}

// scaffoldDeferred counts the open DeferScaffoldLedger batches per root, and
// scaffoldDeferredDirty marks roots whose ledger changed while one was open.
// Guarded by scaffoldMu.
var (
	scaffoldDeferred      = map[string]int{}
	scaffoldDeferredDirty = map[string]bool{}
)

// DeferScaffoldLedger holds back root's ledger writes until the returned
// function runs, then writes the ledger once if anything changed.
//
// Every birth record rewrites the whole file (marshal, temp file, fsync,
// rename), so a scaffold that records N files writes it N times — quadratic
// in the project's size, and for a frontend's few hundred files most of the
// time `forge project new` spends. The in-memory ledger is authoritative the
// whole time; only the file waits. A process that dies inside the batch loses
// the records of the scaffold it was writing, which is the state a
// half-written scaffold is already in.
//
// Batches nest. Safe to call from any goroutine.
func DeferScaffoldLedger(root string) (flush func()) {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	scaffoldMu.Lock()
	scaffoldDeferred[abs]++
	scaffoldMu.Unlock()
	return func() {
		scaffoldMu.Lock()
		defer scaffoldMu.Unlock()
		if scaffoldDeferred[abs]--; scaffoldDeferred[abs] > 0 {
			return
		}
		delete(scaffoldDeferred, abs)
		dirty := scaffoldDeferredDirty[abs]
		delete(scaffoldDeferredDirty, abs)
		// A cache reset inside the batch dropped the ledger the dirty mark
		// was about; writing the (now empty) memo would delete the file.
		if _, loaded := scaffoldLedgers[abs]; dirty && loaded {
			saveScaffoldLedgerLocked(abs)
		}
	}
}

// ScaffoldRecorded reports whether forge has EVER scaffolded relPath in
// this project. Combined with the file's presence on disk it resolves the
// three-state question in the file doc:
//
//	!recorded            → never scaffolded: write it
//	recorded && present  → the user's bytes: leave them
//	recorded && absent   → the user deleted it, or declined it
//	                       (ScaffoldEntry.Declined): leave it absent
func ScaffoldRecorded(root, relPath string) bool {
	if IsLocalOnlyScaffold(relPath) {
		return false
	}
	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	_, ok := loadScaffoldLedgerLocked(root)[filepath.ToSlash(relPath)]
	return ok
}

// RecordScaffold marks relPath as scaffolded and persists the ledger
// immediately.
//
// Immediate persistence (rather than a save hook at the end of the run) is
// deliberate: scaffold-once writes happen from FOUR entry points — the
// generate pipeline, `forge new`, `forge project upgrade`, and the
// `forge scaffold ...` commands — and only the first has a save hook. A
// ledger that recorded a birth but persisted it only on the pipeline path
// would leave every other path with the old restore-if-absent behavior,
// which is the defect this file exists to close.
//
// Idempotent: re-recording a known path neither changes the entry nor
// rewrites the file, so a steady-state `forge generate` produces no ledger
// diff.
func RecordScaffold(root, relPath string) {
	if IsLocalOnlyScaffold(relPath) {
		return
	}
	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	rel := filepath.ToSlash(relPath)
	led := loadScaffoldLedgerLocked(root)
	if _, ok := led[rel]; ok {
		return // already recorded — no churn
	}
	led[rel] = ScaffoldEntry{}
	noteBirthLocked(rel)
	saveScaffoldLedgerLocked(root)
}

// scaffoldBirths is the set of paths whose birth record was CREATED while
// the rollback journal is armed — the scaffolds this generate run brought
// into the world. nil when journaling is off. Guarded by scaffoldMu.
//
// It exists so a failed run can keep the file/record pair whole (see
// ReconcileScaffoldBirths) and so the failure report can name the scaffolds
// it removed again, with the one command that declines them.
var scaffoldBirths map[string]bool

func noteBirthLocked(rel string) {
	if rollbackJournal == nil {
		return
	}
	if scaffoldBirths == nil {
		scaffoldBirths = map[string]bool{}
	}
	scaffoldBirths[rel] = true
}

// ScaffoldBirthsThisRun returns the paths whose birth this generate run
// recorded, sorted. Read it BEFORE RestoreRollbackReport, which clears it.
func ScaffoldBirthsThisRun() []string {
	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	out := make([]string, 0, len(scaffoldBirths))
	for p := range scaffoldBirths {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// resetScaffoldBirths drops the per-run birth set. Called wherever the
// journal is armed or dropped.
func resetScaffoldBirths() {
	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	scaffoldBirths = nil
}

// reconcileScaffoldBirths makes every path born this run agree with its
// birth record after a rollback: a scaffold-once file is never half-recorded.
//
// The rollback deletes the run's new files and restores the ledger file to
// its pre-run bytes, which normally leaves each birth wholly undone. Either
// half can fail on its own, though, and each failure is a different lie:
//
//   - the file could not be removed but its record was: the file is on disk
//     and unrecorded, so forge would read the user's later deletion of it as
//     "never scaffolded" and write it again;
//   - the file was removed but the ledger could not be restored: the record
//     says "scaffolded, then deleted on purpose", so forge would never write
//     the file at all — the failed run consumed the path's only birth.
//
// So the record follows the file: present files are recorded, absent ones
// this run recorded are forgotten. Records that predate the run (births of
// earlier runs, and declined entries) are not in births and are untouched.
func reconcileScaffoldBirths(root string, births []string) {
	for _, rel := range births {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		switch {
		case err == nil:
			RecordScaffold(root, rel)
		case os.IsNotExist(err):
			ForgetScaffold(root, rel)
		}
	}
}

// DeclineScaffold records that the project declines relPath: forge must
// never scaffold it here. reason is the design feedback the decline carries,
// and is required (an empty reason is ignored by the caller's validation,
// never stored as a decline that reads as a deletion).
//
// An existing birth record is kept and marked, so a scaffold the user had
// deleted becomes a declared decline without losing its provenance.
func DeclineScaffold(root, relPath, reason string) {
	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	rel := filepath.ToSlash(relPath)
	led := loadScaffoldLedgerLocked(root)
	e, ok := led[rel]
	if ok && e.Declined == reason {
		return // already declined for this reason — no churn
	}
	e.Declined = reason
	led[rel] = e
	saveScaffoldLedgerLocked(root)
}

// ScaffoldDeclined reports whether the project declined relPath, and why.
func ScaffoldDeclined(root, relPath string) (reason string, ok bool) {
	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	e, found := loadScaffoldLedgerLocked(root)[filepath.ToSlash(relPath)]
	if !found || e.Declined == "" {
		return "", false
	}
	return e.Declined, true
}

// DeclinedScaffolds returns every declined path with its reason.
func DeclinedScaffolds(root string) map[string]string {
	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	out := map[string]string{}
	for p, e := range loadScaffoldLedgerLocked(root) {
		if e.Declined != "" {
			out[p] = e.Declined
		}
	}
	return out
}

// ForgetScaffold drops relPath's birth record, returning the path to the
// "never scaffolded" state so the next run scaffolds it fresh — the
// deliberate reset `forge project rescaffold` takes.
//
// A failed `forge generate` rarely needs it: the rollback journal restores
// the ledger file itself to its pre-run bytes (see RestoreRollbackReport),
// and reconcileScaffoldBirths calls it only when that restore failed.
func ForgetScaffold(root, relPath string) {
	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	rel := filepath.ToSlash(relPath)
	led := loadScaffoldLedgerLocked(root)
	if _, ok := led[rel]; !ok {
		return
	}
	delete(led, rel)
	saveScaffoldLedgerLocked(root)
}

// ScaffoldLedgerPaths returns root's recorded paths, sorted. Exposed for
// tests and for `forge project audit`-style reporting; callers must not
// mutate the ledger through it.
func ScaffoldLedgerPaths(root string) []string {
	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	led := loadScaffoldLedgerLocked(root)
	out := make([]string, 0, len(led))
	for p := range led {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// AbsentScaffolds returns the recorded paths that are NOT on disk, sorted.
//
// That set is the ledger's third state made VISIBLE. The ledger exists so a
// deleted scaffold stays deleted, and the suppression it implements is
// correct — but it is also completely silent, and silence is how the
// deletion turns into a defect. A measured run deleted a scaffolded CRUD
// lifecycle test expecting `forge generate` to re-derive it against the
// migrations it had just corrected; generate reported success, said
// nothing, and the file was simply gone. The author spent an hour trying to
// reproduce the birth condition (copying the project aside, stripping RPCs
// from the proto, deleting db/migrations) because nothing in forge's output
// named the file or the one edit that brings it back.
//
// The remedy is not to weaken the guarantee — forge must still never
// resurrect a file the user removed — but to say what it is doing. This is
// the query the reporting layer needs, and it is deliberately GENERAL: it
// answers for every scaffold-once path the ledger holds, so a writer added
// later is covered without being taught about here.
//
// Unreadable (as opposed to absent) paths are not reported: a permission
// error is not evidence of a deletion, and a report that cannot tell the
// two apart is a report that cries wolf.
func AbsentScaffolds(root string) []string {
	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	var out []string
	for p, e := range loadScaffoldLedgerLocked(abs) {
		if e.Declined != "" {
			// Declared absent, not gone missing: nothing to report.
			continue
		}
		if _, serr := os.Stat(filepath.Join(abs, filepath.FromSlash(p))); os.IsNotExist(serr) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// AbsentScaffoldsIfChanged returns the absent set only when it DIFFERS from
// the set the last run reported, and records the new set as reported.
// Returns nil (and writes nothing) when the set is unchanged.
//
// AbsentScaffolds answers "which scaffolds are deleted?", which is a
// standing fact and therefore a standing banner: control-plane printed the
// same 38-path list, plus five lines of explanation, on EVERY run. The
// notice exists for the moment a scaffold GOES missing — the run that
// deleted one and got a silent success. That is an event, and an event
// reported unconditionally is indistinguishable from wallpaper by the third
// run.
//
// Both directions count as a change. A path appearing is the original case.
// A path DISAPPEARING from the set (the user rescaffolded it, or restored it
// from git) is also worth one line, because the previous notice is now
// stale, and because silently continuing to suppress would mean the next
// deletion of that same path never reports.
//
// The standing fact remains available on demand — `forge project scaffolded`
// lists it whenever the user asks, which is the right shape for a fact that
// does not change.
func AbsentScaffoldsIfChanged(root string) []string {
	absent := AbsentScaffolds(root)

	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	// Populate the memo (and scaffoldAbsentReported) from disk if this is
	// the first touch of this root in the process.
	loadScaffoldLedgerLocked(abs)

	if sameStringSet(scaffoldAbsentReported[abs], absent) {
		return nil
	}
	scaffoldAbsentReported[abs] = absent
	saveScaffoldLedgerLocked(abs)
	return absent
}

// sameStringSet compares two SORTED string slices for equality. Both
// callers produce sorted input (AbsentScaffolds sorts; the persisted list
// was sorted when it was written), so this is a cheap element-wise compare
// rather than a set build.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// SplitScaffoldPath resolves an absolute destination path into the
// (projectRoot, relPath) pair the ledger is keyed on, by walking up to the
// nearest directory holding a forge.yaml.
//
// It exists because several scaffold-once writers — the raw ones in
// internal/codegen — were built to take a single joined path and never
// thread the pair through (the same shape that forced RecordPreWriteAbs to
// exist for the rollback journal). Rather than re-plumb every call site,
// the root is recovered here.
//
// When journaling is armed its root is authoritative: the pipeline knows
// which project it is generating, and a nested module inside the project
// (a frontend workspace, an examples/ tree) must not be mistaken for a
// separate project with its own ledger.
//
// Returns ok=false when no project root can be established — a bare temp
// directory in a unit test, for instance. Callers then fall back to the
// historical presence check rather than refusing to write.
func SplitScaffoldPath(path string) (root, relPath string, ok bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", false
	}
	if rollbackRoot != "" {
		if rel, rerr := filepath.Rel(rollbackRoot, abs); rerr == nil &&
			rel != "." && !filepath.IsAbs(rel) && !hasDotDotPrefix(rel) {
			return rollbackRoot, filepath.ToSlash(rel), true
		}
	}
	dir := filepath.Dir(abs)
	for {
		if _, serr := os.Stat(filepath.Join(dir, "forge.yaml")); serr == nil {
			rel, rerr := filepath.Rel(dir, abs)
			if rerr != nil {
				return "", "", false
			}
			return dir, filepath.ToSlash(rel), true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		dir = parent
	}
}

// ScaffoldOnceDecision is the single answer every scaffold-once writer in
// forge asks for, so that the three-state rule is stated ONCE instead of
// being re-derived (and mis-derived as a bare os.Stat) at each site.
//
// Returns write=true only for a genuine birth. When it returns false the
// caller must not write; when it returns true the caller writes and then
// calls RecordScaffold.
//
// The `present` arm ADOPTS: a file that exists but has no birth record is
// a project scaffolded by an older forge, so the record is backfilled here.
// Without that backfill, every project created before this ledger existed
// would keep the old behavior forever — the user's first deletion would
// look like "never scaffolded" and be undone exactly once more.
func ScaffoldOnceDecision(root, relPath string) (write bool) {
	full := filepath.Join(root, relPath)
	if _, err := os.Stat(full); err == nil {
		// Present. The user owns the bytes either way; backfill the birth
		// record so a LATER deletion is recognizable as a deletion.
		RecordScaffold(root, relPath)
		return false
	} else if !os.IsNotExist(err) {
		// Unreadable (permissions, a directory in the way). Not a birth we
		// can vouch for — writing over it is the destructive guess.
		return false
	}
	// Absent: birth iff forge has never scaffolded it here.
	return ScaffoldMayBeBorn(root, relPath)
}

// ScaffoldMayBeBorn is the one answer to "may forge create this scaffold-once
// file, which is not on disk?", shared by every writer that can birth one:
// `forge generate`'s emitters (through ScaffoldOnceDecision), `forge project
// upgrade`'s templates phase, and the scaffold commands.
//
// It is false when the ledger records the path (the user deleted it, or
// declined it with `forge project disown --absent`) and when the path was
// disowned, because either way the project has said the file is not forge's
// to write. Only `forge project rescaffold`, which drops the record first, or
// naming the path under `forge project upgrade --force`, brings it back.
//
// The caller has already established the file is absent; this does not stat.
func ScaffoldMayBeBorn(root, relPath string) bool {
	if ScaffoldRecorded(root, relPath) {
		return false
	}
	return !scaffoldDisowned(root, relPath)
}

// scaffoldDisowned reports whether relPath has a .forge/disowned.json entry.
// An unreadable or absent file reads as "not disowned": like the ledger, the
// disowned set only ever suppresses a write, so a failure to read it must
// leave forge's historical behavior in place.
func scaffoldDisowned(root, relPath string) bool {
	data, err := os.ReadFile(filepath.Join(root, DisownedFile))
	if err != nil {
		return false
	}
	var d disownedJSON
	if json.Unmarshal(data, &d) != nil {
		return false
	}
	_, ok := d.Files[slashKey(relPath)]
	return ok
}

// RecordScaffoldWithHash records a birth AND the content hash forge wrote,
// for the scaffolds that keep refreshing until the user touches them.
//
// Unlike RecordScaffold it overwrites an existing entry's hash: each refresh
// is a new birth for this purpose, and the recorded hash has to track the
// bytes currently on disk or the very next run would read the file as edited.
func RecordScaffoldWithHash(root, relPath string, content []byte) {
	if IsLocalOnlyScaffold(relPath) {
		return
	}
	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	rel := filepath.ToSlash(relPath)
	led := loadScaffoldLedgerLocked(root)
	e, known := led[rel]
	if !known {
		noteBirthLocked(rel)
	}
	e.BirthHash = Hash(content)
	led[rel] = e
	saveScaffoldLedgerLocked(root)
}

// ScaffoldIsPristine reports whether relPath is still byte-for-byte what
// forge last wrote there.
//
// False when the file is absent, unreadable, was never recorded with a hash,
// or differs by a single byte — every one of which means "do not touch it".
// A missing hash reads as edited on purpose: a scaffold born before this
// bookkeeping existed is one forge has no evidence about, and silently
// rewriting a file the user may have spent an afternoon on is the failure
// worth avoiding.
func ScaffoldIsPristine(root, relPath string) bool {
	scaffoldMu.Lock()
	defer scaffoldMu.Unlock()
	rel := filepath.ToSlash(relPath)
	led := loadScaffoldLedgerLocked(root)
	e, ok := led[rel]
	if !ok || e.BirthHash == "" {
		return false
	}
	content, err := os.ReadFile(filepath.Join(root, relPath))
	if err != nil {
		return false
	}
	return Hash(content) == e.BirthHash
}
