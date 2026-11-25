package experimentstats

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// Verdict is what an experiment's data supports.
type Verdict string

const (
	// VerdictNoData: no unit has been assigned.
	VerdictNoData Verdict = "no_data"
	// VerdictSRMFailed: the arms' sizes do not match the planned split, so
	// assignment or its recording is broken and no effect can be trusted.
	// It overrides every other verdict.
	VerdictSRMFailed Verdict = "srm_failed"
	// VerdictInconclusive: the data cannot support a decision (Reason says
	// why).
	VerdictInconclusive Verdict = "inconclusive"
	// VerdictTreatmentBetter: the treatment converts at a higher rate.
	VerdictTreatmentBetter Verdict = "treatment_better"
	// VerdictTreatmentWorse: the treatment converts at a lower rate.
	VerdictTreatmentWorse Verdict = "treatment_worse"
)

// Reason says why a verdict is inconclusive.
type Reason string

const (
	// ReasonBelowMinSample: an arm has fewer than MinSamplePerArm assigned
	// units. Keep the run going.
	ReasonBelowMinSample Reason = "below_min_sample"
	// ReasonWindowNotComplete: the arms have enough assigned units, but too
	// few of them have had their whole attribution window yet.
	ReasonWindowNotComplete Reason = "window_not_complete"
	// ReasonIntervalSpansZero: enough data, and no detectable difference:
	// the difference interval includes zero, or the p-value is not below
	// the comparison's level.
	ReasonIntervalSpansZero Reason = "interval_spans_zero"
)

const (
	// DefaultAlpha is the significance level when Input.Alpha is zero.
	DefaultAlpha = 0.05
	// SRMAlpha is the sample-ratio mismatch threshold: a chi-square p-value
	// below it fails the run. Strict, because the check runs on every
	// readout and a false alarm hides a valid result.
	SRMAlpha = 0.001
	// SRMMinExpected is the smallest expected arm size the chi-square
	// approximation is trusted at; below it the check is skipped.
	SRMMinExpected = 5
)

// Arm is one variant's counts, from pkg/obsquery's ExperimentArm.
type Arm struct {
	Variant string
	// Assigned is every unit assigned to the arm, window complete or not:
	// the sample-ratio check reads it.
	Assigned int64
	// Units are the assigned units whose attribution window is complete,
	// and Conversions those of them that converted: the effect reads these.
	Units, Conversions int64
}

// Input is one experiment run's counts and its pre-registered plan.
type Input struct {
	// Arms are the run's variants, the control among them.
	Arms []Arm
	// Control names the control arm.
	Control string
	// WeightsBP is the planned split in basis points by variant (they sum to
	// 10000 in a forge.changes/v1 split; only the ratios matter here). A
	// variant with no weight is not planned.
	WeightsBP map[string]int64
	// MinSamplePerArm is the pre-registered sample size: below it, in any
	// arm, every verdict is inconclusive. At least 1.
	MinSamplePerArm int64
	// Alpha is the significance level, (0, 0.5]; zero means DefaultAlpha.
	Alpha float64
}

// ArmResult is one arm's rate.
type ArmResult struct {
	Variant                      string
	Assigned, Units, Conversions int64
	// Rate is Conversions / Units (0 with no units), and RateCI its Wilson
	// interval at 1-Alpha ([0, 1] with no units).
	Rate   float64
	RateCI Interval
}

// Comparison is one treatment against the control.
type Comparison struct {
	Treatment, Control string
	// AbsoluteLift is the treatment's rate minus the control's.
	AbsoluteLift float64
	// RelativeLift is AbsoluteLift over the control's rate; nil when the
	// control's rate is 0.
	RelativeLift *float64
	// DiffCI is Newcombe's interval for AbsoluteLift at 1-Alpha.
	DiffCI Interval
	// Z and PValue are the pooled two-proportion z-test's.
	Z, PValue float64
	// Alpha is this comparison's level: Input.Alpha over the number of
	// treatments (Bonferroni).
	Alpha float64
	// Verdict and, when inconclusive, Reason.
	Verdict Verdict
	Reason  Reason
}

// SRM is the sample-ratio mismatch check.
type SRM struct {
	// Checked says the chi-square test ran. It does not run with fewer than
	// two planned arms, with no assigned units, or while an expected arm
	// size is below SRMMinExpected.
	Checked bool
	// ChiSquare, DF and PValue are the goodness-of-fit test's.
	ChiSquare float64
	DF        int
	PValue    float64
	// Expected are the planned arm sizes for the assigned total, by
	// variant.
	Expected map[string]float64
	// Unplanned are the variants with assigned units but no planned
	// weight, sorted; any fails the check.
	Unplanned []string
	// Failed: PValue < SRMAlpha, or an unplanned variant has units.
	Failed bool
}

// Result is the verdict and every number behind it.
type Result struct {
	// Verdict is the run's: VerdictSRMFailed, else VerdictNoData, else
	// the one comparison's, else (several treatments) treatment_better when
	// any treatment is better (Best names it), treatment_worse when every
	// treatment is worse, and inconclusive otherwise.
	Verdict Verdict
	// Reason explains an inconclusive Verdict: the first of below_min_sample,
	// window_not_complete, interval_spans_zero that any comparison has.
	Reason Reason
	// Best is the better treatment with the highest difference-interval
	// lower bound; empty unless Verdict is treatment_better.
	Best string
	// Arms are Input's arms in order, then any planned variant missing from
	// them (zero counts), sorted.
	Arms []ArmResult
	// Comparisons are the treatments against the control, in Arms order.
	Comparisons []Comparison
	SRM         SRM
	// Alpha is the run's significance level.
	Alpha float64
}

// ErrInvalidInput marks an Input that cannot be analyzed.
var ErrInvalidInput = errors.New("invalid experiment input")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, a...))
}

// Analyze computes the verdict for in. It fails only on an input that is
// inconsistent (counts that cannot be, an unknown control, no treatment).
func Analyze(in Input) (Result, error) {
	alpha := in.Alpha
	if alpha == 0 {
		alpha = DefaultAlpha
	}
	if !(alpha > 0 && alpha <= 0.5) {
		return Result{}, invalid("alpha %v must be in (0, 0.5]", in.Alpha)
	}
	if in.MinSamplePerArm < 1 {
		return Result{}, invalid("min sample per arm must be at least 1")
	}
	arms, err := armsOf(in)
	if err != nil {
		return Result{}, err
	}
	control := -1
	for i, a := range arms {
		if a.Variant == in.Control {
			control = i
		}
	}
	if control < 0 {
		return Result{}, invalid("control %q is not an arm", in.Control)
	}
	if len(arms) < 2 {
		return Result{}, invalid("an experiment needs a control and at least one treatment")
	}

	res := Result{Alpha: alpha, SRM: sampleRatio(arms, in.WeightsBP)}
	z := ZCritical(alpha)
	var total int64
	for _, a := range arms {
		total += a.Assigned
		r := ArmResult{Variant: a.Variant, Assigned: a.Assigned, Units: a.Units, Conversions: a.Conversions, RateCI: Wilson(a.Conversions, a.Units, z)}
		if a.Units > 0 {
			r.Rate = float64(a.Conversions) / float64(a.Units)
		}
		res.Arms = append(res.Arms, r)
	}

	k := len(arms) - 1
	cmpAlpha := alpha / float64(k)
	cmpZ := ZCritical(cmpAlpha)
	c := arms[control]
	for i, t := range arms {
		if i == control {
			continue
		}
		res.Comparisons = append(res.Comparisons, compare(c, t, cmpAlpha, cmpZ, in.MinSamplePerArm))
	}

	switch {
	case res.SRM.Failed:
		res.Verdict = VerdictSRMFailed
		for i := range res.Comparisons {
			res.Comparisons[i].Verdict, res.Comparisons[i].Reason = VerdictSRMFailed, ""
		}
	case total == 0:
		res.Verdict = VerdictNoData
		for i := range res.Comparisons {
			res.Comparisons[i].Verdict, res.Comparisons[i].Reason = VerdictNoData, ""
		}
	default:
		res.Verdict, res.Reason, res.Best = overall(res.Comparisons)
	}
	return res, nil
}

// armsOf validates in's arms and appends every planned variant they lack.
func armsOf(in Input) ([]Arm, error) {
	seen := map[string]bool{}
	arms := make([]Arm, 0, len(in.Arms))
	for _, a := range in.Arms {
		switch {
		case seen[a.Variant]:
			return nil, invalid("variant %q is listed twice", a.Variant)
		case a.Assigned < 0 || a.Units < 0 || a.Conversions < 0:
			return nil, invalid("variant %q has a negative count", a.Variant)
		case a.Units > a.Assigned:
			return nil, invalid("variant %q has more window-complete units than assigned ones", a.Variant)
		case a.Conversions > a.Units:
			return nil, invalid("variant %q has more conversions than window-complete units", a.Variant)
		}
		seen[a.Variant] = true
		arms = append(arms, a)
	}
	var missing []string
	var weight int64
	for v, w := range in.WeightsBP {
		if w < 0 {
			return nil, invalid("variant %q has a negative weight", v)
		}
		weight += w
		if w > 0 && !seen[v] {
			missing = append(missing, v)
		}
	}
	if weight <= 0 {
		return nil, invalid("the planned split has no weight")
	}
	sort.Strings(missing)
	for _, v := range missing {
		arms = append(arms, Arm{Variant: v})
	}
	return arms, nil
}

// sampleRatio is the chi-square goodness-of-fit test of the arms' assigned
// units against the planned weights.
func sampleRatio(arms []Arm, weights map[string]int64) SRM {
	s := SRM{Expected: map[string]float64{}, Unplanned: []string{}}
	var n, w float64
	planned := 0
	for _, a := range arms {
		if weights[a.Variant] > 0 {
			n += float64(a.Assigned)
			w += float64(weights[a.Variant])
			planned++
		} else if a.Assigned > 0 {
			s.Unplanned = append(s.Unplanned, a.Variant)
		}
	}
	sort.Strings(s.Unplanned)
	s.Failed = len(s.Unplanned) > 0
	small := false
	for _, a := range arms {
		if wt := weights[a.Variant]; wt > 0 {
			e := n * float64(wt) / w
			s.Expected[a.Variant] = e
			if e < SRMMinExpected {
				small = true
			}
		}
	}
	if planned < 2 || n == 0 || small {
		return s
	}
	for _, a := range arms {
		if e, ok := s.Expected[a.Variant]; ok {
			d := float64(a.Assigned) - e
			s.ChiSquare += d * d / e
		}
	}
	s.Checked, s.DF = true, planned-1
	s.PValue = ChiSquareSF(s.ChiSquare, s.DF)
	if s.PValue < SRMAlpha {
		s.Failed = true
	}
	return s
}

// compare tests treatment t against control c at level alpha (critical value
// z), deciding only once both arms reach minSample window-complete units.
func compare(c, t Arm, alpha, z float64, minSample int64) Comparison {
	out := Comparison{Treatment: t.Variant, Control: c.Variant, Alpha: alpha, PValue: 1, DiffCI: Interval{-1, 1}}
	if c.Units > 0 && t.Units > 0 {
		pc, pt := float64(c.Conversions)/float64(c.Units), float64(t.Conversions)/float64(t.Units)
		out.AbsoluteLift = pt - pc
		if pc > 0 {
			rel := (pt - pc) / pc
			out.RelativeLift = &rel
		}
		out.DiffCI = NewcombeDiff(c.Conversions, c.Units, t.Conversions, t.Units, z)
		out.Z, out.PValue = TwoProportionZ(c.Conversions, c.Units, t.Conversions, t.Units)
	}
	out.Verdict = VerdictInconclusive
	switch {
	case min(c.Units, t.Units) < minSample:
		// The decision is gated on window-complete units, the ones the
		// estimate is computed from, and on nothing else. Assigned only
		// says why it is not yet: an arm short of assigned units needs
		// more traffic; one with enough needs its windows to complete.
		out.Reason = ReasonWindowNotComplete
		if min(c.Assigned, t.Assigned) < minSample {
			out.Reason = ReasonBelowMinSample
		}
	case out.PValue < alpha && out.DiffCI.Low > 0:
		out.Verdict = VerdictTreatmentBetter
	case out.PValue < alpha && out.DiffCI.High < 0:
		out.Verdict = VerdictTreatmentWorse
	default:
		out.Reason = ReasonIntervalSpansZero
	}
	return out
}

// overall folds the comparisons into the run's verdict.
func overall(cs []Comparison) (Verdict, Reason, string) {
	if len(cs) == 1 {
		best := ""
		if cs[0].Verdict == VerdictTreatmentBetter {
			best = cs[0].Treatment
		}
		return cs[0].Verdict, cs[0].Reason, best
	}
	best, bestLow := "", math.Inf(-1)
	worse := 0
	for _, c := range cs {
		switch c.Verdict {
		case VerdictTreatmentBetter:
			if c.DiffCI.Low > bestLow {
				best, bestLow = c.Treatment, c.DiffCI.Low
			}
		case VerdictTreatmentWorse:
			worse++
		}
	}
	if best != "" {
		return VerdictTreatmentBetter, "", best
	}
	if worse == len(cs) {
		return VerdictTreatmentWorse, "", ""
	}
	for _, r := range []Reason{ReasonBelowMinSample, ReasonWindowNotComplete, ReasonIntervalSpansZero} {
		for _, c := range cs {
			if c.Reason == r {
				return VerdictInconclusive, r, ""
			}
		}
	}
	return VerdictInconclusive, ReasonIntervalSpansZero, ""
}
