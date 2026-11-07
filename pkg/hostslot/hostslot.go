// Package hostslot is a host-wide counting semaphore for heavy work: at most
// N heavy jobs — builds, test lanes, codegen, linters — run at once on one
// machine, however many agents, worktrees and terminals start them.
//
// Why it exists. On a shared machine the cost of a heavy job is set by how
// many others run beside it. Measured on a 16 vCPU / 32 GiB workspace VM:
// `go build ./...` took 36–50 s beside 0–3 other heavy jobs and 101–155 s
// beside 8 or more, and a local CI gate went from ~40 min to ~2.3 h. The loss
// was memory-cgroup reclaim, not CPU, so past a handful of concurrent heavy
// jobs EVERY job slows down: running them all at once finishes later than
// queueing them.
//
// Mechanism. A class ("heavy") has N slot files in one directory. Holding a
// slot is holding an exclusive OS file lock (flock(2); LockFileEx on Windows)
// on one of them, for as long as the holder runs. The OS drops the lock when
// the holder exits however it exits, so a crashed or killed holder never
// strands a slot: there is no staleness window, no pid probe and nothing for
// anyone to delete. After acquiring, a holder writes its pid and command into
// the slot file; that record exists only so a waiter can say whom it waits
// for. N comes from the machine (DefaultSlots) unless FORGE_<CLASS>_SLOTS
// overrides it (SlotsEnv).
//
// Waiting is FIFO-ish across processes. A waiter that finds the queue
// non-empty or every slot taken files a ticket — itself a locked file, so a
// waiter that dies leaves no live ticket — and only the N oldest live tickets
// compete for a slot that frees, so a newcomer never overtakes a queue. A
// waiter prints one line naming the holders (pid, command, age) and repeats it
// periodically, and gives up when its context is cancelled.
//
// Re-entrancy. A process that already runs inside a slot must not take
// another: a gate step holding a slot that runs `forge generate`, which wants
// a slot of its own, would otherwise deadlock as soon as every slot is held by
// parents waiting on children. Two signals mark a process as inside a slot,
// and either makes Acquire return at once without taking one:
//
//   - FORGE_SLOT_HELD in its environment (Slot.Env passes it to children),
//     naming a slot whose record still carries the same nonce and which is
//     still locked — a stale or copied token is ignored, not trusted;
//   - an ANCESTOR process holding a slot of the class with Options.Subtree
//     set (a wrapper like `forge slot run`, or a scheduler), read from the
//     kernel's parent links. This covers a child whose environment was
//     scrubbed between the holder and it.
package hostslot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/procguard"
)

// Heavy is the class for jobs that saturate several cores and gigabytes of
// memory: a whole-module build or test run, codegen, a full lint.
const Heavy = "heavy"

// EnvHeld carries the slots a process runs inside to its children, as
// comma-separated "<class>:<slot index>:<nonce>" entries. Set it by appending
// Slot.Env() to a child's environment; never by hand.
const EnvHeld = "FORGE_SLOT_HELD"

// EnvDir relocates the slot directory. The default is the user cache dir, so
// every process of one user on a host shares it. Point every user of a
// multi-user host at one directory (mode 1777) to share slots across users.
const EnvDir = "FORGE_SLOTS_DIR"

// Options configure Acquire and Status. The zero value is the production
// behavior.
type Options struct {
	// Dir is the slot directory; empty is $FORGE_SLOTS_DIR, else
	// <user cache dir>/forge/slots. Each class gets a subdirectory.
	Dir string
	// Slots overrides the capacity (a --slots flag). Zero resolves it with
	// CapacityFor: FORGE_<CLASS>_SLOTS, else the machine default.
	Slots int
	// Out receives the waiting lines. Nil waits silently.
	Out io.Writer
	// Label is the command recorded for this holder, shown to waiters. Empty
	// is this process's own command line.
	Label string
	// Subtree marks the holder as running the slot's work in child
	// processes, so every descendant counts as inside the slot even when its
	// environment lost FORGE_SLOT_HELD. Set it for wrappers and schedulers
	// that exec the work; leave it unset for a process that does the work
	// itself, so an unrelated child of a long-lived host is not exempted.
	Subtree bool
	// NoticeEvery is how often the waiting line repeats. Zero is 30s.
	NoticeEvery time.Duration
	// Poll is the mean interval between attempts while waiting, jittered
	// ±50%. Zero is 500ms.
	Poll time.Duration
	// Env is the environment read for FORGE_SLOT_HELD, FORGE_SLOTS_DIR and
	// FORGE_<CLASS>_SLOTS — normally os.Environ(). The package reads no
	// ambient variable itself, so a nil Env means none is set; in
	// particular, a caller that leaves it nil cannot see the slot its parent
	// passed it and will queue for one of its own.
	Env []string
}

// Slot is one acquisition. Release it when the work is done; a process that
// exits releases it regardless.
type Slot struct {
	class     string
	capacity  Capacity
	index     int
	file      *os.File
	nonce     string
	reentrant string // why Acquire took nothing, when it took nothing
	parent    string // the FORGE_SLOT_HELD this process inherited
	waited    time.Duration

	mu       sync.Mutex
	released bool
}

var classRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func validClass(class string) error {
	if !classRE.MatchString(class) {
		return fmt.Errorf("slot class %q: want lowercase letters, digits and dashes", class)
	}
	return nil
}

// Acquire takes a slot of class, waiting while every slot is held. It returns
// at once, taking nothing, when the limit is off or this process already runs
// inside a slot of the class (see the package doc). It returns ctx's error if
// ctx ends first.
func Acquire(ctx context.Context, class string, opt Options) (*Slot, error) {
	capy, err := opt.capacity(class)
	if err != nil {
		return nil, err
	}
	s := &Slot{class: class, capacity: capy, index: -1, parent: lookupEnv(opt.Env, EnvHeld)}
	if capy.Unlimited {
		return s, nil
	}
	dir, err := opt.classDir(class)
	if err != nil {
		return nil, err
	}
	if why, ok := reentrant(dir, class, s.parent); ok {
		s.reentrant = why
		return s, nil
	}

	start := time.Now()
	q := queue{dir: filepath.Join(dir, "queue")}
	var holders []Holder
	if q.ahead("") == 0 {
		ok, h, err := s.tryAny(dir, opt)
		if err != nil {
			return nil, err
		}
		if ok {
			return s, nil
		}
		holders = h
	}

	t, err := q.enqueue(opt.record(""))
	if err != nil {
		return nil, err
	}
	defer t.remove()

	every := opt.NoticeEvery
	if every <= 0 {
		every = 30 * time.Second
	}
	var lastNotice time.Time
	for {
		ahead := q.ahead(t.name)
		if ahead < capy.Slots {
			ok, h, err := s.tryAny(dir, opt)
			if err != nil {
				return nil, err
			}
			if ok {
				s.waited = time.Since(start)
				return s, nil
			}
			holders = h
		}
		if opt.Out != nil && time.Since(lastNotice) >= every {
			fmt.Fprintln(opt.Out, waitingLine(capy, holders, ahead, time.Since(start)))
			lastNotice = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(opt.jitteredPoll()):
		}
	}
}

// tryAny tries every slot once, from a random start so concurrent waiters do
// not all contend on slot 0. On failure it returns the holders it saw.
func (s *Slot) tryAny(dir string, opt Options) (bool, []Holder, error) {
	n := s.capacity.Slots
	off := mrand.IntN(n)
	var holders []Holder
	for k := 0; k < n; k++ {
		i := (off + k) % n
		path := slotPath(dir, i)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return false, nil, fmt.Errorf("open slot %s: %w", path, err)
		}
		ok, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return false, nil, fmt.Errorf("lock slot %s: %w", path, err)
		}
		if !ok {
			_ = f.Close()
			if rec, ok := readRecord(path); ok {
				holders = append(holders, rec.holder(i))
			}
			continue
		}
		nonce, err := newNonce()
		if err != nil {
			_ = unlock(f)
			_ = f.Close()
			return false, nil, err
		}
		rec := opt.record(nonce)
		if err := f.Truncate(0); err == nil {
			_, _ = f.WriteAt(rec.encode(), 0)
		}
		s.file, s.index, s.nonce = f, i, nonce
		return true, nil, nil
	}
	sort.Slice(holders, func(a, b int) bool { return holders[a].Slot < holders[b].Slot })
	return false, holders, nil
}

// Release gives the slot back. Safe to call more than once, and on a slot
// that took nothing.
func (s *Slot) Release() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released || s.file == nil {
		s.released = true
		return nil
	}
	s.released = true
	// Clear the record first: a waiter reading it after the unlock must not
	// name a holder that has gone.
	_ = s.file.Truncate(0)
	err := unlock(s.file)
	if cerr := s.file.Close(); err == nil {
		err = cerr
	}
	return err
}

// Env returns what to append to a child's environment so the child — and
// everything it runs — knows it is inside this slot and takes no other of
// the class. Nil when there is nothing to pass.
func (s *Slot) Env() []string {
	if s == nil || s.file == nil {
		return nil
	}
	own := fmt.Sprintf("%s:%d:%s", s.class, s.index, s.nonce)
	entries := []string{own}
	for _, e := range strings.Split(s.parent, ",") {
		e = strings.TrimSpace(e)
		if e == "" || strings.HasPrefix(e, s.class+":") {
			continue
		}
		entries = append(entries, e)
	}
	return []string{EnvHeld + "=" + strings.Join(entries, ",")}
}

// SetLabel replaces the command recorded for this holder, which waiters and
// `forge slot status` show. A scheduler that takes a slot and only then
// decides which job gets it names the job here.
func (s *Slot) SetLabel(label string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil || s.released {
		return
	}
	rec, ok := readRecord(s.file.Name())
	if !ok {
		return
	}
	rec.cmd = oneLine(label)
	if err := s.file.Truncate(0); err == nil {
		_, _ = s.file.WriteAt(rec.encode(), 0)
	}
}

// Inside reports whether this process already runs inside a slot of class
// (see the package doc), and why. Acquire would return at once, taking
// nothing; a scheduler that means to bound its own jobs by slots learns
// here that it cannot.
func Inside(class string, opt Options) (string, bool) {
	if err := validClass(class); err != nil {
		return "", false
	}
	dir := filepath.Join(opt.BaseDir(), class)
	if _, err := os.Stat(dir); err != nil {
		return "", false
	}
	return reentrant(dir, class, lookupEnv(opt.Env, EnvHeld))
}

// Class is the slot's class.
func (s *Slot) Class() string { return s.class }

// Index is the slot taken, or -1 when Acquire took nothing.
func (s *Slot) Index() int { return s.index }

// Capacity is the class's capacity as Acquire resolved it.
func (s *Slot) Capacity() Capacity { return s.capacity }

// Reentrant reports why Acquire took nothing because this process already
// runs inside a slot of the class ("" when it did take one, or the limit is
// off).
func (s *Slot) Reentrant() string { return s.reentrant }

// Unlimited reports that the class's limit is off, so nothing was taken.
func (s *Slot) Unlimited() bool { return s.capacity.Unlimited }

// Waited is how long Acquire waited for the slot.
func (s *Slot) Waited() time.Duration { return s.waited }

// String describes the acquisition in a few words, for a status line.
func (s *Slot) String() string {
	switch {
	case s.capacity.Unlimited:
		return s.class + " slots off (" + s.capacity.Source + ")"
	case s.reentrant != "":
		return s.class + " slot inherited (" + s.reentrant + ")"
	}
	return fmt.Sprintf("%s slot %d/%d", s.class, s.index+1, s.capacity.Slots)
}

// reentrant decides whether this process already runs inside a slot of
// class: a valid FORGE_SLOT_HELD token, or a Subtree holder among its
// ancestors.
func reentrant(dir, class, held string) (string, bool) {
	for _, e := range strings.Split(held, ",") {
		parts := strings.SplitN(strings.TrimSpace(e), ":", 3)
		if len(parts) != 3 || parts[0] != class {
			continue
		}
		i, err := strconv.Atoi(parts[1])
		if err != nil || i < 0 {
			continue
		}
		path := slotPath(dir, i)
		if rec, ok := readRecord(path); ok && rec.nonce == parts[2] && locked(path) {
			return fmt.Sprintf("%s from pid %d", EnvHeld, rec.pid), true
		}
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "slot-*.lock"))
	if len(paths) == 0 {
		return "", false
	}
	self := os.Getpid()
	var lineage *procguard.Lineage
	for _, path := range paths {
		rec, ok := readRecord(path)
		if !ok || !rec.subtree || rec.pid == self || rec.pid <= 0 {
			continue
		}
		if lineage == nil {
			l := procguard.Self()
			lineage = &l
		}
		if lineage.Contains(rec.pid) && locked(path) {
			return fmt.Sprintf("ancestor pid %d holds it", rec.pid), true
		}
	}
	return "", false
}

// locked reports whether another holder has path locked. The probe takes the
// lock for an instant when it is free; a concurrent acquirer that collides
// with it just tries the next slot.
func locked(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	ok, err := tryLock(f)
	if err != nil {
		return false
	}
	if ok {
		_ = unlock(f)
		return false
	}
	return true
}

func slotPath(dir string, i int) string {
	return filepath.Join(dir, fmt.Sprintf("slot-%d.lock", i))
}

func (o Options) capacity(class string) (Capacity, error) {
	c, err := CapacityFor(class, o.Env)
	if err != nil {
		return Capacity{}, err
	}
	if o.Slots > 0 {
		c.Slots, c.Unlimited, c.Source = o.Slots, false, fmt.Sprintf("requested %d", o.Slots)
	}
	return c, nil
}

// BaseDir is the directory holding every class's slots for these options.
func (o Options) BaseDir() string {
	if o.Dir != "" {
		return o.Dir
	}
	if d := lookupEnv(o.Env, EnvDir); d != "" {
		return d
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "forge", "slots")
	}
	return filepath.Join(os.TempDir(), "forge-slots")
}

func (o Options) classDir(class string) (string, error) {
	dir := filepath.Join(o.BaseDir(), class)
	if err := os.MkdirAll(filepath.Join(dir, "queue"), 0o755); err != nil {
		return "", fmt.Errorf("create slot directory: %w", err)
	}
	return dir, nil
}

func (o Options) jitteredPoll() time.Duration {
	p := o.Poll
	if p <= 0 {
		p = 500 * time.Millisecond
	}
	return time.Duration(float64(p) * (0.5 + mrand.Float64()))
}

func (o Options) record(nonce string) record {
	label := o.Label
	if label == "" {
		label = commandLine(os.Args)
	}
	return record{pid: os.Getpid(), cmd: oneLine(label), since: time.Now(), nonce: nonce, subtree: o.Subtree}
}

func newNonce() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("slot nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func lookupEnv(env []string, key string) string {
	val := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val = v // the last assignment wins, as it does for exec
		}
	}
	return val
}

// commandLine renders argv as one short line ("forge generate").
func commandLine(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	parts := append([]string{filepath.Base(argv[0])}, argv[1:]...)
	return strings.Join(parts, " ")
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const maxLen = 120
	if len(s) > maxLen {
		s = s[:maxLen] + "…"
	}
	return s
}

// Holder is one live slot holder or waiter, as its record describes it.
type Holder struct {
	// Slot is the slot index; -1 for a waiter.
	Slot    int
	PID     int
	Command string
	Since   time.Time
	// Subtree is set when the holder's descendants count as inside its slot.
	Subtree bool
}

func (h Holder) String() string {
	age := time.Since(h.Since).Round(time.Second)
	if h.Since.IsZero() {
		age = 0
	}
	cmd := h.Command
	if cmd == "" {
		cmd = "?"
	}
	return fmt.Sprintf("pid %d (%s) %s", h.PID, cmd, age)
}

func waitingLine(c Capacity, holders []Holder, ahead int, waited time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⏳ forge: waiting for a %s slot (%s)", c.Class, waited.Round(time.Second))
	if len(holders) > 0 {
		parts := make([]string, len(holders))
		for i, h := range holders {
			parts[i] = h.String()
		}
		fmt.Fprintf(&b, " — %d of %d held: %s", len(holders), c.Slots, strings.Join(parts, ", "))
	} else {
		fmt.Fprintf(&b, " — all %d held", c.Slots)
	}
	if ahead > 0 {
		fmt.Fprintf(&b, "; %d queued ahead", ahead)
	}
	fmt.Fprintf(&b, ". %d slots = %s; set %s to change, `forge slot status` to list.", c.Slots, c.Source, SlotsEnv(c.Class))
	return b.String()
}

// record is what a holder or waiter writes into its file.
type record struct {
	pid     int
	cmd     string
	since   time.Time
	nonce   string
	subtree bool
}

func (r record) encode() []byte {
	sub := "0"
	if r.subtree {
		sub = "1"
	}
	return []byte(fmt.Sprintf("pid=%d\ncmd=%s\nsince=%s\nnonce=%s\nsubtree=%s\n",
		r.pid, r.cmd, r.since.UTC().Format(time.RFC3339Nano), r.nonce, sub))
}

func (r record) holder(slot int) Holder {
	return Holder{Slot: slot, PID: r.pid, Command: r.cmd, Since: r.since, Subtree: r.subtree}
}

func readRecord(path string) (record, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return record{}, false
	}
	var r record
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "pid":
			r.pid, _ = strconv.Atoi(v)
		case "cmd":
			r.cmd = v
		case "since":
			r.since, _ = time.Parse(time.RFC3339Nano, v)
		case "nonce":
			r.nonce = v
		case "subtree":
			r.subtree = v == "1"
		}
	}
	return r, r.pid > 0
}

// queue is the waiting line: one locked ticket file per waiter, named so
// names sort by arrival.
type queue struct{ dir string }

type ticket struct {
	f    *os.File
	path string
	name string
}

// ticketGrace is how old an unlocked ticket must be before another waiter
// reaps it. A waiter creates its ticket and locks it microseconds later; the
// grace keeps a reaper from deleting a ticket inside that window.
const ticketGrace = 2 * time.Second

func (q queue) enqueue(rec record) (*ticket, error) {
	for attempt := 0; ; attempt++ {
		name := fmt.Sprintf("%019d-%d", time.Now().UnixNano(), os.Getpid())
		path := filepath.Join(q.dir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
		if errors.Is(err, os.ErrExist) && attempt < 5 {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("file a slot ticket: %w", err)
		}
		if ticketFiled != nil {
			ticketFiled(path)
		}
		if err := lockFreshTicket(f); err != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return nil, fmt.Errorf("lock slot ticket %s: %w", path, err)
		}
		_, _ = f.WriteAt(rec.encode(), 0)
		return &ticket{f: f, path: path, name: name}, nil
	}
}

// ticketFiled, when set (tests only), runs the moment a new ticket exists
// under its name and before its waiter has locked it.
var ticketFiled func(path string)

// freshTicketLockWait bounds how long a waiter keeps trying to lock the
// ticket it just created.
const freshTicketLockWait = time.Second

// lockFreshTicket locks a ticket this waiter has just created. Between the
// create and the lock the file is visible and unlocked, and another waiter's
// alive() probe may hold its lock right then: the probe takes the lock to
// test for a dead waiter, sees a ticket younger than ticketGrace, and lets
// go at once without removing it. A single try lost that race, and Acquire
// failed a healthy wait with "lock slot ticket …" (TestAcquire_
// NeverMoreThanNHolders, one Linux CI run in ~30). So try until the probe
// lets go; the bound is far above a probe's microseconds and far below the
// grace, so a ticket is never reaped while its waiter is still locking it.
func lockFreshTicket(f *os.File) error {
	deadline := time.Now().Add(freshTicketLockWait)
	for {
		ok, err := tryLock(f)
		switch {
		case err != nil:
			return err
		case ok:
			return nil
		case time.Now().After(deadline):
			return errors.New("another process holds the lock on a ticket this one just created")
		}
		time.Sleep(time.Millisecond)
	}
}

func (t *ticket) remove() {
	_ = unlock(t.f)
	_ = t.f.Close()
	_ = os.Remove(t.path)
}

// ahead counts the live tickets filed before mine ("" counts them all),
// reaping any whose waiter has died.
func (q queue) ahead(mine string) int { return len(q.live(mine)) }

// live lists the live waiters ahead of mine, oldest first.
func (q queue) live(mine string) []Holder {
	entries, err := os.ReadDir(q.dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var out []Holder
	for _, name := range names {
		if name == mine {
			break
		}
		path := filepath.Join(q.dir, name)
		if !q.alive(path, name) {
			continue
		}
		rec, _ := readRecord(path)
		out = append(out, rec.holder(-1))
	}
	return out
}

// alive reports whether a ticket's waiter still holds it, removing it when
// not (and it is past the grace window).
func (q queue) alive(path, name string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	ok, err := tryLock(f)
	if err != nil || !ok {
		_ = f.Close()
		return true
	}
	stale := true
	if ts, _, found := strings.Cut(name, "-"); found {
		if ns, err := strconv.ParseInt(ts, 10, 64); err == nil && time.Since(time.Unix(0, ns)) < ticketGrace {
			stale = false
		}
	}
	_ = unlock(f)
	_ = f.Close()
	if stale {
		_ = os.Remove(path)
		return false
	}
	return true
}
