// Copyright (c) 2025 Reliant Labs

package diskwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/reliant-labs/forge/pkg/gocache"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/version"
)

// The Go-cache floor audit.
//
// The Go build cache is shared by every build on the machine. A running build
// resolves an entry once and links against it much later, and Go restamps an
// entry's mtime only when it is more than an hour stale, so an entry younger
// than the floor may be in use. Deleting one fails that build with "could not
// import … no such file".
//
// forge's trim holds that floor (forge#1231; #1243: the cutoff is held back
// to the oldest running build; #1245: an entry's mtime is re-checked before
// it is unlinked), and the reaper runs THIS binary's forge, which go.mod pins
// at or above those fixes. The 2h floor binds at rest. When the entries older
// than it cannot bring the cache under budget, forge's size rule reaches
// toward its goCacheMinFloor, cmd/go's 1h restamp interval plus 5 minutes,
// and never past it. As defence in depth every pass that trims the Go cache
// leaves a durable record. A cutoff inside the 2h floor is a WARN, because
// forge does that only to stop a disk filling. A cutoff inside the minimum
// floor is an ERROR whatever forge said it would do. Until #2762 the audit
// knew only the 2h floor, so every trim under disk pressure logged an ERROR
// for a cutoff forge's contract allows.

const (
	// liveBuildFloor is the youngest Go-cache entry a trim deletes at rest:
	// forge/pkg/gocache's Floor, the one copy forge's trim reads too.
	liveBuildFloor = gocache.Floor

	// minLiveBuildFloor is the youngest Go-cache entry a trim may delete
	// under budget pressure: forge/pkg/gocache's MinFloor.
	minLiveBuildFloor = gocache.MinFloor

	// floorViolationSlack absorbs the clock read between the pass starting
	// and forge reading its own clock.
	floorViolationSlack = 5 * time.Second
)

// floorVerdict is how a pass was decided.
type floorVerdict string

const (
	verdictTrimmed floorVerdict = "trimmed"

	// The go_work_dirs pass's verdicts: it removed dead work directories,
	// found none proven dead, or could prove nothing (no /proc).
	verdictReclaimed   floorVerdict = "reclaimed"
	verdictNothingDead floorVerdict = "nothing_proven_dead"
	verdictUnproven    floorVerdict = "unproven"
)

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
	At          time.Time        `json:"at"`
	Pass        string           `json:"pass"`
	Trigger     string           `json:"trigger"`
	Verdict     floorVerdict     `json:"verdict"`
	Forge       string           `json:"forge"`
	Floor       string           `json:"floor"`
	Ran         bool             `json:"ran"`
	ExitCode    int              `json:"exit_code,omitempty"`
	RecordFresh bool             `json:"record_fresh"`
	Caches      []trimAuditCache `json:"caches,omitempty"`
	// WorkDirs is the go_work_dirs pass (gowork.go): killed builds' work
	// directories, removed once proven dead.
	WorkDirs *workDirAudit `json:"work_dirs,omitempty"`
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

// floorViolated reports whether a trim's cutoff reached inside the minimum
// floor of the pass that made it. A zero cutoff says nothing was selected by age.
func floorViolated(start, cutoff time.Time) bool {
	return !cutoff.IsZero() && start.Sub(cutoff) < minLiveBuildFloor-floorViolationSlack
}

// insideRestFloor reports whether a trim's cutoff reached inside the 2h
// floor: allowed only under budget pressure, down to minLiveBuildFloor.
func insideRestFloor(start, cutoff time.Time) bool {
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

// auditPass writes the durable record of one pass: what forge reported and,
// whatever forge said, whether its cutoff respected the floor.
func (w *Watcher) auditPass(ctx context.Context, policyPath, pass, trigger string, start time.Time, exit int, rec autoGCRecord, fresh bool) {
	a := trimAuditRecord{
		At: w.now(), Pass: pass, Trigger: trigger, Verdict: verdictTrimmed,
		Forge: version.Forge(), Floor: liveBuildFloor.String(),
		Ran: true, ExitCode: exit, RecordFresh: fresh,
	}
	if fresh {
		a.Caches = auditCaches(start, rec.GoCaches)
	}
	w.appendTrimAudit(ctx, policyPath, a)
}
