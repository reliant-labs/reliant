package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/reliant-labs/forge/internal/openfiles"
)

// Temp sweep: reclaim abandoned scratch directories in the system temp dir.
//
// These are not forge's own artifacts in the sense the rest of this package
// means it — nobody declared them and no project owns them — but they are the
// single largest unreclaimed consumer on a developer machine that builds Go
// all day. Measured on one host: 128 `go-link-*` dirs holding 15 GB (linker
// scratch orphaned when a link is SIGKILLed, which `cmd/link` would otherwise
// have removed on exit), plus several hundred leaked test fixtures.
//
// The whole design is about never deleting something that is still wanted,
// because $TMPDIR is shared with every other tool on the machine:
//
//   - A FIXED ALLOWLIST of name prefixes, never a pattern or a blanket sweep.
//     An entry forge does not recognize is left alone forever, even if it is
//     ancient and enormous.
//   - One FIXED SHAPE besides the allowlist: an orphaned Go `t.TempDir()`
//     root — <TestName><random digits>/ whose children are all `001`, `002`…
//     directories (orphanedGoTestTempDir). A test binary killed before its
//     Cleanup runs leaves one behind forever; measured on one Mac, five such
//     roots from a single test held 22 GB. The match is on the full shape Go
//     produces, children included, never on a name alone.
//   - TOP LEVEL ONLY, so a prefix match can never be inherited from a parent.
//   - 24h of total quiescence, measured as the NEWEST mtime anywhere inside
//     the entry — not the root's own mtime, which a directory keeps from its
//     creation while files churn underneath it.
//   - Not open by any process. One lsof snapshot covers every candidate, and
//     a missing, failing, killed or timed-out lsof skips the whole layer:
//     without that evidence the age test alone cannot distinguish abandoned
//     from merely idle. The root is resolved before anything is compared,
//     because lsof reports resolved paths and os.TempDir() on macOS is not
//     one (/var/folders/… is /private/var/folders/…).
//   - Never a git worktree. A `.git` FILE is the gitdir pointer a real
//     worktree has, so it means stop. A `.git` DIR is allowed only under
//     `tierguard-`, whose fixtures `git init` a scaffolded project on purpose.
type tempSweep struct {
	ctx    context.Context
	root   string
	now    time.Time
	maxAge time.Duration
	// openPaths snapshots every path a live process holds. Injectable so
	// tests need neither a real lsof nor a real open file.
	openPaths func() (openfiles.Snapshot, error)
	print     func(string, ...any)
	// del paces, caps and backs off the removals (deletes.go). The zero
	// value is filled from the default policy.
	del deleter
}

// tempSweepPrefixes is the allowlist. Each entry is scratch that its producer
// is KNOWN to abandon — Go toolchain scratch orphaned by a killed process, the
// upstream embedded-postgres log file (created per start, never removed), and
// forge's own sync.Once test fixtures, which are now cleaned up at the source
// but leave historical copies on disk.
var tempSweepPrefixes = []string{
	"go-link-",
	"go-build",
	"embedded_postgres_log",
	"forge-e2e-bin-",
	"forge-bin-",
	"forge-skill-validate-",
	"forge-kcl-module-test-",
	"tierguard-",
	"forge-k3d-",
}

// tempSweepGitDirPrefixes names the prefixes whose fixtures legitimately
// contain a `.git` DIRECTORY. Everything else with a `.git` of any kind is
// treated as someone's checkout and skipped.
var tempSweepGitDirPrefixes = []string{"tierguard-"}

const tempSweepAge = 24 * time.Hour

// tempSweepWalkLimit bounds the per-entry walk. A scaffolded fixture tree is a
// few thousand files; anything larger is not a shape we recognize, and the
// sweep declines rather than spending unbounded time measuring it.
const tempSweepWalkLimit = 200_000

// tempRoot resolves the directory this sweep reclaims from.
//
// Under `go test` an unset root is REFUSED rather than defaulted to
// os.TempDir(). This layer is one of only two in GC whose blast radius is a
// fixed ABSOLUTE path outside the project — Logs walks Policy.Projects (empty
// by default, so inert) and the registry layers go through an injected
// Command, but an unset TempRoot would apply-sweep the developer's REAL
// $TMPDIR. Four pre-existing tests in this package call GC(ctx, true) to
// assert things about Docker endpoints and builder pruning; none of them has
// any reason to think about a temp sweep, and the identical hole in the
// Sources layer deleted five real source clones before it was closed.
//
// So the safeguard cannot be "remember to set a field". Making the default
// unreachable from a test turns a silent deletion into a message that names
// what to set.
func (r Runner) tempRoot() (string, error) {
	if r.TempRoot != "" {
		return r.TempRoot, nil
	}
	if testing.Testing() {
		return "", fmt.Errorf("storage.Runner.TempRoot is unset under test: " +
			"set it to a t.TempDir() (an unset root would apply-sweep the developer's real $TMPDIR)")
	}
	return os.TempDir(), nil
}

// TempSweep reclaims abandoned scratch entries in the system temp directory.
// Dry-run by default: it prints each candidate and its size, and removes
// nothing unless apply is set.
func (r Runner) TempSweep(apply bool) error {
	root, err := r.tempRoot()
	if err != nil {
		r.print("skip temp sweep (%v)\n", err)
		return nil
	}
	// Candidates are named from the RESOLVED root so they are spelled the
	// way lsof spells what it reports. A root that cannot be resolved is
	// skipped, never swept under its unresolved name.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		if !os.IsNotExist(err) {
			r.print("skip temp sweep (cannot resolve %s: %v)\n", root, err)
		}
		return nil
	}
	ctx := r.hostCtx()
	s := tempSweep{
		ctx:       ctx,
		root:      resolved,
		now:       time.Now(),
		maxAge:    tempSweepAge,
		openPaths: func() (openfiles.Snapshot, error) { return r.openSnapshot(ctx) },
		print:     r.print,
		del:       r.deletes(),
	}
	return s.run(apply)
}

// done reports the sweep's deadline or cancellation, if it has passed. A
// sweep cut off by it stops where it is: everything not yet removed is
// retained, and the caller learns the pass did not finish.
func (s tempSweep) done() error {
	if s.ctx == nil {
		return nil
	}
	return s.ctx.Err()
}

func (s tempSweep) run(apply bool) error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		// No temp dir to sweep is not a maintenance failure.
		return nil
	}

	// Collect the name-matched set first, so lsof is only paid for when there
	// is something it could protect.
	type namedEntry struct {
		name  string
		mtime time.Time
	}
	var named []namedEntry
	for _, entry := range entries {
		// A symlink at the top level is never followed and never removed:
		// its target is outside the root this sweep is scoped to.
		if entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		if !tempSweepAllowed(entry.Name()) && !orphanedGoTestTempDir(s.root, entry) {
			continue
		}
		var mtime time.Time
		if info, err := entry.Info(); err == nil {
			mtime = info.ModTime()
		}
		named = append(named, namedEntry{entry.Name(), mtime})
	}
	if len(named) == 0 {
		return nil
	}

	open, err := s.openPaths()
	if ctxErr := s.done(); ctxErr != nil {
		// The deadline hit while waiting for the snapshot: nothing could be
		// shown safe to remove yet. That is a cut-off, not a failure; the
		// next pass resumes, and the snapshot is started with the pass so it
		// is usually ready by now.
		return &CutOffError{Err: fmt.Errorf("temp sweep: no open-files snapshot before the deadline, nothing removed: %w", ctxErr)}
	}
	if err != nil {
		// Fail closed. Age alone cannot tell an abandoned entry from one a
		// live process is still writing to, and deleting the latter breaks a
		// running build for bytes that would have been reclaimed next pass.
		s.print("temp sweep skipped: cannot determine which temp entries are in use (%v)\n", err)
		return nil
	}

	// Oldest first, each removed as soon as it qualifies: a cut-off keeps what
	// it already reclaimed. Entry mtime is the ordering hint only; whether an
	// entry is idle is decided by the newest mtime INSIDE it, measured below.
	sort.SliceStable(named, func(i, j int) bool { return named[i].mtime.Before(named[j].mtime) })
	var total int64
	var removed, reached, qualified int
	var skippedOpen, skippedYoung, skippedGit int
	var cut, stop error
	for _, n := range named {
		if err := s.done(); err != nil {
			cut = err
			break
		}
		reached++
		path := filepath.Join(s.root, n.name)
		if open.Holds(path) {
			skippedOpen++
			continue
		}
		size, newest, hasGit, walkErr := s.measure(path, n.name)
		if walkErr != nil {
			if s.done() != nil {
				cut = s.done()
				reached--
				break
			}
			if fsDistress(walkErr) && s.del.pass != nil {
				stop = s.del.observe(path, walkErr)
				s.print("temp sweep: stopped at %s (%v)\n", path, stop)
				break
			}
			continue
		}
		if hasGit {
			skippedGit++
			continue
		}
		if s.now.Sub(newest) < s.maxAge {
			skippedYoung++
			continue
		}
		qualified++
		s.print("temp sweep: %s (%d bytes, idle %s)\n", path, size, s.now.Sub(newest).Round(time.Hour))
		if !apply {
			total += size
			continue
		}
		ok, err := s.remove(path)
		if err != nil {
			stop = err
			break
		}
		if ok {
			total += size
			removed++
		}
	}
	if qualified > 0 || skippedOpen > 0 || skippedYoung > 0 || skippedGit > 0 {
		verb := "reclaimable"
		if apply {
			verb = "reclaimed"
		}
		s.print("temp sweep: %d entries %s (%.2f GiB); retained %d in use, %d recent, %d with git metadata\n",
			map[bool]int{true: removed, false: qualified}[apply], verb,
			float64(total)/float64(GiB), skippedOpen, skippedYoung, skippedGit)
	}
	if stop != nil {
		return stop
	}
	if cut != nil {
		s.print("temp sweep: cut off after %d/%d candidates\n", reached, len(named))
		return &CutOffError{Err: fmt.Errorf("temp sweep cut off after %d/%d candidates: %w", reached, len(named), cut)}
	}
	return nil
}

// remove deletes one qualified entry through the governor. A non-nil error is
// the delete cap or a back-off, which stops the sweep: this entry may be
// partly removed and the rest are untouched, all retained for the next pass.
// Any other failure costs only this entry (ok false).
func (s tempSweep) remove(path string) (ok bool, stop error) {
	del := s.del
	if del.pass == nil {
		del = newDeleter(DefaultPolicy(), nil, nil)
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	err := del.removeTree(ctx, path)
	if err == nil {
		return true, nil
	}
	if stopsPass(err) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		s.print("temp sweep: stopped at %s (%v)\n", path, err)
		return false, err
	}
	s.print("temp sweep: could not remove %s: %v\n", path, err)
	return false, nil
}

// measure walks one candidate, returning its total size, the newest mtime
// anywhere inside it, and whether it carries git metadata that disqualifies
// it. WalkDir does not follow symlinks, which is the behaviour this needs:
// a link inside the entry must not let the walk escape the entry.
func (s tempSweep) measure(path, name string) (size int64, newest time.Time, hasGit bool, err error) {
	gitDirOK := false
	for _, prefix := range tempSweepGitDirPrefixes {
		if strings.HasPrefix(name, prefix) {
			gitDirOK = true
			break
		}
	}
	visited := 0
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// An unreadable child makes the measurement incomplete, which
			// means the entry cannot be shown to be idle. Abort it.
			return walkErr
		}
		if visited++; visited > tempSweepWalkLimit {
			return fmt.Errorf("temp entry %s exceeds the %d-file walk limit", path, tempSweepWalkLimit)
		}
		if d.Name() == ".git" {
			// A `.git` FILE is a worktree's gitdir pointer: never ours.
			// A `.git` DIR is only ours under an allowlisted fixture prefix.
			if !d.IsDir() || !gitDirOK {
				hasGit = true
				return filepath.SkipAll
			}
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if d.Type().IsRegular() {
			size += info.Size()
		}
		return nil
	})
	return size, newest, hasGit, err
}

// tempSweepAllowed reports whether name is on the fixed prefix allowlist.
func tempSweepAllowed(name string) bool {
	for _, prefix := range tempSweepPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// goTestTempSymbols are the only non-alphanumeric characters testing.T.TempDir
// keeps when it turns a test name into a directory name (removeSymbolsExcept in
// the testing package); every other symbol, `/` included, is dropped.
const goTestTempSymbols = "!#$%&()+,-.=@^_{}~ "

// goTestTempRootMaxLen is the longest root name TempDir can produce: the test
// name truncated to 64 bytes, then os.MkdirTemp's random suffix, a uint32 in
// decimal (at most 10 digits).
const goTestTempRootMaxLen = 64 + 10

// goTestTempRootName reports whether name has the exact shape
// testing.T.TempDir gives its root directory: os.MkdirTemp(<name>) where <name>
// is a Go test, benchmark or fuzz function name with symbols stripped, so the
// root is that name followed by MkdirTemp's random decimal suffix.
func goTestTempRootName(name string) bool {
	if len(name) > goTestTempRootMaxLen {
		return false
	}
	var rest string
	matched := false
	for _, prefix := range []string{"Test", "Benchmark", "Fuzz"} {
		if after, ok := strings.CutPrefix(name, prefix); ok {
			rest, matched = after, true
			break
		}
	}
	if !matched {
		return false
	}
	// `go test` only runs Test<X> where X is not a lower-case letter, so
	// "Testdata123" or "Testing42" is never a test's TempDir.
	if first, _ := utf8.DecodeRuneInString(rest); unicode.IsLower(first) {
		return false
	}
	// MkdirTemp's random suffix: the name must END in at least one digit.
	if strings.TrimRight(rest, "0123456789") == rest {
		return false
	}
	for _, r := range rest {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) && !strings.ContainsRune(goTestTempSymbols, r) {
			return false
		}
	}
	return true
}

// goTestTempChildName reports whether name is one of TempDir's per-call
// subdirectories, which it names fmt.Sprintf("%03d", seq): three or more
// decimal digits.
func goTestTempChildName(name string) bool {
	if len(name) < 3 {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// orphanedGoTestTempDir reports whether entry is a whole testing.T.TempDir
// root: a Go-test-shaped name whose children are ALL numbered directories, and
// at least one of them. The children are what make this a shape rather than a
// name match — a directory that merely starts with "Test" and ends in digits,
// but holds anything Go would not have put there (a stray file, a `results/`
// dir, nothing at all), is somebody else's and is left alone.
func orphanedGoTestTempDir(root string, entry fs.DirEntry) bool {
	if !entry.IsDir() || !goTestTempRootName(entry.Name()) {
		return false
	}
	children, err := os.ReadDir(filepath.Join(root, entry.Name()))
	if err != nil || len(children) == 0 {
		return false
	}
	for _, child := range children {
		// IsDir is false for a symlink (ReadDir reports Lstat types), so a
		// numbered link to somewhere else disqualifies the root too.
		if !child.IsDir() || !goTestTempChildName(child.Name()) {
			return false
		}
	}
	return true
}
