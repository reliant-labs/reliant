package testreport

import (
	"fmt"
	"sort"
	"strings"
)

// Status is the verdict of an [Analysis]. Four states, because collapsing
// them loses the distinction the whole exercise is about: a run that proved
// its packages ran, a run that proved they did not, a run there was nothing
// to judge in, and a run forge could not read.
type Status string

// The four verdicts.
const (
	// StatusPass — every package that had tests ran enough of them for
	// its result to mean something.
	StatusPass Status = "pass"
	// StatusFail — at least one package skipped its way to a meaningless
	// pass, or the run contains failures.
	StatusFail Status = "fail"
	// StatusNotApplicable — the run contains no tests to judge (a module
	// of packages with no test files). Nothing is missing.
	StatusNotApplicable Status = "not-applicable"
	// StatusUndetermined — forge could not obtain the facts. NOT a pass:
	// unreadable input and a truncated stream both land here, and both
	// must be louder than silence rather than quieter.
	StatusUndetermined Status = "undetermined"
)

// Kind names a finding's rule. See the package doc for why there are exactly
// two and why neither is "this package skipped something".
type Kind string

// The finding kinds, strongest first.
const (
	// KindZeroEvidence — every test in the package skipped.
	KindZeroEvidence Kind = "zero-evidence"
	// KindMassSkip — the package skipped at least the policy's share.
	KindMassSkip Kind = "mass-skip"
	// KindFailed — the package failed or did not build. Reported so that
	// piping a run into forge cannot LAUNDER a red suite green: in a
	// shell without `set -o pipefail`, `go test -json ./... | forge ...`
	// reports only the last command's exit status, so a checker that
	// ignored failures would swallow them.
	KindFailed Kind = "failed"
)

// Finding is one package's verdict.
type Finding struct {
	Package string
	Kind    Kind
	Tests   int
	Skipped int
	Ratio   float64
	// Examples names a few of the skipped tests, shown only when no skip
	// reason could be recovered.
	Examples []string
	// Reasons is the dominant t.Skip message(s) behind the skips, most
	// common first. This is the actionable half of the finding: "124
	// skipped" is a number, `118x "DATABASE_URL not set"` is an instruction.
	Reasons []ReasonCount
	// Detail carries the failure text for [KindFailed].
	Detail string
}

// Exemption declares that a package is EXPECTED to skip heavily, so its
// legitimate skips stop being noise.
//
// Reason is required and unused by the logic on purpose: it exists to be read
// in a code review. An exemption nobody had to justify is an exemption nobody
// will revisit, which is how a suppression outlives the condition it was
// written for. Same contract as `forge project disown <path> --reason`.
type Exemption struct {
	// Package matches the Go import path. Accepted spellings: the full
	// import path, a trailing path fragment ("internal/threads" matches
	// "github.com/acme/app/internal/threads"), or either with a "/..."
	// suffix for the subtree.
	Package string `yaml:"package"`
	// Reason is why this package legitimately skips. Required.
	Reason string `yaml:"reason"`
	// MaxSkipRatio is the share this package may skip, in [0,1]. Zero
	// means "fully exempt" (1.0) — a declaration with no number is a
	// blanket one, which is the common case for an integration-only
	// package.
	MaxSkipRatio float64 `yaml:"max_skip_ratio,omitempty"`
}

// allowance returns the effective ratio ceiling for an exemption.
func (e Exemption) allowance() float64 {
	if e.MaxSkipRatio <= 0 {
		return 1
	}
	return e.MaxSkipRatio
}

// Policy is the rule set [Analyze] applies.
type Policy struct {
	// MaxSkipRatio is the share of a package's tests that may skip before
	// the package is reported. See [DefaultPolicy] for the default and
	// the measurement behind it.
	MaxSkipRatio float64
	// MinTests is the sample-size floor for [KindMassSkip]. Below it a
	// ratio is arithmetic, not evidence: 1-of-2 is 50% and means nothing.
	// [KindZeroEvidence] deliberately ignores this floor — "none of them
	// ran" is unambiguous at any size.
	MinTests int
	// Allow holds the declared exemptions.
	Allow []Exemption
}

// DefaultPolicy is the shipped rule set.
//
// MaxSkipRatio 0.5 is a line anyone can restate — "more than half of this
// package's tests did not run" — and it was checked against a real
// distribution rather than picked for roundness. Across the 112 tested
// packages of the reference project, run in the environment that triggers the
// mass skipping, the ratios above 0.5 are 1.00, 0.95, 0.80, 0.60 and 0.56;
// the next package down is 0.20. There is a wide empty band around the line,
// so nudging it does not change who gets reported — which is the property a
// threshold needs if it is not going to be argued about forever.
//
// MinTests 5 removes the tiny-sample noise (one 2-test package sitting at
// exactly 0.50) without touching any real finding.
func DefaultPolicy() Policy {
	return Policy{MaxSkipRatio: 0.5, MinTests: 5}
}

// normalise fills zero fields with the defaults so a partially-specified
// Policy (one key set in forge.yaml) behaves as the user expects.
func (p Policy) normalise() Policy {
	d := DefaultPolicy()
	if p.MaxSkipRatio <= 0 {
		p.MaxSkipRatio = d.MaxSkipRatio
	}
	if p.MinTests <= 0 {
		p.MinTests = d.MinTests
	}
	return p
}

// Validate reports the exemptions that are not usable, by index. It is
// separate from [Analyze] so a bad declaration surfaces as a configuration
// error rather than as a silently-ignored line — a suppression that does not
// parse must never read as a suppression that applied.
func (p Policy) Validate() []error {
	var errs []error
	for i, e := range p.Allow {
		where := fmt.Sprintf("ci.test_skips.allow[%d]", i)
		pkg := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(e.Package), "./"), "/")
		switch {
		case pkg == "":
			errs = append(errs, fmt.Errorf("%s: `package` is empty", where))
		case pkg == "..." || pkg == "all":
			// A module-wide exemption is not a narrow declaration that
			// happens to be broad — it switches the check off while
			// leaving it in the pipeline, reporting a pass forever. If
			// that is the intent it belongs in the pipeline, visibly.
			errs = append(errs, fmt.Errorf("%s: `package: %s` exempts the whole module, which turns this check into a permanent pass — remove the check from CI instead, or name the packages", where, e.Package))
		}
		if strings.TrimSpace(e.Reason) == "" {
			errs = append(errs, fmt.Errorf("%s (package %q): `reason` is required — an exemption nobody had to justify is one nobody will revisit", where, e.Package))
		}
		if e.MaxSkipRatio < 0 || e.MaxSkipRatio > 1 {
			errs = append(errs, fmt.Errorf("%s (package %q): `max_skip_ratio` must be between 0 and 1, got %v", where, e.Package, e.MaxSkipRatio))
		}
	}
	return errs
}

// ReasonCount is one t.Skip message and how many tests gave it.
type ReasonCount struct {
	Reason string
	Count  int
}

// Suppressed records a finding an exemption absorbed, so the report can say
// how much silence was bought and by which declaration.
type Suppressed struct {
	Package   string
	Kind      Kind
	Tests     int
	Skipped   int
	Ratio     float64
	Exemption Exemption
}

// Unknown is one reason forge could not obtain a fact it needed.
type Unknown struct {
	Package string // "" when the whole run is the problem
	Reason  string
}

// Totals is the run-wide roll-up.
type Totals struct {
	Packages       int // packages that had at least one leaf test
	NoTestFiles    int // packages cmd/go skipped: no test files
	NoTestsRan     int // packages that compiled and ran zero tests (e.g. -run filtered)
	Tests          int
	Passed         int
	Failed         int
	Skipped        int
	SkipRatio      float64
	SuiteFailed    bool
	FailedPackages int
}

// Analysis is the finished verdict over a [Run].
type Analysis struct {
	Policy     Policy
	Totals     Totals
	Findings   []Finding
	Suppressed []Suppressed
	// Stale names exemptions that matched packages in this run and
	// suppressed nothing. Reported so a declaration cannot outlive its
	// reason and quietly become an unfalsifiable rule.
	Stale []Exemption
	// Undetermined holds every fact forge could not obtain.
	Undetermined []Unknown
	Malformed    int
}

// Status is the verdict.
//
// Order matters. A run forge could not read at all is UNDETERMINED before
// anything else, because every other answer would be a claim about content
// nobody saw. Concrete findings then outrank a partial-fact warning: they are
// defects that exist regardless of what the unreadable remainder holds.
func (a Analysis) Status() Status {
	switch {
	case a.Totals.Packages == 0 && a.Totals.NoTestFiles == 0 && len(a.Undetermined) > 0:
		return StatusUndetermined
	case len(a.Findings) > 0 || a.Totals.SuiteFailed:
		return StatusFail
	case len(a.Undetermined) > 0:
		return StatusUndetermined
	case a.Totals.Packages == 0:
		return StatusNotApplicable
	default:
		return StatusPass
	}
}

// Analyze applies a policy to a parsed run.
func Analyze(run *Run, policy Policy) Analysis {
	p := policy.normalise()
	a := Analysis{Policy: p, Malformed: run.Malformed}

	if run.Events == 0 {
		reason := "the input carried no `go test -json` events"
		if run.Malformed > 0 {
			reason = fmt.Sprintf("none of the %d input line(s) were `go test -json` events", run.Malformed)
		}
		a.Undetermined = append(a.Undetermined, Unknown{Reason: reason})
		return a
	}

	// matched tracks, per exemption index, whether it matched any package
	// at all and whether it actually suppressed a finding. An exemption
	// that matched nothing is NOT reported: a scoped run (`go test
	// ./internal/threads/`) legitimately fails to mention every other
	// package, and reporting those would make the common case noisy —
	// which is the failure mode that gets checks switched off.
	matchedAny := make([]bool, len(p.Allow))
	suppressedAny := make([]bool, len(p.Allow))

	// Accumulate into a local and assign once at the end. Incrementing
	// through `a.Totals.X++` never assigns the FIELD itself, which reads —
	// to a human skimming, and to forge's own deadcodeguard, which flags
	// exactly this shape — as a struct nothing ever fills.
	totals := Totals{}

	for _, pkg := range run.Packages {
		switch {
		case pkg.NoTestFiles:
			totals.NoTestFiles++
			continue
		case pkg.BuildFailed:
			totals.FailedPackages++
			totals.SuiteFailed = true
			a.Findings = append(a.Findings, Finding{
				Package: pkg.Path,
				Kind:    KindFailed,
				Detail:  "the package did not build, so none of its tests ran",
			})
			continue
		case pkg.Incomplete:
			// Partial tallies are not a verdict. Say so instead of
			// reporting the fraction that happened to arrive.
			a.Undetermined = append(a.Undetermined, Unknown{
				Package: pkg.Path,
				Reason:  fmt.Sprintf("the stream ends before this package finished (%d test event(s) seen, no package result) — its totals are partial", pkg.Tests),
			})
			continue
		}

		if pkg.Tests == 0 {
			// Compiled, ran nothing. Usually `-run` filtering. Counted,
			// not judged: forge cannot tell an intentional filter from
			// an accidental one, and guessing would cry wolf on every
			// targeted run.
			totals.NoTestsRan++
			if pkg.Result == OutcomeFail {
				totals.FailedPackages++
				totals.SuiteFailed = true
				a.Findings = append(a.Findings, Finding{
					Package: pkg.Path,
					Kind:    KindFailed,
					Detail:  "the package reported failure without running any test (TestMain or a package-level panic)",
				})
			}
			continue
		}

		totals.Packages++
		// Note every exemption that COVERS this package, whatever the
		// verdict turns out to be. Marked here, once, so "matched
		// something in this run" cannot drift apart from "suppressed
		// something in this run" — the difference between the two is
		// exactly what makes a declaration stale.
		for i, e := range p.Allow {
			if matchPackage(e.Package, pkg.Path) {
				matchedAny[i] = true
			}
		}
		totals.Tests += pkg.Tests
		totals.Passed += pkg.Passed
		totals.Failed += pkg.Failed
		totals.Skipped += pkg.Skipped
		if pkg.Failed > 0 || pkg.Result == OutcomeFail {
			totals.FailedPackages++
			totals.SuiteFailed = true
			// Named, one line, so forge's own output is sufficient when
			// CI shows only the last step. The failure DOMINATES this
			// package's skip verdict rather than joining it: a package
			// gets one line, and "tests failed here" is the more urgent
			// of the two things that could be said about it.
			a.Findings = append(a.Findings, Finding{
				Package: pkg.Path,
				Kind:    KindFailed,
				Tests:   pkg.Tests,
				Skipped: pkg.Skipped,
				Detail:  fmt.Sprintf("%d of %d test(s) failed", failedCount(pkg), pkg.Tests),
			})
			continue
		}

		ratio := pkg.SkipRatio()
		kind := Kind("")
		switch {
		case pkg.Skipped == pkg.Tests:
			kind = KindZeroEvidence
		case ratio > p.MaxSkipRatio && pkg.Tests >= p.MinTests:
			kind = KindMassSkip
		}
		if kind == "" {
			continue
		}

		if i, e, ok := findExemption(p.Allow, pkg.Path, ratio); ok {
			suppressedAny[i] = true
			a.Suppressed = append(a.Suppressed, Suppressed{
				Package: pkg.Path, Kind: kind, Tests: pkg.Tests,
				Skipped: pkg.Skipped, Ratio: ratio, Exemption: e,
			})
			continue
		}
		finding := Finding{
			Package: pkg.Path,
			Kind:    kind,
			Tests:   pkg.Tests,
			Skipped: pkg.Skipped,
			Ratio:   ratio,
			Reasons: topReasons(pkg.SkipReasons, 2),
		}
		if len(finding.Reasons) == 0 {
			// Only when the messages could not be recovered — names
			// locate the tests, reasons tell you what to supply, and
			// printing both would be two lines to say one thing.
			finding.Examples = examples(pkg.Skips, 3)
		}
		a.Findings = append(a.Findings, finding)
	}

	for i, e := range p.Allow {
		if matchedAny[i] && !suppressedAny[i] {
			a.Stale = append(a.Stale, e)
		}
	}

	if totals.Tests > 0 {
		totals.SkipRatio = float64(totals.Skipped) / float64(totals.Tests)
	}
	a.Totals = totals

	// Worst first: zero-evidence above mass-skip, then by ratio, then by
	// how many tests went missing. A reader who stops after the first
	// line has read the worst one.
	sort.SliceStable(a.Findings, func(i, j int) bool {
		x, y := a.Findings[i], a.Findings[j]
		if rank(x.Kind) != rank(y.Kind) {
			return rank(x.Kind) < rank(y.Kind)
		}
		if x.Ratio != y.Ratio {
			return x.Ratio > y.Ratio
		}
		if x.Skipped != y.Skipped {
			return x.Skipped > y.Skipped
		}
		return x.Package < y.Package
	})
	return a
}

// failedCount reports how many tests failed, falling back to "at least one"
// when the package failed without any individual test failing (a TestMain
// exit, a post-test panic).
func failedCount(p Package) int {
	if p.Failed > 0 {
		return p.Failed
	}
	return 1
}

func rank(k Kind) int {
	switch k {
	case KindFailed:
		return 0
	case KindZeroEvidence:
		return 1
	default:
		return 2
	}
}

// topReasons returns the n most common skip messages, ties broken
// alphabetically so the report is stable across runs.
func topReasons(counts map[string]int, n int) []ReasonCount {
	if len(counts) == 0 {
		return nil
	}
	out := make([]ReasonCount, 0, len(counts))
	for reason, c := range counts {
		out = append(out, ReasonCount{Reason: reason, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Reason < out[j].Reason
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func examples(names []string, n int) []string {
	if len(names) <= n {
		return names
	}
	return names[:n]
}

// findExemption returns the first exemption covering pkg at the given ratio.
func findExemption(allow []Exemption, pkg string, ratio float64) (int, Exemption, bool) {
	for i, e := range allow {
		if matchPackage(e.Package, pkg) && ratio <= e.allowance() {
			return i, e, true
		}
	}
	return 0, Exemption{}, false
}

// matchPackage reports whether pattern names pkg (a Go import path).
//
// Three spellings are accepted because three are what people write: the full
// import path (what `go test -json` prints), the tail fragment they think in
// ("internal/threads"), and either with Go's own "/..." subtree suffix. The
// tail form requires a "/" boundary, so "db" cannot match "…/internal/scaffolddb".
func matchPackage(pattern, pkg string) bool {
	pattern = strings.TrimSuffix(strings.TrimSpace(pattern), "/")
	pattern = strings.TrimPrefix(pattern, "./")
	if pattern == "" {
		return false
	}
	if base, ok := strings.CutSuffix(pattern, "/..."); ok {
		return matchExact(base, pkg) || matchExact(base, trimLastSegments(pkg, base))
	}
	return matchExact(pattern, pkg)
}

// matchExact matches a full import path or a "/"-bounded tail of one.
func matchExact(pattern, pkg string) bool {
	return pkg == pattern || strings.HasSuffix(pkg, "/"+pattern)
}

// trimLastSegments walks pkg's prefixes so a "/..." pattern matches a package
// nested under the named directory: pattern "internal/db" against package
// "…/internal/db/migrations" tries "…/internal/db" and matches.
func trimLastSegments(pkg, base string) string {
	segs := strings.Count(base, "/") + 1
	parts := strings.Split(pkg, "/")
	for n := len(parts) - 1; n >= segs; n-- {
		candidate := strings.Join(parts[:n], "/")
		if matchExact(base, candidate) {
			return candidate
		}
	}
	return pkg
}
