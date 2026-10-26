// Copyright (c) 2025 Reliant Labs

package diskwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/version"
)

// The Go-cache floor guard.
//
// The Go build cache is shared by every build on the machine. A running build
// resolves an entry once and links against it much later, and Go restamps an
// entry's mtime only when it is more than an hour stale, so an entry younger
// than the floor may be in use. Deleting one fails that build with "could not
// import … no such file".
//
// forge's trim holds that floor only from forge#1231 (the 2h floor holds even
// over budget), with #1243 (the cutoff is held back to the oldest running
// build) and #1245 (an entry's mtime is re-checked before it is unlinked).
// Before #1231 an over-budget trim reached down to go_cache_min_age (20m by
// default). A daemon that embeds such a forge deleted entries about an hour old
// every 10-15 minutes, on a machine whose cache fills at 27-33 GiB/h against a
// 40 GiB budget, so nearly every pass took that path.
//
// The reaper runs THIS binary's forge, so it knows which forge it is about to
// run. Before any pass that trims the Go cache it checks that forge has the
// floor, and refuses the pass when it does not.
//
// Which forge: this binary's own, not a project's pinned one. The shared cache
// belongs to the machine, not to a project: a machine holds many checkouts,
// each pinned at a different forge (a worktree pinned before 2026-10-23 runs
// the unsafe trim itself, and the pin hand-off of forge#1189 exists so a
// project's lint runs under that project's analyzers, which is the opposite
// question). The daemon has no project (the reaper runs in the temp directory
// on purpose), so there is no pin to take, and a pinned forge would have to be
// found, installed and version-checked per pass. The embedded forge is the one
// forge whose version reliant controls and ships together with the guard.

const (
	// MinFloorForge is the oldest forge whose Go-cache trim holds the
	// live-build floor: the pseudo-version of forge#1245 (681742bd9,
	// 2026-10-24T01:39:01Z), the last of the three fixes (#1231, #1243,
	// #1245). Bump it only to a forge that is at least as safe. go.mod's pin
	// must stay at or above it (TestMinFloorForgeIsNotAboveThisModulesForgePin).
	MinFloorForge = "v0.1.47-0.20261024013901-681742bd904e"

	// liveBuildFloor is the youngest Go-cache entry a trim may ever delete:
	// forge's goCacheFloor.
	liveBuildFloor = 2 * time.Hour
	// liveBuildFloorLabel is how messages spell it.
	liveBuildFloorLabel = "2h"

	// EmergencyFreeFloor is the free space on the volume holding the Go cache
	// below which a trim by a forge without the floor is allowed anyway: a
	// full disk fails every build, which is worse than one retryable build.
	// It is far below the pressure thresholds (2 GiB, or 90% full with under
	// 20 GiB free) on purpose: pressure alone is the normal state of this
	// machine and must not defeat the guard. Scaled down to an eighth of a
	// volume smaller than 8 GiB.
	EmergencyFreeFloor = GiB
	// emergencyInodeFraction is the inode use at which the same applies.
	emergencyInodeFraction = 0.98

	// floorViolationSlack absorbs the clock read between the pass starting
	// and forge reading its own clock.
	floorViolationSlack = 5 * time.Second
)

// forgeHasFloor reports whether forge version v holds the live-build floor and,
// when it does not, why not. A version that cannot be identified is treated as
// one that predates the floor.
func forgeHasFloor(v string) (bool, string) {
	if !semver.IsValid(v) {
		return false, fmt.Sprintf("its version %q cannot be identified", v)
	}
	if semver.Compare(v, MinFloorForge) < 0 {
		return false, fmt.Sprintf("it is older than %s", MinFloorForge)
	}
	return true, ""
}

func (s *reaperState) forgeVer() string {
	if s.forgeVersion != nil {
		return s.forgeVersion()
	}
	return version.Forge()
}

// floorVerdict is how a pass was decided.
type floorVerdict string

const (
	verdictTrimmed             floorVerdict = "trimmed"
	verdictSkippedStaleForge   floorVerdict = "skipped_stale_forge"
	verdictEmergencyStaleForge floorVerdict = "emergency_stale_forge"
)

// floorDecision is what the guard decided for one pass.
type floorDecision struct {
	verdict   floorVerdict
	forge     string
	reason    string
	emergency string // what is critically low, when it is
	freeBytes uint64
	below     uint64
}

func (d floorDecision) proceed() bool { return d.verdict != verdictSkippedStaleForge }

// checkFloor decides whether a pass that trims the Go cache may run, and says
// so in the log. pass is "full" (forge storage auto-gc) or "gocache".
func (w *Watcher) checkFloor(ctx context.Context, policyPath, pass, trigger string) floorDecision {
	v := w.reaper.forgeVer()
	d := floorDecision{verdict: verdictTrimmed, forge: v}
	ok, why := forgeHasFloor(v)
	if ok {
		return d
	}
	d.reason = why
	if label, free, below, critical := w.goCacheVolumeCritical(); critical {
		d.verdict = verdictEmergencyStaleForge
		d.emergency, d.freeBytes, d.below = label, free, below
		logging.ErrorContext(ctx, fmt.Sprintf("[diskwatch] Storage reaper: EMERGENCY, running the %s pass with embedded forge %s, which predates the %s live-build floor; "+
			"it may delete Go cache entries a running build links against (\"could not import … no such file\"). The disk is nearly full, which is worse. Update the daemon.", pass, v, liveBuildFloorLabel),
			"trigger", trigger, "forge", v, "min_forge", MinFloorForge, "reason", why, "volume", label, "free", FormatBytes(free), "emergency_below", FormatBytes(below))
		return d
	}
	d.verdict = verdictSkippedStaleForge
	logging.WarnContext(ctx, fmt.Sprintf("[diskwatch] Storage reaper: embedded forge %s predates the %s live-build floor; skipping the %s pass (it trims the Go cache) until the daemon is updated",
		v, liveBuildFloorLabel, pass),
		"trigger", trigger, "forge", v, "min_forge", MinFloorForge, "reason", why)
	return d
}

// goCacheVolumeCritical reports whether a volume holding a Go cache is so full
// that a trim is worth its risk: free space under EmergencyFreeFloor (an
// eighth of the volume when that is smaller) or inodes nearly gone.
func (w *Watcher) goCacheVolumeCritical() (label string, free, below uint64, critical bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.valid {
		return "", 0, 0, false
	}
	for _, v := range w.vols {
		if !v.has(RoleCache) {
			continue
		}
		u := v.usage()
		if u.TotalBytes == 0 {
			continue
		}
		floor := min(EmergencyFreeFloor, u.TotalBytes/8)
		if u.FreeBytes < floor || (u.FilesTotal > 0 && u.InodeFraction() >= emergencyInodeFraction) {
			return v.label(), u.FreeBytes, floor, true
		}
	}
	return "", 0, 0, false
}

const (
	trimAuditName = "gocache-trim-audit.jsonl"
	// trimAuditMaxBytes bounds the live log; one previous generation is kept
	// as trimAuditName+".1".
	trimAuditMaxBytes = 4 << 20
)

// trimAuditRecord is one line of the durable Go-cache trim log, beside the
// policy and forge's own pass records: every pass that trims the Go cache, or
// was refused, leaves one.
type trimAuditRecord struct {
	At                  time.Time        `json:"at"`
	Pass                string           `json:"pass"`
	Trigger             string           `json:"trigger"`
	Verdict             floorVerdict     `json:"verdict"`
	Reason              string           `json:"reason,omitempty"`
	Forge               string           `json:"forge"`
	MinForge            string           `json:"min_forge"`
	Floor               string           `json:"floor"`
	Emergency           bool             `json:"emergency,omitempty"`
	EmergencyVolume     string           `json:"emergency_volume,omitempty"`
	FreeBytes           uint64           `json:"free_bytes,omitempty"`
	EmergencyBelowBytes uint64           `json:"emergency_below_bytes,omitempty"`
	Ran                 bool             `json:"ran"`
	ExitCode            int              `json:"exit_code,omitempty"`
	RecordFresh         bool             `json:"record_fresh"`
	Caches              []trimAuditCache `json:"caches,omitempty"`
}

type trimAuditCache struct {
	Cache         string    `json:"cache"`
	Root          string    `json:"root"`
	EstimatedByte uint64    `json:"estimated_bytes"`
	BudgetBytes   uint64    `json:"budget_bytes"`
	InsideFloor   uint64    `json:"inside_floor_bytes"`
	Cutoff        time.Time `json:"cutoff"`
	CutoffAge     string    `json:"cutoff_age,omitempty"`
	Removed       int       `json:"removed"`
	FreedBytes    uint64    `json:"freed_bytes"`
	Shards        string    `json:"shards"`
	Finished      bool      `json:"finished"`
	FloorViolated bool      `json:"floor_violated,omitempty"`
}

// floorViolated reports whether a trim's cutoff reached inside the floor of
// the pass that made it. A zero cutoff says nothing was selected by age.
func floorViolated(start, cutoff time.Time) bool {
	return !cutoff.IsZero() && start.Sub(cutoff) < liveBuildFloor-floorViolationSlack
}

// auditCaches turns forge's trims into audit entries, marking any whose cutoff
// is inside the floor of the pass that started at start.
func auditCaches(start time.Time, trims []goCacheTrim) []trimAuditCache {
	out := make([]trimAuditCache, 0, len(trims))
	for _, t := range trims {
		c := trimAuditCache{
			Cache: t.Cache, Root: t.Root, EstimatedByte: t.EstimatedBytes, BudgetBytes: t.BudgetBytes,
			InsideFloor: t.YoungBytes, Cutoff: t.Cutoff, Removed: t.Removed, FreedBytes: t.FreedBytes,
			Shards: fmt.Sprintf("%d/%d", t.ShardsCovered, t.ShardsTotal), Finished: t.Finished,
			FloorViolated: floorViolated(start, t.Cutoff),
		}
		if !t.Cutoff.IsZero() {
			c.CutoffAge = start.Sub(t.Cutoff).Round(time.Second).String()
		}
		out = append(out, c)
	}
	return out
}

// appendTrimAudit appends rec to the durable log beside the policy. It never
// fails a pass: an unwritable log is a WARN.
func (w *Watcher) appendTrimAudit(ctx context.Context, policyPath string, rec trimAuditRecord) {
	if policyPath == "" {
		return
	}
	dir := filepath.Dir(policyPath)
	path := filepath.Join(dir, trimAuditName)
	if err := appendJSONLine(path, rec); err != nil {
		logging.WarnContext(ctx, "[diskwatch] Could not append to the Go cache trim audit log", "path", path, "error", err)
	}
}

func appendJSONLine(path string, rec any) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if st, err := os.Stat(path); err == nil && st.Size() >= trimAuditMaxBytes {
		if err := os.Rename(path, path+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// auditPass writes the durable record of one pass: the decision, what forge
// reported, and, whatever forge said, whether its cutoff respected the floor.
// An ERROR is logged for a cutoff inside the floor.
func (w *Watcher) auditPass(ctx context.Context, policyPath, pass, trigger string, start time.Time, d floorDecision, ran bool, exit int, rec autoGCRecord, fresh bool) {
	a := trimAuditRecord{
		At: w.now(), Pass: pass, Trigger: trigger, Verdict: d.verdict, Reason: d.reason,
		Forge: d.forge, MinForge: MinFloorForge, Floor: liveBuildFloor.String(),
		Emergency: d.emergency != "", EmergencyVolume: d.emergency, FreeBytes: d.freeBytes, EmergencyBelowBytes: d.below,
		Ran: ran, ExitCode: exit, RecordFresh: fresh,
	}
	if fresh {
		a.Caches = auditCaches(start, rec.GoCaches)
	}
	w.appendTrimAudit(ctx, policyPath, a)
}
