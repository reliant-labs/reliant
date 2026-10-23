package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Two records beside the policy say what maintenance has actually done.
//
//   - last-full-gc.json is written by every APPLIED full pass (`forge storage
//     gc --apply` and the scheduled job, which runs that command). It records
//     whether the pass succeeded, and which layers failed. This is the only
//     evidence that registry retention, the layer that reclaims the most, is
//     running at all.
//   - last-auto-gc.json is the opportunistic pass's own attempt record. It
//     rate-limits that pass to once per interval, and that is all it is for.
//
// They used to be one stamp, last-gc.json, which the opportunistic pass
// advanced even when it failed and even though it never runs the registry
// layer. `forge doctor` then reported "GC scheduled, last ran 2h ago" on a
// machine where registry retention had never once succeeded.

// GCResult is the outcome of one maintenance pass.
type GCResult struct {
	At time.Time `json:"at"`
	OK bool      `json:"ok"`
	// FailedLayers names each layer that failed: "logs", "temp sweep",
	// "source cache", "builder <name>", "registry <container>", "docker".
	FailedLayers []string `json:"failed_layers,omitempty"`
	// Error is the combined error text, for a human reading the record.
	Error string `json:"error,omitempty"`
	// CutOffLayers names each layer that ran out of its time slice (or the
	// pass out of its budget) after doing what it could. That is not a
	// failure: the layer reclaimed what it reached and resumes next pass. A
	// pass whose only problems are cut-offs is OK.
	CutOffLayers []string `json:"cut_off_layers,omitempty"`
	// BackedOffLayers names each layer that stopped, or never started,
	// because the filesystem reported distress (EMFILE, ENFILE, ENOTCONN,
	// EIO). Like a cut-off it retained the rest for the next pass and is not
	// a failure, but it says the MACHINE is unwell, so it is reported apart.
	BackedOffLayers []string `json:"backed_off_layers,omitempty"`
	// OverBudget describes each cache the pass could not bring under its size
	// budget because more than the budget is younger than the trim's safety
	// floor (BudgetUnreachableError). Not a failure — the pass removed what it
	// safely could — but a budget that is not being held, so it is reported
	// on every pass until the fill slows.
	OverBudget []string `json:"over_budget,omitempty"`
	// GoCaches is what each Go-format cache trim in the pass measured and
	// did (Runner.OnGoCacheTrim). Set by the caller that ran the pass.
	GoCaches []GoCacheTrim `json:"go_caches,omitempty"`
}

// CutOffError marks a layer that stopped at its deadline having made whatever
// progress it could. NewGCResult records it as a cut-off, not a failure.
type CutOffError struct{ Err error }

func (e *CutOffError) Error() string { return "cut off: " + e.Err.Error() }
func (e *CutOffError) Unwrap() error { return e.Err }

// onlyContextErrors reports whether every leaf of err is a deadline or
// cancellation, i.e. the layer ended because it ran out of time and for no
// other reason. A real error joined with a deadline is still a failure.
func onlyContextErrors(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, inner := range joined.Unwrap() {
			if !onlyContextErrors(inner) {
				return false
			}
		}
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// LayerError attributes a failure to the maintenance layer it came from, so a
// result can name the layers that failed without parsing error text.
type LayerError struct {
	Layer string
	Err   error
}

func (e *LayerError) Error() string { return e.Layer + ": " + e.Err.Error() }
func (e *LayerError) Unwrap() error { return e.Err }

func layerErr(layer string, err error) error {
	if err == nil {
		return nil
	}
	return &LayerError{Layer: layer, Err: err}
}

// NewGCResult summarises a pass's error into a record.
func NewGCResult(at time.Time, err error) GCResult {
	r := GCResult{At: at, OK: err == nil}
	if err == nil {
		return r
	}
	seen := map[string]bool{}
	cut := map[string]bool{}
	backed := map[string]bool{}
	addOnce := func(set map[string]bool, list *[]string, name string) {
		if !set[name] {
			set[name] = true
			*list = append(*list, name)
		}
	}
	failed := false
	var walk func(error)
	// classify records how one layer ended. A layer can end several ways at
	// once (cut off AND over an unreachable budget), so a joined error is
	// classified part by part.
	classify := func(layer *LayerError) {
		if joined, ok := layer.Err.(interface{ Unwrap() []error }); ok {
			for _, inner := range joined.Unwrap() {
				walk(&LayerError{Layer: layer.Layer, Err: inner})
			}
			return
		}
		var ub *BudgetUnreachableError
		var bo *BackedOffError
		var co *CutOffError
		switch {
		case errors.As(layer.Err, &ub):
			r.OverBudget = append(r.OverBudget, ub.Error())
		case errors.As(layer.Err, &bo):
			addOnce(backed, &r.BackedOffLayers, layer.Layer)
		case errors.As(layer.Err, &co):
			addOnce(cut, &r.CutOffLayers, layer.Layer)
		default:
			failed = true
			addOnce(seen, &r.FailedLayers, layer.Layer)
		}
	}
	walk = func(e error) {
		if e == nil {
			return
		}
		// A joined error is descended FIRST: errors.As on it would stop at
		// the first layer and never name the rest.
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, inner := range joined.Unwrap() {
				walk(inner)
			}
			return
		}
		if layer := (*LayerError)(nil); errors.As(e, &layer) && layer != nil {
			classify(layer)
			return
		}
		if ub := (*BudgetUnreachableError)(nil); errors.As(e, &ub) {
			r.OverBudget = append(r.OverBudget, ub.Error())
			return
		}
		if bo := (*BackedOffError)(nil); errors.As(e, &bo) {
			if !backed["pass"] {
				backed["pass"] = true
				r.BackedOffLayers = append(r.BackedOffLayers, "pass")
			}
			return
		}
		failed = true
		if onlyContextErrors(e) { // the pass itself ran out of budget between layers
			failed = false
			if !cut["pass budget"] {
				cut["pass budget"] = true
				r.CutOffLayers = append(r.CutOffLayers, "pass budget")
			}
			return
		}
		r.Error += e.Error() + "\n"
	}
	walk(err)
	sort.Strings(r.FailedLayers)
	sort.Strings(r.CutOffLayers)
	sort.Strings(r.BackedOffLayers)
	r.OK = !failed
	if failed {
		r.Error = strings.TrimSpace(err.Error())
	} else {
		r.Error = ""
	}
	return r
}

// Summary is one line naming what failed, for `env up` and `doctor`.
func (r GCResult) Summary() string {
	s := r.outcome()
	if len(r.OverBudget) > 0 {
		s += fmt.Sprintf("; %d cache(s) over a budget no trim can reach — filling faster than entries age past the safety floor", len(r.OverBudget))
	}
	return s
}

func (r GCResult) outcome() string {
	if r.OK {
		if len(r.BackedOffLayers) > 0 {
			return "backed off on filesystem distress, the rest retained for the next pass (" + strings.Join(r.BackedOffLayers, ", ") + ")"
		}
		if len(r.CutOffLayers) > 0 {
			return "succeeded (cut off, resumes next pass: " + strings.Join(r.CutOffLayers, ", ") + ")"
		}
		return "succeeded"
	}
	if len(r.FailedLayers) > 0 {
		return "failed in " + strings.Join(r.FailedLayers, ", ")
	}
	return "failed"
}

// FullGCRecordPath is the full pass's record, beside the policy.
func FullGCRecordPath(policyPath string) string {
	return filepath.Join(filepath.Dir(policyPath), "last-full-gc.json")
}

// AutoGCRecordPath is the opportunistic pass's attempt record.
func AutoGCRecordPath(policyPath string) string {
	return filepath.Join(filepath.Dir(policyPath), "last-auto-gc.json")
}

// RecordFullGC records an applied full pass.
func RecordFullGC(policyPath string, r GCResult) error {
	return writeRecord(policyPath, FullGCRecordPath(policyPath), r)
}

// RecordAutoGC records an opportunistic pass's attempt.
func RecordAutoGC(policyPath string, r GCResult) error {
	return writeRecord(policyPath, AutoGCRecordPath(policyPath), r)
}

// LastFullGC reads the full pass's record. ok is false when there is none, or
// it cannot be read: "never ran" and "cannot tell" lead to the same advice.
func LastFullGC(policyPath string) (GCResult, bool) {
	return readRecord(FullGCRecordPath(policyPath))
}

// LastAutoGC reads the opportunistic pass's attempt record.
func LastAutoGC(policyPath string) (GCResult, bool) {
	return readRecord(AutoGCRecordPath(policyPath))
}

// GoCacheGCRecordPath is the Go-cache pass's record (`forge storage gocache
// --apply`): its outcome and each cache's GoCacheTrim. It is that pass's own
// record, apart from the opportunistic one, so a scheduler running both on
// different cadences can rate-limit each by its own last run.
func GoCacheGCRecordPath(policyPath string) string {
	return filepath.Join(filepath.Dir(policyPath), "last-gocache-gc.json")
}

// RecordGoCacheGC records an applied Go-cache pass.
func RecordGoCacheGC(policyPath string, r GCResult) error {
	return writeRecord(policyPath, GoCacheGCRecordPath(policyPath), r)
}

// LastGoCacheGC reads the Go-cache pass's record.
func LastGoCacheGC(policyPath string) (GCResult, bool) {
	return readRecord(GoCacheGCRecordPath(policyPath))
}

// FullGCStaleAfter is when a successful full pass stops being evidence that
// retention is running: the schedule is daily, so two days means it is not
// firing.
const FullGCStaleAfter = 48 * time.Hour

// FullGCProblem reports, in one sentence, why registry retention is not known
// to be working, or "" if it is. It applies only when registries are
// registered: a machine with none has nothing that only the full pass can
// reclaim. Doctor and `forge env up` both read it, so they cannot disagree.
func FullGCProblem(p Policy, last GCResult, ok bool, now time.Time) string {
	if len(p.Registries) == 0 {
		return ""
	}
	switch {
	case !ok:
		return fmt.Sprintf("%d local registr(ies) registered, but no full storage GC has ever completed", len(p.Registries))
	case !last.OK:
		return fmt.Sprintf("the last full storage GC (%s ago) %s", ageText(now.Sub(last.At)), last.Summary())
	case now.Sub(last.At) > FullGCStaleAfter:
		return fmt.Sprintf("the last full storage GC was %s ago; the daily pass is not running", ageText(now.Sub(last.At)))
	}
	return ""
}

func ageText(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d >= 48*time.Hour {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	return fmt.Sprintf("%dh", int(d/time.Hour))
}

func writeRecord(policyPath, path string, r GCResult) error {
	return writeStateFile(policyPath, path, r)
}

// writeStateFile atomically replaces path, a maintenance state file kept beside
// the policy, with v as JSON. Refused under test for the machine policy, like
// the policy itself.
func writeStateFile(policyPath, path string, v any) error {
	if err := guardMachinePolicy(policyPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".gc-record-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func readRecord(path string) (GCResult, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return GCResult{}, false
	}
	var r GCResult
	if json.Unmarshal(b, &r) != nil || r.At.IsZero() {
		return GCResult{}, false
	}
	return r, true
}

// RealFailure returns err only when the pass really failed. A pass whose every
// layer finished or was cut off after making progress is a success, so the
// command that ran it (and the scheduler watching its exit status) sees nil.
func RealFailure(err error) error {
	if err == nil || NewGCResult(time.Time{}, err).OK {
		return nil
	}
	return err
}
