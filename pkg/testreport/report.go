package testreport

import "sort"

// Report is the forge.test.report/v1 document: what `forge test report
// --json` prints and what a commit's record shows. It is the summary of one
// or more runs on one tree: their totals, the merged coverage figures, the
// failures and the flake signals.
type Report struct {
	Head
	Project string   `json:"project,omitempty"`
	Commit  string   `json:"commit,omitempty"`
	Tree    string   `json:"tree,omitempty"`
	Dirty   bool     `json:"dirty"`
	Base    *BaseRef `json:"base,omitempty"`
	// Runs are the lane runs the report covers.
	Runs   []RunRef `json:"runs"`
	Totals Totals   `json:"totals"`
	// Coverage is absent when no run carried coverage.
	Coverage *ReportCoverage `json:"coverage,omitempty"`
	// Packages is per-package coverage, worst first.
	Packages []PackageCoverage `json:"packages,omitempty"`
	// Failures are the failed leaf cases, and the packages that failed with
	// no case to show for it.
	Failures []Failure `json:"failures,omitempty"`
	// Flakes are the flake signals the runs themselves carry (retry passes).
	Flakes []FlakeSignal `json:"flakes,omitempty"`
	// Upload is the result of sending the report to the control plane, when
	// one was attempted.
	Upload *Upload `json:"upload,omitempty"`
	// Suggested are commands worth running next.
	Suggested []string `json:"suggested,omitempty"`
}

// BaseRef is the commit a report is compared with.
type BaseRef struct {
	SHA string `json:"sha"`
	// Report says whether the base has a report. Without one, no delta is
	// computed ("no report for base"), never one against an older commit.
	Report bool `json:"report"`
}

// RunRef names one lane run in a report.
type RunRef struct {
	ID   string `json:"id"`
	Lane Lane   `json:"lane"`
	// Source is where the run happened: "local", "ci", "preview".
	Source   string `json:"source,omitempty"`
	URL      string `json:"url,omitempty"`
	ExitCode int    `json:"exit_code"`
}

// ReportCoverage is a report's coverage figures, in percent.
type ReportCoverage struct {
	// Granularity is "statement" when every counted file is, else "line"
	// (some file was line-granular), else empty when nothing counted.
	Granularity Granularity `json:"granularity,omitempty"`
	// Lanes is each lane's percentage over every counted language.
	Lanes map[Lane]float64 `json:"lanes"`
	// Combined counts a statement covered if any lane covered it.
	Combined float64 `json:"combined"`
	// CombinedLabel says what Combined adds up when more than one language
	// counted: "weighted by statements across go, ts".
	CombinedLabel string `json:"combined_label,omitempty"`
	// OnlyE2E is the share of statements that only an e2e lane reached.
	OnlyE2E float64 `json:"only_e2e"`
	// Delta is Combined minus the base report's Combined; null with
	// DeltaReason when there is no base report.
	Delta       *float64 `json:"delta"`
	DeltaReason string   `json:"delta_reason,omitempty"`
	// Diff is coverage of the executable lines the diff changed.
	Diff *DiffSummary `json:"diff,omitempty"`
	// Languages breaks the figures down per language, which are never mixed
	// silently.
	Languages []LangCoverage `json:"languages,omitempty"`
	Excluded  ExcludedTotals `json:"excluded"`
}

// LangCoverage is one language's figures.
type LangCoverage struct {
	Lang        Lang             `json:"lang"`
	Granularity Granularity      `json:"granularity"`
	Statements  int              `json:"statements"`
	Lanes       map[Lane]float64 `json:"lanes"`
	Combined    float64          `json:"combined"`
	OnlyE2E     float64          `json:"only_e2e"`
}

// ExcludedTotals is what did not count toward the percentages.
type ExcludedTotals struct {
	GeneratedStatements int `json:"generated_statements"`
	FilteredStatements  int `json:"filtered_statements,omitempty"`
}

// PackageCoverage is one package's figures in a report.
type PackageCoverage struct {
	Path       string           `json:"path"`
	Lang       Lang             `json:"lang"`
	Statements int              `json:"statements"`
	Combined   float64          `json:"combined"`
	Lanes      map[Lane]float64 `json:"lanes"`
}

// Failure is one failure in a report.
type Failure struct {
	Test      TestID `json:"test"`
	Lane      Lane   `json:"lane,omitempty"`
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
	Output    string `json:"output,omitempty"`
	Signature string `json:"signature,omitempty"`
}

// FlakeSignal is one flake signal in a report.
type FlakeSignal struct {
	Test    TestID  `json:"test"`
	Verdict Verdict `json:"verdict"`
	Signal  Signal  `json:"signal"`
}

// Upload is the result of an upload.
type Upload struct {
	// Status is "ok", "skipped" or "failed".
	Status string `json:"status"`
	URL    string `json:"url,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ReportInput is what [NewReport] summarises.
type ReportInput struct {
	Project string
	Commit  string
	Tree    string
	Dirty   bool
	Runs    []RunRef
	// Results are the runs' parsed results, one per lane run.
	Results []*Results
	// Coverage is the merged coverage of the runs, or nil.
	Coverage *Coverage
	// BaseSHA names the base commit; Base is its merged coverage, nil when
	// the base has no report.
	BaseSHA string
	Base    *Coverage
	// Changed are the diff's changed line ranges; nil uses Coverage.Changed.
	Changed []ChangedFile
}

// NewReport summarises runs into a report. The head is set from the results
// alone: exit code 1 when a leaf case or a package failed, 2 when there are no
// results at all (nothing was learned), else 0. A command that applies more
// rules (verify-test-run's zero-evidence rule, an upload that was required)
// stamps its own head over it.
func NewReport(in ReportInput) *Report {
	rep := &Report{
		Head:    Head{Schema: SchemaReport},
		Project: in.Project, Commit: in.Commit, Tree: in.Tree, Dirty: in.Dirty,
		Runs: append([]RunRef(nil), in.Runs...),
	}
	if rep.Runs == nil {
		rep.Runs = []RunRef{}
	}
	if in.BaseSHA != "" {
		rep.Base = &BaseRef{SHA: in.BaseSHA, Report: in.Base != nil}
	}
	events := 0
	for _, res := range in.Results {
		if res == nil {
			continue
		}
		events += res.Stats.Events
		rep.Totals = rep.Totals.Add(res.Totals)
		for _, c := range res.Leaves() {
			if c.Status == StatusFail {
				rep.Failures = append(rep.Failures, Failure{Test: c.ID, Lane: res.Lane, File: c.File, Line: c.Line, Output: c.Output, Signature: c.Signature})
			}
			if c.RetryPassed {
				rep.Flakes = append(rep.Flakes, FlakeSignal{Test: c.ID, Verdict: VerdictConfirmed, Signal: SignalRetryPass})
			}
		}
		for _, p := range res.Packages {
			if (p.Status == StatusFail || p.BuildFailed) && p.Failed == 0 {
				rep.Failures = append(rep.Failures, Failure{Test: p.ID(), Lane: res.Lane, Output: p.Output, Signature: p.Signature})
			}
		}
	}
	sort.SliceStable(rep.Failures, func(i, j int) bool { return lessID(rep.Failures[i].Test, rep.Failures[j].Test) })
	sort.SliceStable(rep.Flakes, func(i, j int) bool { return lessID(rep.Flakes[i].Test, rep.Flakes[j].Test) })

	if in.Coverage != nil {
		changed := in.Changed
		if changed == nil {
			changed = in.Coverage.Changed
		}
		rep.Coverage, rep.Packages = reportCoverage(in.Coverage, in.Base, changed)
	}

	switch {
	case rep.Totals.Failed > 0 || rep.Totals.PackagesFailed > 0:
		rep.ExitCode = 1
	case events == 0:
		rep.ExitCode = 2
		rep.Error = "no test results: the runs recorded no test events"
	}
	rep.OK = rep.ExitCode == 0
	return rep
}

func reportCoverage(c, base *Coverage, changed []ChangedFile) (*ReportCoverage, []PackageCoverage) {
	s := c.Summary()
	rc := &ReportCoverage{
		Lanes:         map[Lane]float64{},
		Combined:      s.Combined.Pct,
		CombinedLabel: s.CombinedLabel,
		OnlyE2E:       s.OnlyE2E.Pct,
		Excluded: ExcludedTotals{
			GeneratedStatements: s.Generated.Statements,
			FilteredStatements:  s.Filtered.Statements,
		},
	}
	for lane, sum := range s.Lanes {
		rc.Lanes[lane] = sum.Pct
	}
	for _, l := range s.Languages {
		lc := LangCoverage{Lang: l.Lang, Granularity: l.Granularity, Statements: l.Combined.Statements,
			Lanes: map[Lane]float64{}, Combined: l.Combined.Pct, OnlyE2E: l.OnlyE2E.Pct}
		for lane, sum := range l.Lanes {
			lc.Lanes[lane] = sum.Pct
		}
		rc.Languages = append(rc.Languages, lc)
		switch {
		case l.Granularity == GranularityLine:
			rc.Granularity = GranularityLine
		case rc.Granularity == "":
			rc.Granularity = l.Granularity
		}
	}
	d := Delta(c, base)
	if d.Available {
		v := d.Value
		rc.Delta = &v
	} else {
		rc.DeltaReason = d.Reason
	}
	if changed != nil {
		diff := c.Diff(changed)
		rc.Diff = &diff
	}
	var pkgs []PackageCoverage
	for _, r := range c.Rollup(ByPackage) {
		pc := PackageCoverage{Path: r.Path, Lang: r.Lang, Statements: r.Combined.Statements, Combined: r.Combined.Pct, Lanes: map[Lane]float64{}}
		for lane, sum := range r.Lanes {
			pc.Lanes[lane] = sum.Pct
		}
		pkgs = append(pkgs, pc)
	}
	sort.SliceStable(pkgs, func(i, j int) bool {
		if pkgs[i].Combined != pkgs[j].Combined {
			return pkgs[i].Combined < pkgs[j].Combined
		}
		return pkgs[i].Path < pkgs[j].Path
	})
	return rc, pkgs
}
