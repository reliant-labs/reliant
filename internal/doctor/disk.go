// Copyright (c) 2025 Reliant Labs
package doctor

// disk.go — the "Disk" check.
//
// It reports the two numbers that actually predict a disk-full failure on a
// developer machine, and the activation state of the storage policy that is
// supposed to prevent one.
//
// WHY THE OBVIOUS NUMBERS ARE THE WRONG ONES
//
// Docker Desktop's VM disk is a 1 TiB SPARSE file. Every percent-of-disk
// reading taken inside the VM therefore says "nearly empty" forever: the VM
// sees `/dev/vda1 1007G, 149G used` while the Mac it runs on is out of
// space. `docker info` exposes neither size nor free, and `docker system df`
// reports only the size of Docker objects — not what the host gave up to
// hold them. So every threshold expressed as a percentage, inside the VM or
// out of it, is blind to the failure being guarded against.
//
// The two decisive numbers are both on the HOST and both readable without
// Docker:
//
//   - host free/total bytes, by statfs. Taken for the PROJECT directory and
//     for os.TempDir() separately, because they are not always the same
//     filesystem — a build writes scratch to one and output to the other,
//     and either can be the one that runs out.
//   - Docker.raw's ALLOCATED bytes — st_blocks*512, what `du` reports — not
//     its apparent size. The apparent size of the file on this machine is
//     1,099,511,627,776 bytes on the day it holds 90 GiB. Reading `ls`'s
//     number instead of `du`'s is the single mistake that makes a disk
//     report useless, which is why allocated and apparent are BOTH in the
//     evidence: a reader who sees them side by side cannot repeat it.
//
// Docker.raw is absent on OrbStack (dynamic disk, returns space to macOS by
// itself), on colima (fixed-size Lima disk) and on Linux (no VM at all).
// Its absence is therefore not a finding and not a hole — the check simply
// stops reporting a number that does not exist there, silently.
//
// WHY ACTIVATION IS PART OF A DISK CHECK
//
// forge already ships the whole reclaim mechanism: budgets in the machine
// policy, `forge storage gc`, and a daily schedule. On the machine that ran
// out of disk, the policy file existed with no registries registered and no
// schedule installed, so none of it had ever run. A protection that must be
// switched on by hand is a protection that is off, and nothing reported
// that it was off. "Registries registered but no schedule installed" is
// therefore a WARN in its own right, independent of how much space is free
// today: it is the state that guarantees the bytes accumulate.
//
// NO COMMAND THAT CAN HANG
//
// Every fact here comes from statfs, stat, or reading a small local file.
// This check shells out to nothing — not `docker`, not `kubectl` — because
// a doctor check that blocks on an unreachable daemon is how a disk report
// gets removed from the default set. Each probe still runs under its own
// short timeout, since a statfs against a stale network mount can block in
// the kernel.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/storage"
)

// diskCheckName is the display name.
const diskCheckName = "Disk"

const (
	// diskProbeTimeout bounds ONE probe. Nothing here makes a network
	// call, but statfs against a stale NFS or SMB mount blocks in the
	// kernel, and $TMPDIR on a corporate laptop is occasionally such a
	// mount.
	diskProbeTimeout = 3 * time.Second

	// diskWarnFreeGiB is the free-space floor below which this check
	// warns, above the policy's own hard reserve. The reserve is where
	// forge REFUSES a build; by then the user is already stuck. The gap
	// between the two is the only window in which `forge storage gc` is a
	// chore rather than an emergency, and this machine crossed 70 GiB free
	// to failure with nothing said at any point.
	diskWarnFreeGiB = 50

	// diskGCStaleAfter is when a recorded last-GC timestamp stops being
	// reassuring. The installed schedule runs daily, so a last run older
	// than this means the schedule is present but not firing.
	diskGCStaleAfter = storage.FullGCStaleAfter
)

// hostSpace is one filesystem's capacity as statfs reports it, against the
// path that was asked about (not the mount point — the reader cares which
// of their directories is short).
type hostSpace struct {
	Path  string
	Free  uint64
	Total uint64
}

// dockerRawUsage is Docker Desktop's VM disk file, as the host sees it.
//
// Apparent is carried alongside Allocated purely so the evidence can show
// the trap rather than describe it: a 1 TiB apparent size next to a 90 GiB
// allocated size is self-explaining, and a reader who has seen the pair
// will not go on to quote `ls` at anyone.
type dockerRawUsage struct {
	Path      string
	Apparent  uint64
	Allocated uint64
}

// diskProbe is the seam to the machine, as funcs rather than an interface,
// for the same reason orphanProbe is: production supplies statfs and stat,
// and the tests supply fabricated capacities. Injecting statfs is not a
// convenience — a table test that asserted FAIL from the REAL disk would
// pass or fail according to how full the developer's laptop happened to be,
// which is the one thing a status-rule test must not depend on.
type diskProbe struct {
	// space reports free/total for one path's filesystem.
	space func(ctx context.Context, path string) (hostSpace, error)
	// dockerRaw is the candidate Docker.raw path on this platform, or ""
	// where no such file can exist (Linux, and any non-Docker-Desktop
	// runtime).
	dockerRaw func() string
	// usage reports Docker.raw's allocated and apparent bytes. ok is false
	// when the file does not exist, which is the normal state on OrbStack,
	// colima and Linux and is reported as nothing at all.
	usage func(ctx context.Context, path string) (u dockerRawUsage, ok bool, err error)
	// policyPath resolves the machine storage policy location.
	policyPath func() (string, error)
	// policy loads it. storage.Load answers with the DEFAULT policy and no
	// error when the file is absent, so "no policy file" arrives here as a
	// policy with no registries — which is exactly how it should be judged.
	policy func(path string) (storage.Policy, error)
	// schedule reports whether the daily GC schedule is installed, and the
	// path that was looked at so the evidence can name it.
	schedule func() (installed bool, where string)
	// fullGC reads the record the last APPLIED full GC wrote beside the
	// policy: when it ran, and whether it succeeded. ok is false when no
	// full pass has ever completed. Only the full pass runs registry
	// retention, so this, not any other timestamp, says whether the
	// registries are being reclaimed.
	fullGC func(policyPath string) (storage.GCResult, bool)
	// now is the clock, injected so a last-GC-age assertion is not a
	// function of when the test runs.
	now func() time.Time
}

// CheckDisk reports host free space, Docker Desktop's allocated disk, and
// whether forge's storage policy is actually active. See the file comment
// for why the host numbers are the only ones that predict a failure.
func CheckDisk(ctx context.Context, env *Environment) CheckResult {
	return checkDisk(ctx, env, diskProbe{
		space:      statfsSpace,
		dockerRaw:  dockerRawCandidate,
		usage:      dockerRawStat,
		policyPath: storage.DefaultPath,
		policy:     storage.Load,
		schedule:   storageScheduleInstalled,
		fullGC:     storage.LastFullGC,
		now:        time.Now,
	})
}

// checkDisk is the whole check minus the machine.
func checkDisk(ctx context.Context, env *Environment, probe diskProbe) CheckResult {
	var (
		spaces []hostSpace
		holes  []string
	)
	for _, p := range diskPaths(env) {
		s, err := probe.space(ctx, p)
		if err != nil {
			holes = append(holes, fmt.Sprintf("%s: %v", p, err))
			continue
		}
		spaces = append(spaces, s)
	}

	policyPath, perr := probe.policyPath()
	policy := storage.DefaultPolicy()
	if perr != nil {
		holes = append(holes, fmt.Sprintf("storage policy location: %v", perr))
	} else {
		// storage.Load returns the default policy alongside an error for a
		// malformed file, so take its value either way and record the
		// error as a hole: a policy forge cannot parse is also a policy
		// nothing is enforcing.
		p, lerr := probe.policy(policyPath)
		policy = p
		if lerr != nil {
			holes = append(holes, fmt.Sprintf("storage policy %s: %v", policyPath, lerr))
		}
	}

	raw, rawOK := dockerRawUsage{}, false
	if path := probe.dockerRaw(); path != "" {
		u, ok, err := probe.usage(ctx, path)
		switch {
		case err != nil:
			holes = append(holes, fmt.Sprintf("%s: %v", path, err))
		case ok:
			raw, rawOK = u, true
		}
	}

	scheduled, scheduleWhere := probe.schedule()
	var lastGC storage.GCResult
	lastGCOK := false
	if policyPath != "" {
		lastGC, lastGCOK = probe.fullGC(policyPath)
	}

	return summariseDisk(diskFacts{
		spaces:        spaces,
		holes:         holes,
		raw:           raw,
		rawOK:         rawOK,
		reserve:       policy.HostReserveGiB,
		policy:        policy,
		registries:    len(policy.Registries),
		policyPath:    policyPath,
		scheduled:     scheduled,
		scheduleWhere: scheduleWhere,
		lastGC:        lastGC,
		lastGCOK:      lastGCOK,
		now:           probe.now(),
	})
}

// diskPaths are the filesystems a build actually consumes: the project's
// own directory and the scratch directory. They are frequently the same
// filesystem and occasionally not, and the one that fills first is not
// predictable from the project — so both are measured and the TIGHTER one
// decides the status.
func diskPaths(env *Environment) []string {
	paths := []string{os.TempDir()}
	if env != nil && strings.TrimSpace(env.ProjectDir) != "" {
		paths = append([]string{env.ProjectDir}, paths...)
	}
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		if p = strings.TrimSpace(p); p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// diskFacts is everything one run learned.
type diskFacts struct {
	spaces        []hostSpace
	holes         []string
	raw           dockerRawUsage
	rawOK         bool
	reserve       uint64
	policy        storage.Policy
	registries    int
	policyPath    string
	scheduled     bool
	scheduleWhere string
	lastGC        storage.GCResult
	lastGCOK      bool
	now           time.Time
}

// retentionProblem is why registry retention is not known to be running, or
// "" — the same judgement `forge env up` prints (storage.FullGCProblem).
func (f diskFacts) retentionProblem() string {
	return storage.FullGCProblem(f.policy, f.lastGC, f.lastGCOK, f.now)
}

// tightest is the filesystem with the least free space — the one that will
// fail first, and therefore the only one the status may be derived from.
// Averaging, or reading only the project directory, is how a full $TMPDIR
// gets reported as healthy.
func (f diskFacts) tightest() (hostSpace, bool) {
	var out hostSpace
	found := false
	for _, s := range f.spaces {
		if !found || s.Free < out.Free {
			out, found = s, true
		}
	}
	return out, found
}

// The two literal fix commands. They are constants because the contract is
// that every non-PASS message ENDS with one of them: a reader who sees only
// the one-line summary — which is all `forge doctor` prints without -v —
// must be handed the next command rather than a diagnosis.
const (
	diskFixGC      = "forge storage gc --dry-run"
	diskFixInstall = "forge storage install"
	diskFixFullGC  = "forge storage gc --apply"
)

// summariseDisk applies the status rules: FAIL below the policy's host
// reserve, WARN below the warning floor or when registries are registered
// with nothing scheduled to reclaim them, PASS otherwise.
func summariseDisk(f diskFacts) CheckResult {
	tight, ok := f.tightest()
	evidence := diskEvidence(f)

	if !ok {
		// No filesystem answered, so the number this check exists to
		// report is the one it does not have. UNDETERMINED, never a pass.
		return CheckResult{
			Status:   StatusUnknown,
			Message:  "could not read free space for any build filesystem, so a disk-full build failure cannot be predicted — run: " + diskFixGC,
			Evidence: evidence,
		}
	}

	needSchedule := f.registries > 0 && !f.scheduled
	reserve := f.reserve * storage.GiB

	switch {
	case tight.Free < reserve:
		return CheckResult{
			Status: StatusFail,
			Message: fmt.Sprintf(
				"%s has %s free, below the %d GiB host reserve — forge will REFUSE builds here%s — run: %s",
				tight.Path, gib(tight.Free), f.reserve, diskScheduleSuffix(needSchedule), diskFix(needSchedule)),
			Evidence: evidence,
		}
	case tight.Free < diskWarnFreeGiB*storage.GiB:
		return CheckResult{
			Status: StatusWarn,
			Message: fmt.Sprintf(
				"%s has %s free, under the %d GiB floor and closing on the %d GiB reserve that refuses builds%s — run: %s",
				tight.Path, gib(tight.Free), diskWarnFreeGiB, f.reserve, diskScheduleSuffix(needSchedule), diskFix(needSchedule)),
			Evidence: evidence,
		}
	case needSchedule:
		return CheckResult{
			Status: StatusWarn,
			Message: fmt.Sprintf(
				"%d local registry/registries are registered for cleanup but no GC schedule is installed, so nothing ever reclaims them (%s free now) — run: %s",
				f.registries, gib(tight.Free), diskFixInstall),
			Evidence: evidence,
		}
	case f.retentionProblem() != "":
		// Scheduled, but not working: the last full pass failed, is older
		// than the daily schedule allows, or none ever completed. This used
		// to PASS, because the opportunistic pass stamped the same "last
		// GC" record — on failure too — and never runs the registry layer.
		return CheckResult{
			Status: StatusWarn,
			Message: fmt.Sprintf("%s, so registry images are not being reclaimed (%s free now) — run: %s",
				f.retentionProblem(), gib(tight.Free), diskFixFullGC),
			Evidence: evidence,
		}
	}

	return CheckResult{
		Status:   StatusPass,
		Message:  diskPassMessage(f, tight),
		Evidence: evidence,
	}
}

// diskFix picks the command that actually unblocks the reader. Low space
// with no schedule needs both, and the schedule is named LAST because it is
// the one that stops the problem recurring — and because the contract is
// that the line ends in a command, not in prose about one.
func diskFix(needSchedule bool) string {
	if needSchedule {
		return diskFixGC + ", then " + diskFixInstall
	}
	return diskFixGC
}

// diskScheduleSuffix carries "and nothing is scheduled to reclaim" onto a
// line that already has a space finding, rather than dropping it. A report
// that says "12 GiB free" while silently omitting "and no GC will ever run"
// describes the symptom and hides the cause.
func diskScheduleSuffix(needSchedule bool) string {
	if !needSchedule {
		return ""
	}
	return ", and no GC schedule is installed to reclaim it"
}

// diskPassMessage states the numbers on the happy path too. A disk check
// whose pass line says only "ok" leaves the reader with no baseline, so the
// next run's number means nothing to them.
func diskPassMessage(f diskFacts, tight hostSpace) string {
	parts := []string{fmt.Sprintf("%s free on %s", gib(tight.Free), tight.Path)}
	if f.rawOK {
		parts = append(parts, fmt.Sprintf("Docker.raw holds %s of host disk", gib(f.raw.Allocated)))
	}
	switch {
	case f.registries == 0:
		parts = append(parts, "no local registries registered for cleanup")
	case f.lastGCOK:
		parts = append(parts, fmt.Sprintf("GC scheduled, last full GC %s ago", age(f.now.Sub(f.lastGC.At))))
	default:
		parts = append(parts, "GC scheduled, no completed pass recorded yet")
	}
	return strings.Join(parts, "; ")
}

// diskEvidence is the full reading, in the order a reader needs it: the
// host numbers that decide the status, then the Docker.raw pair that proves
// the apparent size is a lie, then whether anything is actually enforcing
// the budgets.
func diskEvidence(f diskFacts) string {
	var b strings.Builder

	b.WriteString("Host filesystems (statfs — the numbers that predict a build failure):\n")
	spaces := append([]hostSpace(nil), f.spaces...)
	sort.Slice(spaces, func(i, j int) bool { return spaces[i].Path < spaces[j].Path })
	for _, s := range spaces {
		fmt.Fprintf(&b, "  %-40s %s free of %s\n", s.Path, gib(s.Free), gib(s.Total))
	}
	if len(spaces) == 0 {
		b.WriteString("  (none readable)\n")
	}
	fmt.Fprintf(&b, "  host reserve (policy): %d GiB — builds are refused below this\n", f.reserve)

	if f.rawOK {
		b.WriteString("\nDocker Desktop VM disk:\n")
		fmt.Fprintf(&b, "  %s\n", f.raw.Path)
		fmt.Fprintf(&b, "  allocated (du, st_blocks*512): %s  <- what the Mac has actually given up\n", gib(f.raw.Allocated))
		fmt.Fprintf(&b, "  apparent  (ls, sparse):        %s  <- NOT a disk reading; the file is sparse\n", gib(f.raw.Apparent))
		b.WriteString("  Deleting data inside the VM does release host bytes (DiskTRIM), but discard is\n" +
			"  batched, so the allocated figure can lag a prune by minutes.\n")
	}

	b.WriteString("\nStorage policy activation:\n")
	if f.policyPath != "" {
		fmt.Fprintf(&b, "  policy:     %s\n", f.policyPath)
	}
	fmt.Fprintf(&b, "  registries: %d registered for retention/GC\n", f.registries)
	if f.scheduled {
		fmt.Fprintf(&b, "  schedule:   installed (%s)\n", f.scheduleWhere)
	} else {
		fmt.Fprintf(&b, "  schedule:   NOT installed (looked for %s) — `%s` installs the daily pass\n",
			f.scheduleWhere, diskFixInstall)
	}
	switch {
	case f.lastGCOK:
		d := f.now.Sub(f.lastGC.At)
		fmt.Fprintf(&b, "  last full GC: %s ago (%s), %s\n", age(d), f.lastGC.At.Format(time.RFC3339), f.lastGC.Summary())
		if f.lastGC.Error != "" {
			fmt.Fprintf(&b, "                %s\n", f.lastGC.Error)
		}
		if d > diskGCStaleAfter {
			fmt.Fprintf(&b, "                older than %s — the daily pass is not firing\n", age(diskGCStaleAfter))
		}
	default:
		b.WriteString("  last full GC: no completed pass recorded\n")
	}

	if len(f.holes) > 0 {
		b.WriteString("\nCould not obtain these facts (so the reading above may be incomplete):\n")
		b.WriteString(evidenceLines(f.holes))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// gib renders bytes the way a human compares them to a budget expressed in
// GiB. Bytes are unreadable at this scale and percentages are meaningless
// against a sparse 1 TiB file.
func gib(b uint64) string {
	return fmt.Sprintf("%.1f GiB", float64(b)/float64(storage.GiB))
}

// age renders a duration coarsely — "3d" and "4h" are the resolutions that
// change a decision; minutes never do.
func age(d time.Duration) string {
	switch {
	case d < 0:
		return "0h"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// statfsSpace reads one filesystem's capacity, reusing internal/storage's
// per-platform statfs so there is one implementation of "how much space is
// on this host" and the doctor report cannot disagree with the admission
// check that refuses the build.
func statfsSpace(ctx context.Context, path string) (hostSpace, error) {
	type result struct {
		d   storage.Disk
		err error
	}
	ch := make(chan result, 1)
	go func() {
		d, err := storage.DiskSpace(path)
		ch <- result{d: d, err: err}
	}()
	tctx, cancel := context.WithTimeout(ctx, diskProbeTimeout)
	defer cancel()
	select {
	case r := <-ch:
		if r.err != nil {
			return hostSpace{Path: path}, r.err
		}
		return hostSpace{Path: path, Free: r.d.Available, Total: r.d.Capacity}, nil
	case <-tctx.Done():
		// A statfs that has not returned in three seconds is a stale
		// network mount. Reporting the hole is honest; waiting for the
		// kernel is how a doctor run appears to hang.
		return hostSpace{Path: path}, fmt.Errorf("statfs did not answer within %s (stale network mount?)", diskProbeTimeout)
	}
}

// dockerRawCandidate is where Docker Desktop keeps its VM disk on macOS,
// and "" everywhere else. It does not test for existence — the caller does,
// and an absent file is reported as nothing rather than as a finding.
func dockerRawCandidate() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Containers", "com.docker.docker", "Data", "vms", "0", "data", "Docker.raw")
}

// dockerRawStat reads Docker.raw's allocated and apparent sizes. See
// allocatedBytes for why the allocated figure needs a per-platform read.
func dockerRawStat(ctx context.Context, path string) (dockerRawUsage, bool, error) {
	if err := ctx.Err(); err != nil {
		return dockerRawUsage{}, false, err
	}
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		// OrbStack, colima, Linux, or Docker Desktop never installed.
		// Not a hole: there is no such number on this machine.
		return dockerRawUsage{}, false, nil
	}
	if err != nil {
		return dockerRawUsage{}, false, err
	}
	alloc, ok := allocatedBytes(fi)
	if !ok {
		// The platform cannot report allocation. Reporting the apparent
		// size alone would be worse than silence — it is 1 TiB.
		return dockerRawUsage{}, false, nil
	}
	return dockerRawUsage{Path: path, Apparent: uint64(fi.Size()), Allocated: alloc}, true, nil
}

// storageScheduleInstalled reports whether the daily GC schedule exists, by
// looking for the file `forge storage install` writes.
//
// It reads the filesystem rather than asking launchctl or systemctl. A
// `launchctl print` against a loaded-but-wedged domain can block, and this
// check's whole premise is that it cannot hang; the artefact's presence is
// also the thing the user can see and delete.
func storageScheduleInstalled() (bool, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, "the user home directory (unreadable)"
	}
	var path string
	switch runtime.GOOS {
	case "darwin":
		path = filepath.Join(home, "Library", "LaunchAgents", "com.reliant.forge-storage.plist")
	case "linux":
		path = filepath.Join(home, ".config", "systemd", "user", "forge-storage.timer")
	default:
		return false, fmt.Sprintf("no schedule artefact on %s (schedule `forge storage gc --apply` with your service manager)", runtime.GOOS)
	}
	_, serr := os.Stat(path)
	return serr == nil, path
}
