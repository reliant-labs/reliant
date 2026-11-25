package testcollect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// The reset record: everything `forge test collect --reset` leaves for later
// collects of one workload, in ONE file, .forge/tests/covdata/<env>/.reset/<workload>:
// the fence of counter files flushed before the reset (fence.go) and the
// baselines of the live processes (baseline.go). A reset stages both and
// publishes them with one atomic write, so a later collect sees the whole
// reset or none of it: never a baseline without its fence, or the reverse.

// resetSchema names the record's format.
const resetSchema = "forge.test.collect.reset/v2"

// resetRecord is the record.
type resetRecord struct {
	Schema string `json:"schema"`
	// ResetAt is when the newest reset ran. It is a record for people, never
	// a cut-off.
	ResetAt time.Time `json:"reset_at"`
	// Fenced are the counter files present at a reset, sorted. The fence
	// only grows: a later reset adds what it sees.
	Fenced []fenceEntry `json:"fenced"`
	// Baselines are the live processes' counters at a reset, sorted. A
	// later reset replaces the baseline of a process it sees again and
	// keeps the others (a pod it could not reach keeps the earlier one).
	Baselines []baselineProcess `json:"baselines"`
}

// writeResetFile publishes a record; a variable so a test can make it fail.
var writeResetFile = atomicfile.WriteFile

// readReset reads the workload's record: the zero record when no reset ran,
// which is only when the file is confirmed absent. Any other failure (I/O,
// permission, a corrupt or foreign document) is an error: treating it as "no
// reset" would collect what a reset left out, or lose its fences and
// baselines on the next write.
func readReset(path string) (resetRecord, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return resetRecord{}, nil
	}
	if err != nil {
		return resetRecord{}, fmt.Errorf("reset record %s: %w", path, err)
	}
	var r resetRecord
	if err := json.Unmarshal(b, &r); err != nil || r.Schema != resetSchema {
		return resetRecord{}, fmt.Errorf("%s is not a %s reset record", path, resetSchema)
	}
	return r, nil
}

// fence is the record's fence, for lookups.
func (r resetRecord) fence() fenceSet {
	s := make(fenceSet, len(r.Fenced))
	for _, e := range r.Fenced {
		s[e] = true
	}
	return s
}

// baselines are the record's baselines, by process.
func (r resetRecord) baselines() map[processKey]baselineProcess {
	out := make(map[processKey]baselineProcess, len(r.Baselines))
	for _, p := range r.Baselines {
		out[p.key()] = p
	}
	return out
}

// publishReset merges what a reset saw into the workload's record and
// publishes it with one atomic write. On any failure, an unreadable previous
// record included, the record on disk is the one before the reset: an
// unreadable record is never replaced, since its fences and baselines would
// be lost. Fix or remove it by hand.
func publishReset(path string, at time.Time, seen []fenceEntry, taken []baselineProcess) error {
	prev, err := readReset(path)
	if err != nil {
		return fmt.Errorf("%w (left as it is; fix or remove it to reset again)", err)
	}
	fenced := append(slices.Clone(prev.Fenced), seen...)
	slices.SortFunc(fenced, fenceEntry.less)
	fenced = slices.Compact(fenced)
	bases := prev.baselines()
	for _, p := range taken {
		bases[p.key()] = p
	}
	procs := make([]baselineProcess, 0, len(bases))
	for _, p := range bases {
		procs = append(procs, p)
	}
	sort.Slice(procs, func(i, j int) bool {
		a, b := procs[i], procs[j]
		return a.Scope+"\x00"+a.MetaHash+"\x00"+a.PID < b.Scope+"\x00"+b.MetaHash+"\x00"+b.PID
	})
	if fenced == nil {
		fenced = []fenceEntry{}
	}
	data, err := json.MarshalIndent(resetRecord{Schema: resetSchema, ResetAt: at.UTC(), Fenced: fenced, Baselines: procs}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeResetFile(path, append(data, '\n'), 0o644)
}
