package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Deletion governor: every unlink a maintenance pass makes goes through one.
//
// The 2026-10-09 incident: one `forge storage gc --apply` unlinked 175,324 Go
// build cache entries (53.6 GiB) in about 30 seconds, on a kata-containers
// volume served over virtiofs, while builds were running. Every lookup pins a
// file descriptor in the HOST's virtiofsd until the guest forgets the inode.
// The host daemon ran out of descriptors, the guest started failing with
// EMFILE although its own limit was a million, and seconds later the volume
// was gone from the container. The trim kept going through the EMFILEs,
// because a failed unlink was simply skipped.
//
// So a pass is bounded three ways, and none of them is optional:
//
//   - PACED. Unlinks are granted in batches of deleteBatch, and a batch may
//     not complete faster than the policy's delete_rate_per_sec allows. The
//     pause is taken while holding the pass's lock, so it stops every worker,
//     not one.
//   - CAPPED. One pass unlinks at most max_deletes_per_pass entries across
//     every layer. Whatever is left is retained, and the next pass continues.
//     A budget is CONVERGED over several passes, never chased in one — with
//     one exception: a cache over its SIZE budget may unlink what it takes to
//     get back under it (allowOver). Under agent load the Go build cache grows
//     faster than a capped pass can trim (one pass: 62 GiB estimated against a
//     20 GiB budget, 6.1 GiB freed before the cap), so a cap there means it
//     never converges. Pacing and back-off — what the incident below actually
//     needed — still apply to every one of those unlinks, and the pass's time
//     slice still ends it.
//   - BACKED OFF. The first EMFILE, ENFILE, ENOTCONN or EIO from any read or
//     unlink stops the pass: nothing more is unlinked, and later layers do not
//     start. Those errors mean the filesystem, or whatever serves it, is in
//     distress, and every further unlink adds to it. The rest is retained.

// Policy defaults for the governor.
const (
	defaultMaxDeletesPerPass        = 50_000
	defaultDeleteRatePerSec         = 1_000
	defaultMaxDockerRemovalsPerPass = 100
)

// deleteBatch is how many unlinks are granted between pacing pauses.
const deleteBatch = 200

// BackedOffError marks a pass that stopped because the filesystem reported
// distress. Like a cut-off it is not a failure: everything not yet removed is
// retained, and the next pass continues. It is reported separately because it
// says something about the machine, not about the time budget.
type BackedOffError struct {
	// Path is where the distress was observed.
	Path string
	Err  error
}

func (e *BackedOffError) Error() string {
	return fmt.Sprintf("backed off at %s: %v; stopped deleting and retained the rest for the next pass", e.Path, e.Err)
}

func (e *BackedOffError) Unwrap() error { return e.Err }

// errDeleteCap is wrapped in the CutOffError a layer gets once the pass has
// spent its delete budget.
var errDeleteCap = errors.New("per-pass delete cap reached")

// errDockerCap is the same for the pass's Docker removal budget.
var errDockerCap = errors.New("per-pass Docker removal cap reached")

// fsDistress reports whether err means the filesystem, or the process serving
// it, is failing, as opposed to one entry being unremovable. Out of
// descriptors (EMFILE, ENFILE), a disconnected FUSE/virtiofs transport
// (ENOTCONN), or an I/O error (EIO).
func fsDistress(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) ||
		errors.Is(err, syscall.ENOTCONN) || errors.Is(err, syscall.EIO)
}

// dockerDistress gives a Docker error that reports filesystem distress the
// errno fsDistress recognises. The daemon returns its errors as text, so the
// errno survives only in the message. Forge failing to start docker at all
// (fork/exec: EMFILE) already wraps the errno and is returned unchanged.
func dockerDistress(err error) error {
	if err == nil || fsDistress(err) {
		return err
	}
	msg := strings.ToLower(err.Error())
	for _, m := range []struct {
		text  string
		errno syscall.Errno
	}{
		{"too many open files in system", syscall.ENFILE},
		{"too many open files", syscall.EMFILE},
		{"transport endpoint is not connected", syscall.ENOTCONN},
		{"input/output error", syscall.EIO},
	} {
		if strings.Contains(msg, m.text) {
			return fmt.Errorf("%w: %v", m.errno, err)
		}
	}
	return err
}

// deletePass is the governor's state, shared by every layer of one pass.
type deletePass struct {
	mu        sync.Mutex
	limit     int
	rate      int
	count     int
	inBatch   int
	batchFrom time.Time
	backoff   *BackedOffError
	remove    func(string) error
	sleep     func(context.Context, time.Duration) error
	// overBudget counts unlinks granted from an over-budget allowance; they
	// are not in count.
	overBudget int
	// objects counts the Docker images and volumes granted this pass,
	// against objectLimit (max_docker_removals_per_pass).
	objects     int
	objectLimit int
}

// deleter grants unlinks from a pass. ceiling is the pass count at which this
// deleter stops granting: the whole pass's limit, or a lower share of it.
type deleter struct {
	pass    *deletePass
	ceiling int
	// over is an allowance of unlinks past ceiling that do not count against
	// the pass's cap (allowOver); nil means none. Guarded by pass.mu.
	over *int
}

func newDeleter(p Policy, remove func(string) error, sleep func(context.Context, time.Duration) error) deleter {
	limit := p.MaxDeletesPerPass
	if limit <= 0 {
		limit = defaultMaxDeletesPerPass
	}
	rate := p.DeleteRatePerSec
	if rate <= 0 {
		rate = defaultDeleteRatePerSec
	}
	if remove == nil {
		remove = os.Remove
	}
	if sleep == nil {
		sleep = sleepCtx
	}
	objects := p.MaxDockerRemovalsPerPass
	if objects <= 0 {
		objects = defaultMaxDockerRemovalsPerPass
	}
	return deleter{pass: &deletePass{limit: limit, rate: rate, remove: remove, sleep: sleep, objectLimit: objects}, ceiling: limit}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// share returns a deleter that may spend at most frac of what is left of this
// one's budget, so an early layer cannot spend the whole pass and starve the
// layers after it. Its unlinks still count against the pass.
func (d deleter) share(frac float64) deleter {
	p := d.pass
	p.mu.Lock()
	defer p.mu.Unlock()
	if frac >= 1 {
		return d
	}
	left := d.ceiling - p.count
	if left < 0 {
		left = 0
	}
	sub := int(float64(left) * frac)
	if sub < 1 && left > 0 {
		sub = 1
	}
	return deleter{pass: p, ceiling: p.count + sub}
}

// allowOver returns a deleter that, once its own budget is spent, may grant n
// more unlinks for a cache that is over its size budget. They are paced and
// backed off like every other unlink, but they are not counted against the
// pass's cap, so the layers after this one keep their shares.
func (d deleter) allowOver(n int) deleter {
	if n <= 0 {
		return d
	}
	d.over = &n
	return d
}

// overBudgetUnlinks is how many unlinks the pass granted from allowOver.
func (d deleter) overBudgetUnlinks() int {
	d.pass.mu.Lock()
	defer d.pass.mu.Unlock()
	return d.pass.overBudget
}

// backedOff returns the pass's back-off, or nil.
func (d deleter) backedOff() *BackedOffError {
	d.pass.mu.Lock()
	defer d.pass.mu.Unlock()
	return d.pass.backoff
}

// grant admits one unlink, pausing first when the batch is spent faster than
// the rate allows. It refuses once the pass has backed off, when this
// deleter's budget is spent (a CutOffError), and when ctx is done.
func (d deleter) grant(ctx context.Context) error {
	d.pass.mu.Lock()
	defer d.pass.mu.Unlock()
	return d.grantLocked(ctx)
}

func (d deleter) grantLocked(ctx context.Context) error {
	p := d.pass
	if p.backoff != nil {
		return p.backoff
	}
	fromAllowance := p.count >= d.ceiling
	if fromAllowance && (d.over == nil || *d.over <= 0) {
		return &CutOffError{Err: fmt.Errorf("%w (%d this pass; max_deletes_per_pass %d): the rest is retained and the next pass continues", errDeleteCap, p.count, p.limit)}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.inBatch >= deleteBatch {
		// Held under the lock on purpose: the pause stops every worker.
		if wait := time.Until(p.batchFrom.Add(time.Second * deleteBatch / time.Duration(p.rate))); wait > 0 {
			if err := p.sleep(ctx, wait); err != nil {
				return err
			}
		}
		p.inBatch = 0
	}
	if p.inBatch == 0 {
		p.batchFrom = time.Now()
	}
	p.inBatch++
	if fromAllowance {
		*d.over--
		p.overBudget++
	} else {
		p.count++
	}
	return nil
}

// reserve charges n unlinks another program will make — git removing a
// worktree — or refuses with the cap's CutOffError when fewer are left. They
// cannot be paced: forge does not make them.
func (d deleter) reserve(n int) error {
	p := d.pass
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.backoff != nil {
		return p.backoff
	}
	if p.count+n > d.ceiling {
		return &CutOffError{Err: fmt.Errorf("%w: needs %d unlinks, %d left this pass (max_deletes_per_pass %d)", errDeleteCap, n, max(d.ceiling-p.count, 0), p.limit)}
	}
	p.count += n
	return nil
}

// grantObject admits the removal of one Docker image or volume. The daemon
// makes that removal's unlinks, and one object can be thousands of files, so
// forge cannot pace them one by one. It grants the object as one paced unlink,
// so the pass's back-off, delete cap and deadline all apply, and counts it
// against max_docker_removals_per_pass, which is the bound on how much one pass
// asks the daemon to delete.
func (d deleter) grantObject(ctx context.Context) error {
	p := d.pass
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.backoff != nil {
		return p.backoff
	}
	if p.objects >= p.objectLimit {
		return &CutOffError{Err: fmt.Errorf("%w (%d this pass; max_docker_removals_per_pass %d): the rest is retained and the next pass continues", errDockerCap, p.objects, p.objectLimit)}
	}
	if err := d.grantLocked(ctx); err != nil {
		return err
	}
	p.objects++
	return nil
}

// observe records err from a read or unlink at path. A distress error backs
// the whole pass off and is returned as the pass's BackedOffError; anything
// else is returned unchanged for the caller to handle per entry.
func (d deleter) observe(path string, err error) error {
	if err == nil || !fsDistress(err) {
		return err
	}
	d.pass.mu.Lock()
	defer d.pass.mu.Unlock()
	return d.observeLocked(path, err)
}

func (d deleter) observeLocked(path string, err error) error {
	if err == nil || !fsDistress(err) {
		return err
	}
	if d.pass.backoff == nil {
		d.pass.backoff = &BackedOffError{Path: path, Err: err}
	}
	return d.pass.backoff
}

// unlink removes one file or empty directory through the governor. A path
// already gone is not an error.
//
// The remove happens under the pass's lock, so unlinks are serial across
// workers. That costs nothing the pacing has not already spent, and it is
// what makes "the first EMFILE is the last unlink" exact: no other worker can
// have one in flight when it arrives.
func (d deleter) unlink(ctx context.Context, path string) error {
	d.pass.mu.Lock()
	defer d.pass.mu.Unlock()
	if err := d.grantLocked(ctx); err != nil {
		return err
	}
	err := d.pass.remove(path)
	if err != nil && errors.Is(err, fs.ErrPermission) {
		// A read-only file on Windows, or an entry a module cache wrote 0444.
		_ = os.Chmod(path, 0o700)
		err = d.pass.remove(path)
	}
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return d.observeLocked(path, err)
}

// removeTree deletes a tree one governed unlink at a time, children before
// their directory, never following a symlink. Directories are made writable
// first: a Go module cache writes 0555 directories, whose children cannot
// otherwise be unlinked. Direct children named in keepLast go last, so a tree
// stopped partway still carries the marker that identifies it to the next
// pass. A stop (cap, back-off, deadline) leaves the rest in place.
func (d deleter) removeTree(ctx context.Context, path string, keepLast ...string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return d.observe(path, err)
	}
	if !info.IsDir() {
		return d.unlink(ctx, path)
	}
	if info.Mode().Perm()&0o700 != 0o700 {
		_ = os.Chmod(path, info.Mode().Perm()|0o700)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return d.observe(path, err)
	}
	var last []string
	for _, entry := range entries {
		if slices.Contains(keepLast, entry.Name()) {
			last = append(last, entry.Name())
			continue
		}
		if err := d.removeTree(ctx, filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	for _, name := range last {
		if err := d.removeTree(ctx, filepath.Join(path, name)); err != nil {
			return err
		}
	}
	return d.unlink(ctx, path)
}

// stopsPass reports whether err from a governed unlink means the layer must
// stop rather than skip one entry: a back-off, the delete cap, or the
// deadline.
func stopsPass(err error) bool {
	var backoff *BackedOffError
	var cut *CutOffError
	return errors.As(err, &backoff) || errors.As(err, &cut) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// deletes is the pass's governor, or a fresh one for a layer run on its own.
func (r Runner) deletes() deleter {
	if r.governor.pass != nil {
		return r.governor
	}
	return newDeleter(r.Policy, r.removeFn, r.sleepFn)
}

// backedOffErr is the pass's back-off as an error, or nil. A typed nil must
// never escape as a non-nil error.
func (r Runner) backedOffErr() error {
	if r.governor.pass == nil {
		return nil
	}
	if b := r.governor.backedOff(); b != nil {
		return b
	}
	return nil
}
