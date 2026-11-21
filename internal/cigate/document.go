package cigate

import (
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/obsout"
)

// DocumentSchema names the gate's machine-readable result (`forge ci gate
// --json`). Field names and status values under it are stable; a breaking
// change is a new version. New fields may be added.
const DocumentSchema = "forge.ci.gate/v1"

// Document is the gate's result as a check run consumes it: the verdict, one
// row per check, and each failed check's findings for annotations. Rows are
// in plan order and findings sorted, so one run's document is deterministic
// up to its timings.
type Document struct {
	// Head: schema, ok, exit_code (exactly the process status) and error.
	obsout.Head
	// Verdict is pass, fail, or error (the gate could not run: a bad flag,
	// an unreadable forge.yaml; Checks is then empty).
	Verdict string `json:"verdict"`
	// Lane is the lane argument (all, checks, test, integration, or a
	// project's own).
	Lane string `json:"lane"`
	// Tier is the --tier (or --event's tier) the gate ran as; absent without.
	Tier string `json:"tier,omitempty"`
	// Commit is the short commit judged; Dirty says the tree had uncommitted
	// changes on top of it.
	Commit string `json:"head,omitempty"`
	Dirty  bool   `json:"dirty"`
	// WallMS is the gate's elapsed time.
	WallMS int64           `json:"wall_ms"`
	Counts DocumentCounts  `json:"counts"`
	Checks []DocumentCheck `json:"checks"`
}

// Verdicts of a Document.
const (
	VerdictPass  = "pass"
	VerdictFail  = "fail"
	VerdictError = "error"
)

// DocumentCounts tallies the checks by status. Cached counts the passes the
// verdict cache answered, and is included in Pass.
type DocumentCounts struct {
	Pass     int `json:"pass"`
	Cached   int `json:"cached"`
	Fail     int `json:"fail"`
	Skip     int `json:"skip"`
	Waived   int `json:"waived"`
	Deferred int `json:"deferred"`
	NA       int `json:"na"`
}

// Statuses of a DocumentCheck.
const (
	// StatusPass ran and succeeded, or the verdict cache answered (cached).
	StatusPass = "pass"
	// StatusFail ran and failed. Blocking.
	StatusFail = "fail"
	// StatusSkip could not run here (a missing tool or prerequisite).
	// Blocking: a skip is not a pass.
	StatusSkip = "skip"
	// StatusWaived was not run because the caller said so (CI_SKIP, --skip).
	StatusWaived = "waived"
	// StatusDeferred belongs to a lane the tier leaves to another tier.
	StatusDeferred = "deferred"
	// StatusNA has nothing to look at in this project (no migrations, no
	// integration tests).
	StatusNA = "na"
)

// DocumentCheck is one row.
type DocumentCheck struct {
	Name   string `json:"name"`
	Lane   string `json:"lane"`
	Status string `json:"status"`
	// Blocking is true when this row alone makes the gate fail (fail, or a
	// skip nobody waived).
	Blocking bool `json:"blocking"`
	// DurationMS is how long the command ran; 0 when nothing ran.
	DurationMS int64 `json:"duration_ms"`
	// QueuedMS is how long the check, its dependencies done, waited for a
	// heavy slot or the gate's job cap.
	QueuedMS int64 `json:"queued_ms"`
	// Cached is a pass the verdict cache answered; CachedAt the short commit
	// it was recorded at.
	Cached   bool   `json:"cached"`
	CachedAt string `json:"cached_at,omitempty"`
	// Note is the table's note: why it skipped, how it was narrowed.
	Note string `json:"note,omitempty"`
	// Log is the check's output file, project-relative when under the root.
	Log string `json:"log,omitempty"`
	// Tail is the end of a failed check's output.
	Tail string `json:"tail,omitempty"`
	// Findings are a failed check's located problems; always a list.
	Findings []Finding `json:"findings"`
	// FindingsOmitted counts findings past MaxFindings.
	FindingsOmitted int `json:"findings_omitted,omitempty"`
}

// DocumentInput is what NewDocument needs beyond the report.
type DocumentInput struct {
	// Tier is the tier's name; "" when the gate ran without one.
	Tier string
	// Err is the error the gate command returns: nil, its gate failure, or
	// the reason it could not run. It sets ok, exit_code and error.
	Err error
	// Ran is false when the gate never got as far as running checks; the
	// verdict is then error.
	Ran bool
	// Root, Module and Dirs place findings: the project root, the Go module
	// path, and each check's directory by name.
	Root   string
	Module string
	Dirs   map[string]string
	// ReadLog reads a check's log; nil reads it from disk.
	ReadLog func(path string) ([]byte, error)
}

// NewDocument builds the document of one gate run.
func NewDocument(rep Report, in DocumentInput) Document {
	doc := Document{Lane: rep.Lane, Tier: in.Tier, Commit: rep.Head, Dirty: rep.Dirty, WallMS: rep.Wall.Milliseconds(), Checks: []DocumentCheck{}}
	doc.Stamp(DocumentSchema, in.Err)
	switch {
	case !in.Ran:
		doc.Verdict = VerdictError
	case rep.Failed():
		doc.Verdict = VerdictFail
	default:
		doc.Verdict = VerdictPass
	}
	for _, res := range rep.Results {
		row := DocumentCheck{
			Name: res.Name, Lane: string(res.Lane), Status: res.DocumentStatus(),
			Blocking:   res.Status == Fail || (res.Status == Skip && !res.Allowed),
			DurationMS: res.Duration.Milliseconds(), QueuedMS: res.Queued.Milliseconds(),
			Cached: res.Cached != "", CachedAt: res.Cached,
			Note: res.Note, Log: res.Log, Findings: []Finding{},
		}
		if res.Status == Fail {
			row.Tail = res.Tail
			row.Findings, row.FindingsOmitted = in.findings(res)
		}
		doc.Counts.add(row)
		doc.Checks = append(doc.Checks, row)
	}
	return doc
}

// DocumentStatus is the row's status in the document.
func (res Result) DocumentStatus() string {
	switch res.Status {
	case Pass:
		return StatusPass
	case Fail:
		return StatusFail
	case Skip:
		if res.Allowed {
			return StatusWaived
		}
		return StatusSkip
	case Deferred:
		return StatusDeferred
	default:
		return StatusNA
	}
}

func (c *DocumentCounts) add(row DocumentCheck) {
	switch row.Status {
	case StatusPass:
		c.Pass++
		if row.Cached {
			c.Cached++
		}
	case StatusFail:
		c.Fail++
	case StatusSkip:
		c.Skip++
	case StatusWaived:
		c.Waived++
	case StatusDeferred:
		c.Deferred++
	default:
		c.NA++
	}
}

// findings reads a failed check's whole log (its tail when there is none) for
// located problems.
func (in DocumentInput) findings(res Result) ([]Finding, int) {
	output := res.Tail
	if res.Log != "" {
		p := res.Log
		if !filepath.IsAbs(p) && in.Root != "" {
			p = filepath.Join(in.Root, filepath.FromSlash(p))
		}
		read := in.ReadLog
		if read == nil {
			read = os.ReadFile
		}
		if b, err := read(p); err == nil {
			output = string(b)
		}
	}
	f, omitted := ExtractFindings(output, FindingsInput{Root: in.Root, Dir: in.Dirs[res.Name], Module: in.Module})
	if f == nil {
		f = []Finding{}
	}
	return f, omitted
}
