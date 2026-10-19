// Copyright (c) 2025 Reliant Labs

package diskwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// The storage reaper routine: the daemon runs forge's own reaper on a schedule.
//
// Why the daemon does it. A cloud machine has no systemd timer or cron, so
// `forge storage install` cannot register its daily job, and the only other
// trigger — the pass `forge env up` starts in the background — is rate-limited
// to once a day and only fires after an `up`. Under agent load the shared Go
// build cache grows ~40 GB an hour, so a daily trim cannot hold its budget and
// age cutoffs free nothing (every entry is hours old). What bounds the cache is
// the budget in forge's storage policy (go_cache_gib), enforced by forge's
// reaper. This file only decides WHEN to run it.
//
// What it runs: `<this binary> forge storage auto-gc --policy <path>` — the
// bounded, non-disruptive pass (rotated logs, Go caches, dangling images,
// builder cache, temp sweep, source cache). It never removes worktrees,
// anonymous volumes or registry images, and never takes a registry offline;
// those stay with an explicit `forge storage gc --apply`. forge owns every
// protection and budget; nothing here widens its scope.
//
// When:
//   - under pressure: a watched volume the reaper can relieve (a cache, docker
//     or temp volume) is past diskwatch's own pressure threshold — at most
//     every ReaperConfig.PressureInterval.
//   - on a schedule: every ReaperConfig.Interval regardless of pressure,
//     because the cache budget is the real control and pressure only appears
//     once the disk is nearly full.
//
// Each scheduled or pressure pass first trims the caches forge does not own —
// npm, the Go module cache above its ceiling, killed builds' go-build work
// dirs — to budgets derived from their volume (cachetrim.go), in the same
// single-flight. Watcher.TrimCaches runs both at once for a caller that needs
// space now.
//
// The cadence is anchored on forge's own record of its last pass
// (last-auto-gc.json, beside the policy), not on process memory: a restarting
// daemon, several daemons on one machine and a `forge env up` all share one
// rhythm instead of each starting a pass.

const (
	// Reaper modes (RELIANT_STORAGE_REAPER).
	ReaperModeAuto = "auto"
	ReaperModeOn   = "on"
	ReaperModeOff  = "off"

	// DefaultReaperInterval is the slow, pressure-independent pass: hourly,
	// because the cache grows ~40 GB/h and forge's own docs call for an hourly
	// pass to hold a 60 GiB budget at current agent load.
	DefaultReaperInterval = time.Hour
	// MinReaperInterval floors the slow pass: the reaper walks cache shards.
	MinReaperInterval = 5 * time.Minute
	// DefaultReaperPressureInterval is how soon a pass may repeat while a
	// volume stays under pressure: soon enough to relieve a filling disk, long
	// enough that a pass which can free nothing (everything recent) is not
	// re-run in a loop.
	DefaultReaperPressureInterval = 10 * time.Minute
	MinReaperPressureInterval     = time.Minute

	// reaperPollInterval is how often the routine decides whether a pass is
	// due: a statfs-backed pressure read and one small file read.
	reaperPollInterval = time.Minute
	// reaperPassBudget bounds one pass. forge bounds its own at 15 minutes and
	// records a cut-off layer as such; this is the outer wall for a wedged
	// child, with reaperKillGrace for it to stop on SIGTERM before it is killed.
	reaperPassBudget = 20 * time.Minute
	reaperKillGrace  = 30 * time.Second
	// reaperOutputTail caps the stdout/stderr kept per pass.
	reaperOutputTail = 4096

	autoGCRecordName = "last-auto-gc.json"
	// forge's lock error for a pass that finds another one running.
	reaperBusyMarker = "already running"
)

// ReaperConfig says whether and how often the daemon runs the reaper. The zero
// value is disabled.
type ReaperConfig struct {
	Enabled          bool
	Interval         time.Duration
	PressureInterval time.Duration
	// PolicyPath is forge's machine storage policy file; empty resolves to
	// <user config dir>/forge/storage.json like forge's own default.
	PolicyPath string
}

// ParseReaperConfig turns the typed config strings (RELIANT_STORAGE_REAPER*)
// into a ReaperConfig. managed is whether the platform declared this a managed
// daemon: "auto" enables the routine there and leaves a person's own machine
// alone. A bad or out-of-range value falls back or is clamped, and is named in
// the returned warnings; it never stops the daemon.
func ParseReaperConfig(mode, interval, pressureInterval, policyPath string, managed bool) (ReaperConfig, []string) {
	var warnings []string
	cfg := ReaperConfig{PolicyPath: strings.TrimSpace(policyPath)}
	switch m := strings.ToLower(strings.TrimSpace(mode)); m {
	case ReaperModeOn:
		cfg.Enabled = true
	case ReaperModeOff:
		cfg.Enabled = false
	case "", ReaperModeAuto:
		cfg.Enabled = managed
	default:
		cfg.Enabled = managed
		warnings = append(warnings, fmt.Sprintf("RELIANT_STORAGE_REAPER=%q is not auto, on or off; using auto", mode))
	}
	var w string
	cfg.Interval, w = parseReaperDuration("RELIANT_STORAGE_REAPER_INTERVAL", interval, DefaultReaperInterval, MinReaperInterval)
	warnings = appendIf(warnings, w)
	cfg.PressureInterval, w = parseReaperDuration("RELIANT_STORAGE_REAPER_PRESSURE_INTERVAL", pressureInterval, DefaultReaperPressureInterval, MinReaperPressureInterval)
	warnings = appendIf(warnings, w)
	return cfg, warnings
}

func appendIf(list []string, s string) []string {
	if s == "" {
		return list
	}
	return append(list, s)
}

func parseReaperDuration(name, raw string, def, floor time.Duration) (time.Duration, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, ""
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return def, fmt.Sprintf("%s=%q is not a duration; using %s", name, raw, def)
	}
	if d < floor {
		return floor, fmt.Sprintf("%s=%s is below the %s minimum; using %s", name, d, floor, floor)
	}
	return d, ""
}

// reaperRun is one finished invocation of the reaper.
type reaperRun struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// reaperRunner runs one reaper pass and returns once it has exited. A non-zero
// exit is a result (reaperRun.ExitCode), not an error; err is for a command
// that could not run at all or was stopped by ctx.
type reaperRunner func(ctx context.Context, policyPath string) (reaperRun, error)

// reaperState is the routine's state on a Watcher.
type reaperState struct {
	// sem is the single-flight: at most one pass per daemon at a time, whether
	// the schedule or a user's "clear caches" started it.
	sem chan struct{}
	run reaperRunner
	// budget overrides reaperPassBudget (tests).
	budget time.Duration

	// guarded by Watcher.mu
	cfg     ReaperConfig
	lastRun time.Time
}

// ConfigureReaper sets the routine's schedule. Call before RunReaper.
func (w *Watcher) ConfigureReaper(cfg ReaperConfig) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.reaper.cfg = cfg
	w.mu.Unlock()
}

func (w *Watcher) reaperConfig() ReaperConfig {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reaper.cfg
}

// RunReaper runs the scheduled routine until ctx is done. It returns at once
// when the routine is disabled. Nil-safe.
func (w *Watcher) RunReaper(ctx context.Context) {
	if w == nil {
		return
	}
	cfg := w.reaperConfig()
	if !cfg.Enabled {
		logging.Info("[diskwatch] Storage reaper schedule disabled (RELIANT_STORAGE_REAPER=auto runs it on managed daemons only; on enables, off disables)")
		return
	}
	logging.Info("[diskwatch] Storage reaper scheduled", "interval", cfg.Interval, "pressure_interval", cfg.PressureInterval, "policy", w.reaperPolicyPath(cfg))
	ticker := time.NewTicker(reaperPollInterval)
	defer ticker.Stop()
	for {
		w.reaperTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// reaperTick runs one pass if one is due and none is running. It reports
// whether it ran one.
func (w *Watcher) reaperTick(ctx context.Context) (ran bool) {
	cfg := w.reaperConfig()
	if !cfg.Enabled || ctx.Err() != nil {
		return false
	}
	now := w.now()
	trigger, interval := "scheduled", cfg.Interval
	if w.reclaimablePressure() {
		trigger, interval = "pressure", cfg.PressureInterval
	}
	policy := w.reaperPolicyPath(cfg)
	if last := w.lastReaperRun(policy); !last.IsZero() && now.Sub(last) < interval {
		return false
	}
	if !w.tryAcquireReaper() {
		return false
	}
	defer w.releaseReaper()
	// The caches forge does not own (npm, the module cache's ceiling, killed
	// builds' work dirs) on the same cadence; under pressure, to the urgent
	// budgets.
	w.trimNativeCaches(ctx, trigger == "pressure", false, trigger)
	w.reaperPass(ctx, policy, trigger)
	return true
}

func (w *Watcher) tryAcquireReaper() bool {
	select {
	case w.reaper.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// acquireReaper waits for the running pass to finish, or ctx.
func (w *Watcher) acquireReaper(ctx context.Context) bool {
	select {
	case w.reaper.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (w *Watcher) releaseReaper() { <-w.reaper.sem }

// reclaimablePressure reports whether a volume the reaper can relieve is under
// pressure. A system-only volume (the overlays over /etc /usr) is not: nothing
// the reaper removes lives there.
func (w *Watcher) reclaimablePressure() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.valid {
		return false
	}
	for _, v := range w.vols {
		if w.pressure[v.key] && reaperRelieves(v) {
			return true
		}
	}
	return false
}

func reaperRelieves(v *volume) bool {
	return v.has(RoleCache) || v.has(RoleDocker) || v.has(RoleTemp)
}

// reclaimableFree is the free bytes across the volumes the reaper can relieve.
func (w *Watcher) reclaimableFree() uint64 {
	var free uint64
	for _, v := range groupVolumes(w.targetList(), w.stat) {
		if reaperRelieves(v) {
			free += v.usage().FreeBytes
		}
	}
	return free
}

// lastReaperRun is the later of this process's last pass and forge's record of
// its last pass anywhere on the machine; zero when neither exists.
func (w *Watcher) lastReaperRun(policyPath string) time.Time {
	w.mu.Lock()
	last := w.reaper.lastRun
	w.mu.Unlock()
	if rec, err := readAutoGCRecord(policyPath); err == nil && rec.At.After(last) {
		last = rec.At
	}
	return last
}

func (w *Watcher) reaperPolicyPath(cfg ReaperConfig) string {
	if cfg.PolicyPath != "" {
		return cfg.PolicyPath
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "forge", "storage.json")
}

// autoGCRecord is the part of forge's last-auto-gc.json this reads: the
// machine-readable summary of a pass.
type autoGCRecord struct {
	At              time.Time `json:"at"`
	OK              bool      `json:"ok"`
	FailedLayers    []string  `json:"failed_layers"`
	Error           string    `json:"error"`
	CutOffLayers    []string  `json:"cut_off_layers"`
	BackedOffLayers []string  `json:"backed_off_layers"`
}

func readAutoGCRecord(policyPath string) (autoGCRecord, error) {
	var rec autoGCRecord
	if policyPath == "" {
		return rec, os.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(policyPath), autoGCRecordName))
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return autoGCRecord{}, err
	}
	return rec, nil
}

// ReaperResult is what one pass did, for callers that report it.
type ReaperResult struct {
	// Ran is false when the pass could not start (forge missing, no policy
	// location) or was cut short by ctx.
	Ran bool
	// Freed is the growth of free space on the volumes the reaper relieves
	// across the pass. forge reports no byte total, so this is a measurement,
	// and an underestimate while anything else writes to the disk.
	Freed uint64
	// CutOff and BackedOff are the layers forge stopped early (time slice or
	// filesystem distress); they resume on the next pass.
	CutOff, BackedOff []string
	// Failed is the layers that errored.
	Failed []string
}

// reaperPass runs the reaper once and logs the outcome. The caller holds the
// single-flight. It never panics and never returns an error: a failure is a
// WARN line, because a failed clean-up must not take the daemon down.
func (w *Watcher) reaperPass(ctx context.Context, policyPath, trigger string) (res ReaperResult) {
	defer func() {
		if r := recover(); r != nil {
			logging.Warn("[diskwatch] Storage reaper pass panicked", "trigger", trigger, "panic", fmt.Sprint(r))
		}
	}()
	budget := w.reaper.budget
	if budget <= 0 {
		budget = reaperPassBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	start := w.now()
	freeBefore := w.reclaimableFree()
	run, err := w.reaper.run(ctx, policyPath)
	freeAfter := w.reclaimableFree()
	// Stamp the attempt whatever happened, so a failing reaper is retried at
	// the interval and not on every poll.
	w.mu.Lock()
	w.reaper.lastRun = w.now()
	w.mu.Unlock()
	elapsed := w.now().Sub(start).Round(time.Second)
	if freeAfter > freeBefore {
		res.Freed = freeAfter - freeBefore
	}

	switch {
	case err != nil && ctx.Err() != nil:
		logging.Warn("[diskwatch] Storage reaper pass stopped; forge resumes on the next pass", "trigger", trigger, "after", elapsed, "budget", budget, "error", ctx.Err())
		return res
	case err != nil:
		logging.Warn("[diskwatch] Storage reaper could not run (is forge available in this binary?)", "trigger", trigger, "error", err)
		return res
	}
	res.Ran = true

	if run.ExitCode != 0 && strings.Contains(run.Stderr, reaperBusyMarker) {
		logging.Info("[diskwatch] Storage reaper skipped: another storage pass holds forge's lock", "trigger", trigger)
		return res
	}

	rec, recErr := readAutoGCRecord(policyPath)
	fresh := recErr == nil && !rec.At.Before(start.Add(-time.Second))
	if fresh {
		res.CutOff, res.BackedOff, res.Failed = rec.CutOffLayers, rec.BackedOffLayers, rec.FailedLayers
	}
	fields := []any{
		"trigger", trigger, "after", elapsed, "freed", FormatBytes(res.Freed),
		"free_before", FormatBytes(freeBefore), "free_after", FormatBytes(freeAfter),
		"cut_off", strings.Join(res.CutOff, ", "), "backed_off", strings.Join(res.BackedOff, ", "),
	}
	switch {
	case run.ExitCode != 0:
		fields = append(fields, "exit", run.ExitCode, "failed", strings.Join(res.Failed, ", "), "stderr", run.Stderr)
		if fresh && rec.Error != "" {
			fields = append(fields, "error", rec.Error)
		}
		logging.Warn("[diskwatch] Storage reaper pass failed", fields...)
	case !fresh:
		// Exit 0 but forge's summary is missing or unreadable: the pass ran, but
		// what it skipped is unknown. Say so rather than report a clean result.
		reason := "no record of this pass"
		if recErr != nil {
			reason = recErr.Error()
		}
		fields = append(fields, "record", reason)
		logging.Warn("[diskwatch] Storage reaper pass finished but its summary is unreadable", fields...)
	default:
		logging.Info("[diskwatch] Storage reaper pass finished", fields...)
	}
	if out := strings.TrimSpace(run.Stdout); out != "" {
		logging.Debug("[diskwatch] Storage reaper output (tail)", "trigger", trigger, "output", out)
	}
	return res
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append(t.b[:0], t.b[len(t.b)-t.max:]...)
	}
	return n, nil
}

func (t *tailBuffer) String() string { return strings.TrimSpace(string(t.b)) }
