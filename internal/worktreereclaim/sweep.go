// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The machine sweep reclaims every linked git worktree on this machine that
// nobody is using, whoever made it (design: worktree-reclaim-all; owner
// decisions 2026-11-08, question bccdb4da):
//
//   - Discovery is from git, not the database (sweep_discover.go): Reliant
//     workspaces whose chats are still open and checkouts agents made by hand
//     alike. Main checkouts are never candidates; full clones are reported,
//     never touched.
//   - Every deletion is non-destructive: the checkout is captured into a local
//     ref of its own repository first (capture.go), and the capture is
//     verified against the files immediately before they are deleted.
//   - Two classes. A checkout that is clean, has its HEAD on a remote and has
//     been idle CleanIdle (24h) is AUTO-SAFE and reclaimed without asking. One
//     with uncommitted, unpushed or reflog-only work that has been idle
//     ApprovalIdle (about 5 days: no commit, no file change) NEEDS APPROVAL: it
//     is only ever proposed, and reclaimed only after a person approves that
//     exact state (Approvals). Everything else is held, with a reason.
//   - A Reliant workspace's checkout is restored automatically, capture
//     included, the next time its chat runs a tool (worktree.ensure).
//
// Never touched: main checkouts, bare repositories, a worktree locked by
// anything but Reliant, one Reliant's archive protocol owns, one with a
// nested repository, one holding ignored files that are not rebuildable
// output, one a process or daemon work is using, and the daemon's own.
//
// Mode off does nothing; dry-run reports what it would reclaim, and the
// bytes, and captures and deletes nothing; on reclaims.

// SweepMode is worktree_reclaim.mode (RELIANT_WORKTREE_RECLAIM).
type SweepMode string

const (
	SweepOff    SweepMode = "off"
	SweepDryRun SweepMode = "dry-run"
	SweepOn     SweepMode = "on"
)

// ParseSweepMode reads a mode; ok is false for anything but off, dry-run or
// on (case and surrounding space ignored; "dryrun" and "dry_run" accepted).
func ParseSweepMode(s string) (SweepMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return SweepOff, true
	case "dry-run", "dryrun", "dry_run":
		return SweepDryRun, true
	case "on":
		return SweepOn, true
	}
	return "", false
}

const (
	// DefaultCleanIdle is how long an auto-safe checkout must have been
	// untouched (no commit, no file change, build output included).
	DefaultCleanIdle = 24 * time.Hour
	// DefaultApprovalIdle is how long a checkout with work only it holds must
	// have been untouched before it is proposed for approval.
	DefaultApprovalIdle = 5 * 24 * time.Hour
	// DefaultSweepBudget bounds one pass; what it does not reach is deferred
	// to the next.
	DefaultSweepBudget = 30 * time.Minute
)

// SweepOptions configures one pass.
type SweepOptions struct {
	Mode SweepMode
	// Roots are walked for repositories; nil means DefaultSweepRoots.
	Roots []SweepRoot
	// Anchors are repositories (main checkouts or git dirs) known otherwise.
	Anchors      []string
	CleanIdle    time.Duration
	ApprovalIdle time.Duration
	Capture      CaptureLimits
	Budget       time.Duration
	// StateDir holds the report, journal and approvals; "" means
	// DefaultStateDir.
	StateDir string
	Trigger  string
	// Pressure orders candidates largest first instead of oldest first.
	Pressure bool
	// ApprovedOnly evaluates only the worktrees with a pending approval:
	// the daemon's prompt response to a decision, between scheduled passes.
	ApprovedOnly bool
	// Version is the daemon's, recorded in captures and the journal.
	Version string
}

func (o SweepOptions) withDefaults() SweepOptions {
	if o.Roots == nil {
		o.Roots = DefaultSweepRoots()
	}
	if o.CleanIdle <= 0 {
		o.CleanIdle = DefaultCleanIdle
	}
	if o.ApprovalIdle <= 0 {
		o.ApprovalIdle = DefaultApprovalIdle
	}
	if o.ApprovalIdle < o.CleanIdle {
		o.ApprovalIdle = o.CleanIdle
	}
	if o.Budget <= 0 {
		o.Budget = DefaultSweepBudget
	}
	o.Capture = o.Capture.withDefaults()
	return o
}

// SweepClass is what a worktree is, for the sweep.
type SweepClass string

const (
	// ClassAutoSafe: clean, HEAD on a remote, idle CleanIdle. Reclaimed in
	// mode on without asking, after capture.
	ClassAutoSafe SweepClass = "auto_safe"
	// ClassNeedsApproval: holds work only it has, idle ApprovalIdle. Never
	// reclaimed without a person's approval of this exact state.
	ClassNeedsApproval SweepClass = "needs_approval"
	// ClassHeld: not a candidate; Reason says why.
	ClassHeld SweepClass = "held"
)

// SweepOutcome is what a pass did with one worktree.
type SweepOutcome string

const (
	OutcomeWouldReclaim  SweepOutcome = "would_reclaim"
	OutcomeReclaimed     SweepOutcome = "reclaimed"
	OutcomeAwaiting      SweepOutcome = "awaiting_approval"
	OutcomeDeclined      SweepOutcome = "declined"
	OutcomeSweepHeld     SweepOutcome = "held"
	OutcomeSweepDeferred SweepOutcome = "deferred"
	// OutcomeMovedBack: an interrupted reclaim's checkout was put back at its
	// path.
	OutcomeMovedBack  SweepOutcome = "moved_back"
	OutcomeSweepError SweepOutcome = "error"
)

// Held reasons the machine sweep adds.
const (
	// ReasonLocked: `git worktree lock`ed by something other than Reliant.
	// Git's explicit keep contract; never reclaimed.
	ReasonLocked HeldReason = "locked"
	// ReasonArchiveProtocol: an archived Reliant workspace, which the
	// server's archive protocol removes; the sweep leaves it to that.
	ReasonArchiveProtocol HeldReason = "archive_protocol"
	// ReasonGitOperation: a merge, rebase, cherry-pick, revert or bisect is
	// in progress; that state is not something a capture records.
	ReasonGitOperation HeldReason = "git_operation_in_progress"
	// ReasonSpecialFile: a socket, FIFO or device in the checkout.
	ReasonSpecialFile HeldReason = "special_file"
)

// SweepEntry is one worktree in a report.
type SweepEntry struct {
	Key       string `json:"key"`
	Path      string `json:"path"`
	CommonDir string `json:"common_dir"`
	// Repo is the repository's main checkout ("" for a bare repository).
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head,omitempty"`
	// Base is the branch its work counts as landed in: the remote's default
	// branch (origin/HEAD), "" when the repository names none.
	Base string `json:"base,omitempty"`
	// Owner is "reliant" for a Reliant workspace's checkout, "unregistered"
	// for any other.
	Owner        string       `json:"owner"`
	RowID        string       `json:"row_id,omitempty"`
	Lock         string       `json:"lock,omitempty"`
	Class        SweepClass   `json:"class"`
	Outcome      SweepOutcome `json:"outcome"`
	Reason       HeldReason   `json:"reason,omitempty"`
	Detail       string       `json:"detail,omitempty"`
	LastActivity time.Time    `json:"last_activity,omitempty"`
	IdleSeconds  int64        `json:"idle_seconds,omitempty"`
	Bytes        int64        `json:"bytes,omitempty"`
	// Fingerprint binds an approval to this exact state (needs approval).
	Fingerprint string `json:"fingerprint,omitempty"`
	// Work summarizes what only this worktree holds (needs approval): the
	// changed files and the commits on no remote.
	Work          string `json:"work,omitempty"`
	UnpushedCount int    `json:"unpushed_commits,omitempty"`
	CaptureRef    string `json:"capture_ref,omitempty"`
	FreedBytes    int64  `json:"freed_bytes,omitempty"`
}

// SweepTotals sums a report.
type SweepTotals struct {
	Seen               int   `json:"seen"`
	AutoSafe           int   `json:"auto_safe"`
	AutoSafeBytes      int64 `json:"auto_safe_bytes"`
	NeedsApproval      int   `json:"needs_approval"`
	NeedsApprovalBytes int64 `json:"needs_approval_bytes"`
	Held               int   `json:"held"`
	Deferred           int   `json:"deferred"`
	Reclaimed          int   `json:"reclaimed"`
	ReclaimedBytes     int64 `json:"reclaimed_bytes"`
	Errors             int   `json:"errors"`
	// HeldByReason counts the holds.
	HeldByReason map[HeldReason]int `json:"held_by_reason,omitempty"`
}

// SweepClone is a main checkout found under a root: reported, never managed.
type SweepClone struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes,omitempty"`
}

// SweepReport is one pass.
type SweepReport struct {
	Version       int          `json:"version"`
	Mode          SweepMode    `json:"mode"`
	Trigger       string       `json:"trigger,omitempty"`
	ApprovedOnly  bool         `json:"approved_only,omitempty"`
	StartedAt     time.Time    `json:"started_at"`
	FinishedAt    time.Time    `json:"finished_at"`
	DaemonVersion string       `json:"daemon_version,omitempty"`
	Anchors       []string     `json:"anchors"`
	Totals        SweepTotals  `json:"totals"`
	Entries       []SweepEntry `json:"entries"`
	Clones        []SweepClone `json:"clones,omitempty"`
}

// SweepMachine runs one pass over every linked worktree on the machine and
// returns its report, which it also writes to the state directory with one
// journal line per worktree. Mode off returns at once.
func (r *Reclaimer) SweepMachine(ctx context.Context, opts SweepOptions) SweepReport {
	opts = opts.withDefaults()
	rep := SweepReport{Version: 1, Mode: opts.Mode, Trigger: opts.Trigger, ApprovedOnly: opts.ApprovedOnly, StartedAt: r.now().UTC(), DaemonVersion: opts.Version}
	if opts.Mode != SweepDryRun && opts.Mode != SweepOn {
		return rep
	}
	dir, err := stateDirOr(opts.StateDir)
	if err != nil {
		return rep
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Budget)
	defer cancel()

	commons, mains := walkRoots(ctx, opts.Roots)
	commons = append(commons, DiscoverCommonDirs(r.Root)...)
	for _, a := range opts.Anchors {
		if c := repoCommonDir(ctx, a); c != "" {
			commons = append(commons, resolvePath(c))
		} else if fi, err := os.Stat(filepath.Join(a, "objects")); err == nil && fi.IsDir() {
			commons = append(commons, resolvePath(a))
		}
	}
	found, anchors := discoverWorktrees(ctx, dedupe(commons))
	rep.Anchors = anchors

	approvals := Approvals{}
	if a, err := readApprovals(dir); err == nil {
		approvals = a
	}
	approvedKeys := map[string]bool{}
	for _, d := range approvals.Approvals {
		if d.Decision == DecisionApprove {
			approvedKeys[d.Key] = true
		}
	}

	entries := make([]SweepEntry, 0, len(found))
	for _, d := range found {
		e := newEntry(d)
		if opts.ApprovedOnly && !approvedKeys[e.Key] {
			continue
		}
		entries = append(entries, e)
	}
	// Interrupted reclaims first: a checkout parked by a crash goes home
	// before anything else looks at its repository.
	for i := range entries {
		if needsRecovery(entries[i]) {
			r.recoverReclaim(ctx, &entries[i], dir)
		}
	}
	// Cheapest-to-decide first is not the order that matters: what matters
	// is reaching the worktrees worth reclaiming before the budget ends.
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	activity := make([]time.Time, len(entries))
	for i := range entries {
		activity[i] = quickActivity(entries[i].Path)
	}
	sort.SliceStable(order, func(a, b int) bool { return activity[order[a]].Before(activity[order[b]]) })

	// The cheap checks (lock, quarantine, a git operation, the reflog) for
	// every worktree first: most of a busy machine's worktrees are held by
	// one of them in milliseconds, and a budget spent on the expensive ones
	// must never leave those unreported.
	var deep []int
	for _, i := range order {
		if entries[i].Outcome == "" && !r.precheck(ctx, &entries[i], opts) {
			deep = append(deep, i)
		}
	}
	consumed := map[string]bool{}
	var consumedMu sync.Mutex
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range sweepWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				e := &entries[i]
				if ctx.Err() != nil {
					e.Class, e.Outcome, e.Detail = ClassHeld, OutcomeSweepDeferred, DetailNotReached
					continue
				}
				r.evaluateDeep(ctx, e, opts)
				if ctx.Err() != nil && e.Class == ClassHeld && e.Reason == ReasonUnverified {
					e.Outcome, e.Detail = OutcomeSweepDeferred, DetailTimedOut
					continue
				}
				done := map[string]bool{}
				r.decide(ctx, e, opts, approvals, dir, done)
				consumedMu.Lock()
				for k := range done {
					consumed[k] = true
				}
				consumedMu.Unlock()
			}
		}()
	}
	for _, i := range deep {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if opts.Pressure {
		// Largest first under pressure, for the report's reader too.
		sort.SliceStable(entries, func(a, b int) bool { return entries[a].Bytes > entries[b].Bytes })
	}

	if !opts.ApprovedOnly {
		known := map[string]bool{}
		for _, e := range entries {
			if e.Repo != "" {
				known[e.Repo] = true
			}
		}
		for _, m := range mains {
			clone := SweepClone{Path: m}
			if !known[m] && ctx.Err() == nil {
				clone.Bytes = r.cachedSize(ctx, m)
			}
			rep.Clones = append(rep.Clones, clone)
		}
	}
	rep.Entries = entries
	rep.Totals = totals(entries)
	rep.FinishedAt = r.now().UTC()

	if len(consumed) > 0 || !opts.ApprovedOnly {
		settleApprovals(context.WithoutCancel(ctx), dir, consumed, r.now())
	}
	if b, err := json.MarshalIndent(rep, "", "  "); err == nil && !(opts.ApprovedOnly && len(entries) == 0) {
		if opts.ApprovedOnly {
			// An approved-only pass amends the last full report rather than
			// replacing it: what it did not look at is still true.
			b = r.mergeIntoLastReport(dir, rep)
		}
		_ = writeFileAtomic(filepath.Join(dir, reportFile), b)
	}
	lines := make([]JournalLine, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, JournalLine{At: rep.FinishedAt, Mode: string(opts.Mode), Trigger: opts.Trigger, SweepEntry: e, DaemonVersion: opts.Version})
	}
	_ = appendJournal(dir, lines)
	return rep
}

// mergeIntoLastReport is the last full report with the entries of an
// approved-only pass replacing their earlier versions.
func (r *Reclaimer) mergeIntoLastReport(dir string, pass SweepReport) []byte {
	last, err := ReadSweepReport(dir)
	if err != nil {
		b, _ := json.MarshalIndent(pass, "", "  ")
		return b
	}
	byKey := map[string]SweepEntry{}
	for _, e := range pass.Entries {
		byKey[e.Key] = e
	}
	for i, e := range last.Entries {
		if n, ok := byKey[e.Key]; ok {
			last.Entries[i] = n
		}
	}
	last.Totals = totals(last.Entries)
	b, _ := json.MarshalIndent(last, "", "  ")
	return b
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// newEntry classifies a discovered worktree's owner from its lock alone.
func newEntry(d discovered) SweepEntry {
	e := SweepEntry{Path: d.Path, CommonDir: d.CommonDir, Repo: d.Main, Branch: strings.TrimPrefix(d.Branch, "refs/heads/"), Head: d.Head, Owner: "unregistered"}
	if d.Locked {
		e.Lock = d.Reason
		if e.Lock == "" {
			e.Lock = "(no reason given)"
		}
	}
	if p, ok := parseLock(d.Reason); ok && d.Locked {
		e.Owner, e.RowID, e.Key = "reliant", p.ID, p.ID
	} else if k, ok := parseReclaimingLock(d.Reason); ok && d.Locked {
		e.Key = k.Key
		if !strings.HasPrefix(k.Key, "p-") {
			e.Owner, e.RowID = "reliant", k.Key
		}
	} else {
		e.Key = CaptureKey(d.Path)
	}
	return e
}

// quickActivity is a cheap ordering key: the checkout's and its admin HEAD's
// mtimes.
func quickActivity(path string) time.Time {
	var t time.Time
	for _, p := range []string{path, filepath.Join(adminDir(path), "HEAD")} {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
	}
	return t
}

func (e *SweepEntry) hold(reason HeldReason, detail string) {
	e.Class, e.Outcome, e.Reason, e.Detail = ClassHeld, OutcomeSweepHeld, reason, detail
}

// gitOperationFiles name a git operation in progress in a worktree's admin
// directory.
var gitOperationFiles = []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "rebase-merge", "rebase-apply"}

// sweepWorkers bounds how many worktrees a pass inspects at once: each is a
// dozen git processes and a walk of its files, and a pass shares the machine
// with the agents working on it.
const sweepWorkers = 4

// evaluate decides a worktree's class, cheapest checks first; it never
// writes anything. Anything it cannot establish holds the worktree.
func (r *Reclaimer) evaluate(ctx context.Context, e *SweepEntry, opts SweepOptions) {
	if !r.precheck(ctx, e, opts) {
		r.evaluateDeep(ctx, e, opts)
	}
}

// precheck runs the checks that cost no walk and at most a reflog read. It
// reports whether they settled the entry (always as held).
func (r *Reclaimer) precheck(ctx context.Context, e *SweepEntry, opts SweepOptions) bool {
	r.precheckInto(ctx, e, opts)
	return e.Outcome != ""
}

func (r *Reclaimer) precheckInto(ctx context.Context, e *SweepEntry, opts SweepOptions) {
	now := r.now()
	switch {
	case isReclaimingLock(e.Lock):
		e.hold(ReasonQuarantined, "an interrupted reclaim holds it; see the journal")
		return
	case e.Lock != "" && e.RowID == "":
		e.hold(ReasonLocked, fmt.Sprintf("locked by something other than Reliant (%q)", e.Lock))
		return
	case e.Lock != "":
		if p, _ := parseLock(e.Lock); p.Archived {
			e.hold(ReasonArchiveProtocol, "an archived Reliant workspace; its own clean-up removes it")
			return
		}
	}
	if inQuarantine(r.Root, e.Path) {
		e.hold(ReasonQuarantined, "parked by a clean-up")
		return
	}
	if _, err := os.Lstat(e.Path); err != nil {
		e.hold(ReasonUnverified, "its directory cannot be read: "+err.Error())
		return
	}
	if !isLinkedWorktree(e.Path) {
		e.hold(ReasonUnmanaged, "not a linked worktree")
		return
	}
	if selfWithin(e.Path) {
		e.hold(ReasonInUse, "the daemon itself runs from here")
		return
	}
	admin := adminDir(e.Path)
	for _, f := range gitOperationFiles {
		if _, err := os.Lstat(filepath.Join(admin, f)); err == nil {
			e.hold(ReasonGitOperation, f+" is present")
			return
		}
	}
	// Commits and checkouts (the reflog), before any walk of the files.
	last, err := idleSince(ctx, e.Path, []string{e.Path})
	if err != nil {
		e.hold(ReasonUnverified, "could not read its reflog: "+err.Error())
		return
	}
	e.LastActivity = last
	if now.Sub(last) < opts.CleanIdle {
		e.hold(ReasonRecentlyActive, fmt.Sprintf("committed or checked out %s ago", now.Sub(last).Round(time.Minute)))
		return
	}
	if h := r.inUse(ctx, Worktree{ID: e.Key, Path: e.Path}, false); h != nil {
		e.hold(h.Reason, h.Detail)
	}
}

// evaluateDeep is evaluate after a precheck that settled nothing: the walk
// of the files, the ignored data, the capture envelope and the git verdict.
func (r *Reclaimer) evaluateDeep(ctx context.Context, e *SweepEntry, opts SweepOptions) {
	now := r.now()
	last := e.LastActivity
	sc := scanCheckout(ctx, e.Path)
	switch {
	case sc.Truncated:
		e.hold(ReasonUnverified, "the checkout is too large to inspect completely")
		return
	case len(sc.Nested) > 0:
		e.hold(ReasonNestedRepo, relDetail(e.Path, sc.Nested[0], "a git repository of its own lives here"))
		return
	case sc.Special != "":
		e.hold(ReasonSpecialFile, relDetail(e.Path, sc.Special, "not a file, directory or symlink"))
		return
	}
	if sc.Newest.After(last) {
		last = sc.Newest
	}
	e.LastActivity = last
	idle := now.Sub(last)
	e.IdleSeconds = int64(idle / time.Second)
	if idle < opts.CleanIdle {
		e.hold(ReasonRecentlyActive, fmt.Sprintf("a file changed %s ago", idle.Round(time.Minute)))
		return
	}
	if v := dataHold(ctx, e.Path, nil); !v.safe() {
		e.hold(v.Reason, v.Detail)
		return
	}
	if _, _, err := untrackedEnvelope(ctx, e.Path, opts.Capture); err != nil {
		if strings.Contains(err.Error(), errCaptureEnvelope.Error()) {
			e.hold(ReasonTooLarge, err.Error())
		} else {
			e.hold(ReasonUnverified, err.Error())
		}
		return
	}
	e.Base = defaultBase(ctx, e.Path)
	v := classifyCheckout(ctx, e.Path, e.Base, nil, e.RowID, r.sweepProbe(opts))
	switch v.Reason {
	case "":
		e.Class, e.Reason, e.Detail = ClassAutoSafe, "", "clean, HEAD on a remote, idle "+idle.Round(time.Hour).String()
	case ReasonDirty, ReasonUnpushed, ReasonUnreachable:
		if idle < opts.ApprovalIdle {
			e.hold(ReasonRecentlyActive, fmt.Sprintf("%s; proposed for approval once idle %s (idle %s)", v.Detail, opts.ApprovalIdle.Round(time.Hour), idle.Round(time.Hour)))
			return
		}
		e.Class, e.Reason, e.Detail = ClassNeedsApproval, v.Reason, v.Detail
		e.Work = workSummary(ctx, e.Path)
		e.UnpushedCount = unpushedCount(ctx, e.Path)
		fp, err := fingerprint(ctx, e.Path, e.Path, sc.Newest)
		if err != nil {
			e.hold(ReasonUnverified, err.Error())
			return
		}
		e.Fingerprint = fp
	default:
		e.hold(v.Reason, v.Detail)
		return
	}
	e.Bytes = r.cachedSize(ctx, e.Path)
}

// decide acts on an evaluated entry according to the mode and the
// approvals.
func (r *Reclaimer) decide(ctx context.Context, e *SweepEntry, opts SweepOptions, approvals Approvals, dir string, consumed map[string]bool) {
	switch e.Class {
	case ClassHeld:
		if e.Outcome == "" {
			e.Outcome = OutcomeSweepHeld
		}
		return
	case ClassAutoSafe:
		if opts.Mode != SweepOn {
			e.Outcome = OutcomeWouldReclaim
			return
		}
		r.reclaimEntry(ctx, e, opts, dir, "")
	case ClassNeedsApproval:
		d, ok := approvals.find(e.Key, e.Fingerprint)
		switch {
		case !ok:
			e.Outcome = OutcomeAwaiting
		case d.Decision == DecisionDecline:
			e.Outcome = OutcomeDeclined
		case opts.Mode != SweepOn:
			e.Outcome, e.Detail = OutcomeWouldReclaim, "approved; "+e.Detail
		default:
			r.reclaimEntry(ctx, e, opts, dir, e.Fingerprint)
		}
	}
	if e.Outcome == OutcomeReclaimed {
		consumed[e.Key] = true
	}
}

// sweepProbe is the merge probe for a pass. A dry-run never fetches: it is
// a report, and writes nothing to any repository.
func (r *Reclaimer) sweepProbe(opts SweepOptions) mergeProbe {
	p := r.probe()
	if opts.Mode != SweepOn {
		p.fetchBase = nil
	}
	return p
}

// defaultBase is the branch origin/HEAD names, without "origin/".
func defaultBase(ctx context.Context, checkout string) string {
	out, err := git(ctx, checkout, nil, "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(out, "origin/")
}

// workSummary names what only this checkout holds, for the person asked to
// approve it: `git status --short` (at most 20 lines) and the commits on no
// remote (at most 10).
func workSummary(ctx context.Context, checkout string) string {
	var b strings.Builder
	if out, err := git(ctx, checkout, nil, "status", "--short", "--untracked-files=all"); err == nil && out != "" {
		lines := strings.Split(out, "\n")
		if len(lines) > 20 {
			lines = append(lines[:20], fmt.Sprintf("… and %d more", len(lines)-20))
		}
		b.WriteString(strings.Join(lines, "\n"))
	}
	if out, err := git(ctx, checkout, nil, "log", "--oneline", "-n", "10", "HEAD", "--not", "--remotes"); err == nil && out != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("commits on no remote:\n" + out)
	}
	return b.String()
}

func unpushedCount(ctx context.Context, checkout string) int {
	out, err := git(ctx, checkout, nil, "rev-list", "--count", "HEAD", "--not", "--remotes")
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(out)
	return n
}

// fingerprint binds an approval to one state of a checkout: its HEAD and
// branch, every index entry (mode, blob and stage of each path: the staged
// content itself), every path `git status` reports (untracked included) with
// a hash of what is on disk at it, the commits only it reaches, and the
// newest change to any file. A commit, a staging, an edit (even one to a file
// already dirty, keeping its status and every mtime) or a new file changes
// it. It writes nothing to the repository. origPath is the checkout's path
// for the person (it is computed again on the parked copy, under another
// path).
func fingerprint(ctx context.Context, checkout, origPath string, newest time.Time) (string, error) {
	head, err := git(ctx, checkout, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	branch, _ := git(ctx, checkout, nil, "symbolic-ref", "-q", "HEAD")
	index, err := gitRaw(ctx, checkout, nil, "ls-files", "-s", "-z")
	if err != nil {
		return "", err
	}
	status, err := gitRaw(ctx, checkout, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return "", err
	}
	contents, err := statusContents(checkout, status)
	if err != nil {
		return "", err
	}
	tips, err := orphanTips(ctx, checkout, "")
	if err != nil {
		return "", err
	}
	sort.Strings(tips)
	h := sha256.New()
	for _, part := range []string{resolvePath(origPath), head, branch, string(index), string(status), contents, strings.Join(tips, ","), strconv.FormatInt(newest.UnixNano(), 10)} {
		fmt.Fprintf(h, "%d:%s\n", len(part), part)
	}
	return "fp2-" + hex.EncodeToString(h.Sum(nil))[:32], nil
}

// statusContents hashes what is on disk at every path a `git status
// --porcelain=v1 -z --no-renames` output names: a file's bytes, a symlink's
// target, or that it is absent or a directory. One line per path, in status
// order.
func statusContents(checkout string, status []byte) (string, error) {
	var b strings.Builder
	for _, rec := range strings.Split(string(status), "\x00") {
		if len(rec) <= 3 {
			continue
		}
		rel := rec[3:]
		p := filepath.Join(checkout, filepath.FromSlash(rel))
		var sum string
		fi, err := os.Lstat(p)
		switch {
		case errors.Is(err, os.ErrNotExist):
			sum = "absent"
		case err != nil:
			return "", err
		case fi.Mode()&os.ModeSymlink != 0:
			t, err := os.Readlink(p)
			if err != nil {
				return "", err
			}
			sum = "link:" + t
		case fi.IsDir():
			sum = "dir"
		case fi.Mode().IsRegular():
			f, err := os.Open(p)
			if err != nil {
				return "", err
			}
			fh := sha256.New()
			_, err = io.Copy(fh, f)
			f.Close()
			if err != nil {
				return "", err
			}
			sum = fmt.Sprintf("file:%o:%x", fi.Mode().Perm(), fh.Sum(nil))
		default:
			sum = "special:" + fi.Mode().Type().String()
		}
		fmt.Fprintf(&b, "%d:%s %s\n", len(rel), rel, sum)
	}
	return b.String(), nil
}

func (r *Reclaimer) cachedSize(ctx context.Context, path string) int64 {
	var res Result
	r.measure(ctx, &res, path)
	return res.SizeBytes
}

func totals(entries []SweepEntry) SweepTotals {
	t := SweepTotals{Seen: len(entries), HeldByReason: map[HeldReason]int{}}
	for _, e := range entries {
		switch {
		case e.Outcome == OutcomeReclaimed:
			t.Reclaimed++
			t.ReclaimedBytes += e.FreedBytes
		case e.Outcome == OutcomeSweepDeferred:
			t.Deferred++
		case e.Outcome == OutcomeSweepError:
			t.Errors++
		}
		switch e.Class {
		case ClassAutoSafe:
			t.AutoSafe++
			t.AutoSafeBytes += e.Bytes
		case ClassNeedsApproval:
			t.NeedsApproval++
			t.NeedsApprovalBytes += e.Bytes
		case ClassHeld:
			if e.Outcome != OutcomeSweepDeferred {
				t.Held++
				t.HeldByReason[e.Reason]++
			}
		}
	}
	return t
}
