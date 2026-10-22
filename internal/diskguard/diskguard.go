// Copyright (c) 2025 Reliant Labs

// Package diskguard acts on a filling disk before it is full: past a reclaim
// threshold it frees what is safe to free, and past a refuse threshold it stops
// new heavy commands from starting, with a message that says what is using the
// space and how to free more.
//
// Why. A cloud workspace's volume reached 97% under a dozen agents and builds
// began dying at link time with ENOSPC — a failure that tells the agent
// nothing it can act on, can leave a half-written git object or index behind,
// and wastes the minutes the build ran. internal/diskwatch already sees the
// volumes filling (and reports them on the heartbeat); nothing acted on it.
//
// What it does, per volume work writes to (every volume diskwatch watches
// except the system one):
//
//   - past ReclaimAt (default 90% used) the volume is RECLAIMING: the guard
//     runs its Reclaimers at once and again every ReclaimEvery while it stays
//     there, and the heartbeat says so — which makes the server's worktree
//     sweep re-check this machine's kept workspaces now instead of after their
//     back-off (internal/worktreesweep).
//   - past RefuseAt (default 95% used, with under RefuseFreeCeiling free) it is
//     REFUSING_HEAVY: HeavyRefusal returns the message the daemon answers a
//     new heavy command with (internal/heavyjob). Light commands, and work
//     already running, are never touched.
//
// Each state clears ClearMargin below its threshold, so a cache filling and
// trimming around a line does not flap. The state, and what the last pass
// reclaimed, go out on the heartbeat on each volume (Annotate), where the
// chat's disk banner and the Inbox's storage item show them.
//
// Reclaimers are a plain interface the daemon fills in (New, Register): forge's
// storage reaper (diskwatch.Watcher.Reclaim) where the reaper routine is
// enabled, and anything else that can free space safely beside running builds
// is one more entry.
//
//forge:exclude-contract: one concrete policy over diskwatch's report; its collaborators (Source, Reclaimer) are the narrow interfaces declared here, and tests drive it through them
package diskguard

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Defaults. ReclaimAt is where cleaning up is cheap and still in time;
// RefuseAt is where the next few build bursts may not fit. The free ceiling
// keeps a big, mostly-free disk from refusing on percentage alone — 5% of a
// 1 TB laptop disk is 50 GB, room for any build — and matches the ceiling
// diskwatch's own pressure bit uses.
const (
	DefaultReclaimAt         = 0.90
	DefaultRefuseAt          = 0.95
	DefaultRefuseFreeCeiling = diskwatch.PressureFreeCeiling
	DefaultClearMargin       = 0.02
	DefaultInterval          = diskwatch.DefaultPollInterval
	DefaultReclaimEvery      = 15 * time.Minute
	// reclaimBudget bounds one reclaim pass.
	reclaimBudget = 30 * time.Minute
)

// Config is the guard's policy. Zero fields take the defaults.
type Config struct {
	// ReclaimAt and RefuseAt are fractions of a volume used. A value above 1
	// switches that action off.
	ReclaimAt float64
	RefuseAt  float64
	// RefuseFreeCeiling: a volume with at least this many bytes free is never
	// refused, whatever its percentage.
	RefuseFreeCeiling uint64
	// ClearMargin is how far below its threshold a state clears.
	ClearMargin float64
	// Interval is how often the volumes are checked.
	Interval time.Duration
	// ReclaimEvery is the least time between two reclaim passes while a
	// volume stays past ReclaimAt.
	ReclaimEvery time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

func (c Config) withDefaults() Config {
	if c.ReclaimAt <= 0 {
		c.ReclaimAt = DefaultReclaimAt
	}
	if c.RefuseAt <= 0 {
		c.RefuseAt = DefaultRefuseAt
	}
	if c.RefuseFreeCeiling == 0 {
		c.RefuseFreeCeiling = DefaultRefuseFreeCeiling
	}
	if c.ClearMargin <= 0 {
		c.ClearMargin = DefaultClearMargin
	}
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	if c.ReclaimEvery <= 0 {
		c.ReclaimEvery = DefaultReclaimEvery
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// ParseThresholds reads the thresholds as the daemon's config carries them
// (RELIANT_DISK_RECLAIM_AT, RELIANT_DISK_REFUSE_AT): a percentage of the
// volume used ("90", "95%"), or "off". A blank one keeps its default. A value
// it cannot read is an error naming it; the returned Config keeps the default
// for that one and applies the other.
func ParseThresholds(reclaimAt, refuseAt string) (Config, error) {
	var cfg Config
	var errs []string
	for _, f := range []struct {
		key string
		v   string
		dst *float64
	}{{"RELIANT_DISK_RECLAIM_AT", reclaimAt, &cfg.ReclaimAt}, {"RELIANT_DISK_REFUSE_AT", refuseAt, &cfg.RefuseAt}} {
		if strings.TrimSpace(f.v) == "" {
			continue
		}
		frac, err := parsePercent(f.v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s=%q: %v", f.key, f.v, err))
			continue
		}
		*f.dst = frac
	}
	if len(errs) > 0 {
		return cfg, fmt.Errorf("disk guard: %s", strings.Join(errs, "; "))
	}
	return cfg, nil
}

// parsePercent reads "90", "90%" or "off".
func parsePercent(v string) (float64, error) {
	v = strings.TrimSuffix(strings.TrimSpace(v), "%")
	if strings.EqualFold(v, "off") {
		return math.Inf(1), nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n <= 0 || n > 100 {
		return 0, fmt.Errorf("want a percentage of the volume used, 1-100, or off")
	}
	return n / 100, nil
}

// Source is where the guard reads the volumes: diskwatch.Watcher.
type Source interface {
	Report() (*reliantv1.DiskReport, bool)
}

// Reclaimer frees space on the machine when a volume is past the reclaim
// threshold. It must be safe to run beside builds and agents at work, and
// bounded by ctx. It reports what it freed, in total and in a few words for
// people ("Go build cache 3.1 GiB").
type Reclaimer interface {
	Reclaim(ctx context.Context) (freedBytes uint64, summary string, err error)
}

// Guard watches the volumes and acts on them. Safe for concurrent use; a nil
// *Guard does nothing and refuses nothing.
type Guard struct {
	cfg        Config
	src        Source
	reclaimers []Reclaimer

	mu         sync.Mutex
	vols       map[string]*volume
	reclaiming bool
	lastPass   time.Time

	changed chan struct{}
}

// volume is the guard's state for one volume, keyed by volumeKey.
type volume struct {
	state reliantv1.DiskGuardState
	// last is the volume as last read, for the refusal message.
	last *reliantv1.DiskVolume
	// The last reclaim pass started for this volume.
	reclaimedAt    time.Time
	reclaimedBytes uint64
	reclaimed      []string
}

// New builds a Guard over src. It does nothing until Run.
func New(cfg Config, src Source, reclaimers ...Reclaimer) *Guard {
	return &Guard{
		cfg:        cfg.withDefaults(),
		src:        src,
		reclaimers: reclaimers,
		vols:       map[string]*volume{},
		changed:    make(chan struct{}, 1),
	}
}

// Register adds a reclaimer: anything that can free space safely when a volume
// is past the reclaim threshold. Call it before Run.
func (g *Guard) Register(r Reclaimer) {
	if g == nil || r == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reclaimers = append(g.reclaimers, r)
}

// Changed receives a token when a volume's state changed or a reclaim pass
// finished, so the heartbeat can carry it at once. Nil-safe.
func (g *Guard) Changed() <-chan struct{} {
	if g == nil {
		return nil
	}
	return g.changed
}

func (g *Guard) signal() {
	select {
	case g.changed <- struct{}{}:
	default:
	}
}

// Run checks the volumes every Interval until ctx ends.
func (g *Guard) Run(ctx context.Context) {
	if g == nil {
		return
	}
	ticker := time.NewTicker(g.cfg.Interval)
	defer ticker.Stop()
	for {
		g.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Check reads the volumes once, steps each one's state, and starts a reclaim
// pass when one is due. Run calls it; tests call it directly.
func (g *Guard) Check(ctx context.Context) {
	rep, ok := g.src.Report()
	if !ok {
		return
	}
	now := g.cfg.Now()
	g.mu.Lock()
	changed := false
	seen := map[string]bool{}
	due := false
	for _, v := range rep.GetVolumes() {
		if !guarded(v) {
			continue
		}
		key := volumeKey(v)
		seen[key] = true
		st := g.vols[key]
		if st == nil {
			st = &volume{}
			g.vols[key] = st
		}
		st.last = v
		next := Next(st.state, usage(v), g.cfg)
		if next != st.state {
			changed = true
			logTransition(v, st.state, next)
			st.state = next
		}
		if next != reliantv1.DiskGuardState_DISK_GUARD_STATE_UNSPECIFIED {
			due = true
		}
	}
	for key, st := range g.vols {
		if !seen[key] {
			delete(g.vols, key)
			changed = changed || st.state != reliantv1.DiskGuardState_DISK_GUARD_STATE_UNSPECIFIED
		}
	}
	reclaimers := append([]Reclaimer(nil), g.reclaimers...)
	start := due && len(reclaimers) > 0 && !g.reclaiming &&
		(g.lastPass.IsZero() || now.Sub(g.lastPass) >= g.cfg.ReclaimEvery)
	if start {
		g.reclaiming, g.lastPass = true, now
	}
	g.mu.Unlock()

	if changed {
		g.signal()
	}
	if start {
		go g.reclaim(context.WithoutCancel(ctx), now, reclaimers)
	}
}

// reclaim runs every reclaimer once and records the outcome on each volume
// that is past its reclaim threshold now.
func (g *Guard) reclaim(ctx context.Context, startedAt time.Time, reclaimers []Reclaimer) {
	ctx, cancel := context.WithTimeout(ctx, reclaimBudget)
	defer cancel()
	var total uint64
	var parts []string
	for _, r := range reclaimers {
		freed, summary, err := r.Reclaim(ctx)
		total += freed
		if summary != "" {
			parts = append(parts, summary)
		}
		if err != nil {
			logging.WarnContext(ctx, "[diskguard] Reclaimer stopped", "error", err, "freed", diskwatch.FormatBytes(freed))
		}
	}
	logging.InfoContext(ctx, "[diskguard] Reclaimed disk space", "freed", diskwatch.FormatBytes(total), "what", strings.Join(parts, ", "))

	g.mu.Lock()
	g.reclaiming = false
	for _, st := range g.vols {
		if st.state != reliantv1.DiskGuardState_DISK_GUARD_STATE_UNSPECIFIED {
			st.reclaimedAt, st.reclaimedBytes, st.reclaimed = startedAt, total, parts
		}
	}
	g.mu.Unlock()
	g.signal()
}

// Next is the pure state step for one volume: REFUSING_HEAVY at or past
// RefuseAt with under RefuseFreeCeiling free, RECLAIMING at or past
// ReclaimAt, and each state held until the volume is ClearMargin below its
// threshold (and, for a refusal, has a fifth more than the ceiling free).
func Next(cur reliantv1.DiskGuardState, u diskwatch.Usage, cfg Config) reliantv1.DiskGuardState {
	cfg = cfg.withDefaults()
	if u.TotalBytes == 0 {
		return reliantv1.DiskGuardState_DISK_GUARD_STATE_UNSPECIFIED
	}
	used := u.UsedFraction()
	refusing := cur == reliantv1.DiskGuardState_DISK_GUARD_STATE_REFUSING_HEAVY
	switch {
	case used >= cfg.RefuseAt && u.FreeBytes < cfg.RefuseFreeCeiling,
		refusing && used >= cfg.RefuseAt-cfg.ClearMargin && u.FreeBytes < cfg.RefuseFreeCeiling*6/5:
		return reliantv1.DiskGuardState_DISK_GUARD_STATE_REFUSING_HEAVY
	case used >= cfg.ReclaimAt,
		cur != reliantv1.DiskGuardState_DISK_GUARD_STATE_UNSPECIFIED && used >= cfg.ReclaimAt-cfg.ClearMargin:
		return reliantv1.DiskGuardState_DISK_GUARD_STATE_RECLAIMING
	default:
		return reliantv1.DiskGuardState_DISK_GUARD_STATE_UNSPECIFIED
	}
}

// Annotate writes each volume's guard state, and what the last reclaim pass
// freed, onto a heartbeat's report.
func (g *Guard) Annotate(rep *reliantv1.DiskReport) {
	if g == nil || rep == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, v := range rep.Volumes {
		st := g.vols[volumeKey(v)]
		if st == nil {
			continue
		}
		v.Guard = st.state
		if !st.reclaimedAt.IsZero() {
			v.ReclaimedAtMs = st.reclaimedAt.UnixMilli()
			v.ReclaimedBytes = st.reclaimedBytes
			v.Reclaimed = append([]string(nil), st.reclaimed...)
		}
	}
}

// HeavyRefusal is why a new heavy command may not start now, or "" when it
// may. It names the fullest refusing volume, what is using it, what the guard
// already freed, and how to free more.
func (g *Guard) HeavyRefusal() string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var worst *volume
	for _, st := range g.vols {
		if st.state != reliantv1.DiskGuardState_DISK_GUARD_STATE_REFUSING_HEAVY {
			continue
		}
		if worst == nil || usage(st.last).UsedFraction() > usage(worst.last).UsedFraction() {
			worst = st
		}
	}
	if worst == nil {
		return ""
	}
	return refusalMessage(worst, g.cfg, len(g.reclaimers) > 0)
}

func refusalMessage(st *volume, cfg Config, reclaims bool) string {
	v := st.last
	u := usage(v)
	var b strings.Builder
	fmt.Fprintf(&b, "Refused: this machine's %q volume is %s full (%s free of %s), past the %s at which new heavy commands — builds, test suites, codegen — are not started, because they would fail mid-write with \"no space left on device\". Light commands, and work already running, are not affected.",
		v.Label, percent(u.UsedFraction()), diskwatch.FormatBytes(u.FreeBytes), diskwatch.FormatBytes(u.TotalBytes), percent(cfg.RefuseAt))
	if using := consumers(v); using != "" {
		fmt.Fprintf(&b, "\nUsing it: %s.", using)
	}
	switch {
	case !reclaims:
		// Nothing reclaims on this machine (a person's own, say): say nothing.
	case st.reclaimedAt.IsZero():
		b.WriteString("\nReclaiming: a clean-up of the build caches is running now.")
	case len(st.reclaimed) == 0:
		fmt.Fprintf(&b, "\nReclaimed automatically at %s: nothing was safe to remove.", st.reclaimedAt.Format("15:04 MST"))
	default:
		fmt.Fprintf(&b, "\nReclaimed automatically at %s: %s (%s).", st.reclaimedAt.Format("15:04 MST"), diskwatch.FormatBytes(st.reclaimedBytes), strings.Join(st.reclaimed, ", "))
	}
	// What an agent reads here is what it does next, so it names only actions
	// that cannot break someone else's work. An earlier "delete … build output
	// in idle checkouts" was read as licence to `rm -rf /tmp/cp-ci-affected.*`
	// mid-gate, deleting a running gate's state (2026-10-22).
	fmt.Fprintf(&b, "\nTo free more, safely: load the `machine-disk` skill, which shows what is using the disk; archive the worktrees you are done with (the worktree tool's delete; the storage item in the Inbox removes archived ones that are safe to delete). The build caches are trimmed by the daemon itself, beside running builds: do not run `go clean -cache` or prune docker images or volumes yourself. %s Heavy commands start again once the volume is below %s; until then, light commands still run.",
		diskwatch.NeverDelete, percent(cfg.RefuseAt-cfg.ClearMargin))
	return b.String()
}

// consumers is "Docker images 18 GiB, Go build cache 15 GiB, …", biggest
// first, or "" before diskwatch measured the volume.
func consumers(v *reliantv1.DiskVolume) string {
	const top = 4
	var parts []string
	for i, c := range v.GetConsumers() {
		if i == top {
			break
		}
		parts = append(parts, c.Label+" "+diskwatch.FormatBytes(c.Bytes))
	}
	return strings.Join(parts, ", ")
}

// guarded reports whether a volume holds anything work writes to: everything
// diskwatch watches except the volume that only holds system packages.
func guarded(v *reliantv1.DiskVolume) bool {
	return v.GetTotalBytes() > 0 && v.GetLabel() != "system"
}

func volumeKey(v *reliantv1.DiskVolume) string {
	key := v.GetLabel()
	if paths := v.GetPaths(); len(paths) > 0 {
		key += "|" + paths[0]
	}
	return key
}

func usage(v *reliantv1.DiskVolume) diskwatch.Usage {
	total, free, used := v.GetTotalBytes(), v.GetFreeBytes(), v.GetUsedBytes()
	if used == 0 && total > free {
		used = total - free // a report without df's Used
	}
	return diskwatch.Usage{TotalBytes: total, UsedBytes: used, FreeBytes: free, FilesTotal: v.GetFilesTotal(), FilesFree: v.GetFilesFree()}
}

func percent(f float64) string {
	return strconv.FormatFloat(math.Round(f*1000)/10, 'f', -1, 64) + "%"
}

func logTransition(v *reliantv1.DiskVolume, from, to reliantv1.DiskGuardState) {
	u := usage(v)
	attrs := []any{"volume", v.GetLabel(), "used", percent(u.UsedFraction()), "free", diskwatch.FormatBytes(u.FreeBytes), "from", from.String(), "to", to.String()}
	if to == reliantv1.DiskGuardState_DISK_GUARD_STATE_REFUSING_HEAVY {
		logging.Warn("[diskguard] Refusing new heavy commands", attrs...)
		return
	}
	logging.Info("[diskguard] Disk guard state changed", attrs...)
}
