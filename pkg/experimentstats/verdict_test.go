package experimentstats

import (
	"errors"
	"testing"
)

func even(variants ...string) map[string]int64 {
	w := map[string]int64{}
	for _, v := range variants {
		w[v] = int64(10000 / len(variants))
	}
	return w
}

func TestAnalyzeBetter(t *testing.T) {
	res, err := Analyze(Input{
		Arms: []Arm{
			{Variant: "control", Assigned: 10000, Units: 10000, Conversions: 200},
			{Variant: "treatment", Assigned: 10000, Units: 10000, Conversions: 260},
		},
		Control: "control", WeightsBP: even("control", "treatment"), MinSamplePerArm: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictTreatmentBetter || res.Best != "treatment" || res.Reason != "" {
		t.Fatalf("verdict = %s (%s, best %q), want treatment_better", res.Verdict, res.Reason, res.Best)
	}
	if res.Alpha != DefaultAlpha {
		t.Errorf("alpha = %v, want the default", res.Alpha)
	}
	c := res.Comparisons[0]
	near(t, "absolute lift", c.AbsoluteLift, 0.006, 1e-12)
	if c.RelativeLift == nil {
		t.Fatal("no relative lift")
	}
	near(t, "relative lift", *c.RelativeLift, 0.3, 1e-12)
	near(t, "z", c.Z, 2.830251652793251, 1e-12)
	nearRel(t, "p", c.PValue, 0.004651140450960617, 1e-10)
	if c.Alpha != 0.05 || c.DiffCI.Low <= 0 {
		t.Errorf("comparison alpha %v, interval %v", c.Alpha, c.DiffCI)
	}
	near(t, "control rate", res.Arms[0].Rate, 0.02, 1e-12)
	if !res.SRM.Checked || res.SRM.Failed || res.SRM.ChiSquare != 0 || res.SRM.PValue != 1 || res.SRM.DF != 1 {
		t.Errorf("SRM = %+v, want a passing check", res.SRM)
	}
}

func TestAnalyzeWorse(t *testing.T) {
	res, err := Analyze(Input{
		Arms:    []Arm{{Variant: "a", Assigned: 70, Units: 70, Conversions: 56}, {Variant: "b", Assigned: 80, Units: 80, Conversions: 48}},
		Control: "a", WeightsBP: map[string]int64{"a": 4667, "b": 5333}, MinSamplePerArm: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictTreatmentWorse || res.Best != "" {
		t.Fatalf("verdict = %s, want treatment_worse", res.Verdict)
	}
	c := res.Comparisons[0]
	near(t, "diff low", c.DiffCI.Low, -0.33387265403690614, 1e-12)
	near(t, "diff high", c.DiffCI.High, -0.05243147240236498, 1e-12)
}

func TestAnalyzeInconclusive(t *testing.T) {
	base := func(c, tr Arm, min int64) Input {
		c.Variant, tr.Variant = "control", "treatment"
		return Input{Arms: []Arm{c, tr}, Control: "control", WeightsBP: even("control", "treatment"), MinSamplePerArm: min}
	}
	cases := []struct {
		name   string
		in     Input
		reason Reason
	}{
		{
			// A difference that would be significant, read before the
			// pre-registered sample: never a winner.
			name:   "below the minimum sample",
			in:     base(Arm{Assigned: 900, Units: 900, Conversions: 18}, Arm{Assigned: 900, Units: 900, Conversions: 60}, 1000),
			reason: ReasonBelowMinSample,
		},
		{
			name:   "enough assigned, too few windows complete",
			in:     base(Arm{Assigned: 5000, Units: 400, Conversions: 8}, Arm{Assigned: 5000, Units: 400, Conversions: 30}, 1000),
			reason: ReasonWindowNotComplete,
		},
		{
			name:   "no window complete yet",
			in:     base(Arm{Assigned: 5000}, Arm{Assigned: 5000}, 1000),
			reason: ReasonWindowNotComplete,
		},
		{
			// 11.5% vs 10% of 1000: p = 0.28, the interval spans zero.
			name:   "the interval spans zero",
			in:     base(Arm{Assigned: 1000, Units: 1000, Conversions: 100}, Arm{Assigned: 1000, Units: 1000, Conversions: 115}, 1000),
			reason: ReasonIntervalSpansZero,
		},
		{
			name:   "identical arms",
			in:     base(Arm{Assigned: 2000, Units: 2000, Conversions: 100}, Arm{Assigned: 2000, Units: 2000, Conversions: 100}, 1000),
			reason: ReasonIntervalSpansZero,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := Analyze(c.in)
			if err != nil {
				t.Fatal(err)
			}
			if res.Verdict != VerdictInconclusive || res.Reason != c.reason {
				t.Errorf("verdict = %s (%s), want inconclusive (%s)", res.Verdict, res.Reason, c.reason)
			}
			if got := res.Comparisons[0]; got.Verdict != VerdictInconclusive || got.Reason != c.reason {
				t.Errorf("comparison = %s (%s)", got.Verdict, got.Reason)
			}
		})
	}
}

// Bonferroni: 130 vs 100 of 1000 is significant alone (p = 0.035, Newcombe
// [0.002, 0.058]) and not at 0.025 with a second treatment (Newcombe
// [-0.002, 0.062]), while 160 vs 100 stays significant.
func TestAnalyzeCorrectsForSeveralTreatments(t *testing.T) {
	two := Input{
		Arms: []Arm{
			{Variant: "control", Assigned: 1000, Units: 1000, Conversions: 100},
			{Variant: "b", Assigned: 1000, Units: 1000, Conversions: 130},
		},
		Control: "control", WeightsBP: even("control", "b"), MinSamplePerArm: 500,
	}
	res, err := Analyze(two)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictTreatmentBetter {
		t.Fatalf("alone: verdict %s, want treatment_better", res.Verdict)
	}
	three := two
	three.Arms = append(append([]Arm{}, two.Arms...), Arm{Variant: "c", Assigned: 1000, Units: 1000, Conversions: 160})
	three.WeightsBP = map[string]int64{"control": 3334, "b": 3333, "c": 3333}
	res, err = Analyze(three)
	if err != nil {
		t.Fatal(err)
	}
	b, c := res.Comparisons[0], res.Comparisons[1]
	if b.Alpha != 0.025 || b.Verdict != VerdictInconclusive || b.Reason != ReasonIntervalSpansZero {
		t.Errorf("b at alpha %v: %s (%s), want inconclusive at 0.025", b.Alpha, b.Verdict, b.Reason)
	}
	near(t, "b diff low", b.DiffCI.Low, -0.002047869310340629, 1e-12)
	near(t, "c diff low", c.DiffCI.Low, 0.026348838278851643, 1e-12)
	if c.Verdict != VerdictTreatmentBetter || res.Verdict != VerdictTreatmentBetter || res.Best != "c" {
		t.Errorf("c %s, run %s best %q; want c better", c.Verdict, res.Verdict, res.Best)
	}
	// The arms' own intervals stay at 1-alpha.
	near(t, "control rate high", res.Arms[0].RateCI.High, 0.1201519631953484, 1e-12)
}

func TestAnalyzeSampleRatioMismatch(t *testing.T) {
	// 5200 / 4800 against 50/50: chi-square 16, p = 6.3e-5.
	in := Input{
		Arms: []Arm{
			{Variant: "control", Assigned: 5200, Units: 5200, Conversions: 100},
			{Variant: "treatment", Assigned: 4800, Units: 4800, Conversions: 300},
		},
		Control: "control", WeightsBP: even("control", "treatment"), MinSamplePerArm: 100,
	}
	res, err := Analyze(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictSRMFailed || !res.SRM.Failed || res.Comparisons[0].Verdict != VerdictSRMFailed {
		t.Fatalf("verdict %s, comparison %s, SRM %+v; want srm_failed overriding a clear effect", res.Verdict, res.Comparisons[0].Verdict, res.SRM)
	}
	near(t, "chi-square", res.SRM.ChiSquare, 16, 1e-12)
	nearRel(t, "SRM p", res.SRM.PValue, 6.334248366623988e-05, 1e-9)
	near(t, "expected", res.SRM.Expected["control"], 5000, 1e-12)

	// 10120 / 9880: chi-square 2.88, p = 0.0897 — not a mismatch.
	in.Arms[0].Assigned, in.Arms[1].Assigned = 10120, 9880
	if res, _ = Analyze(in); res.SRM.Failed {
		t.Errorf("a 10120/9880 split failed SRM: %+v", res.SRM)
	}
	nearRel(t, "SRM p", res.SRM.PValue, 0.08968602177036435, 1e-9)

	// Three arms against an uneven plan, the window-incomplete units
	// included (the check reads Assigned, not Units).
	res, err = Analyze(Input{
		Arms: []Arm{
			{Variant: "a", Assigned: 3400, Units: 10}, {Variant: "b", Assigned: 3300, Units: 10}, {Variant: "c", Assigned: 3300, Units: 10},
		},
		Control: "a", WeightsBP: map[string]int64{"a": 3334, "b": 3333, "c": 3333}, MinSamplePerArm: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	near(t, "three-arm chi-square", res.SRM.ChiSquare, 1.9600040387962012, 1e-9)
	nearRel(t, "three-arm SRM p", res.SRM.PValue, 0.3753103409496445, 1e-8)
	if res.SRM.DF != 2 || res.SRM.Failed || res.Verdict != VerdictInconclusive || res.Reason != ReasonWindowNotComplete {
		t.Errorf("three arms: %+v, verdict %s (%s)", res.SRM, res.Verdict, res.Reason)
	}
}

func TestAnalyzeUnplannedArmFailsSRM(t *testing.T) {
	res, err := Analyze(Input{
		Arms: []Arm{
			{Variant: "control", Assigned: 5000, Units: 5000, Conversions: 100},
			{Variant: "treatment", Assigned: 5000, Units: 5000, Conversions: 100},
			{Variant: "stale", Assigned: 3},
		},
		Control: "control", WeightsBP: even("control", "treatment"), MinSamplePerArm: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictSRMFailed || len(res.SRM.Unplanned) != 1 || res.SRM.Unplanned[0] != "stale" {
		t.Errorf("verdict %s, unplanned %v; want srm_failed naming stale", res.Verdict, res.SRM.Unplanned)
	}
}

func TestAnalyzeSkipsSRMOnTinySamples(t *testing.T) {
	res, err := Analyze(Input{
		Arms:    []Arm{{Variant: "c", Assigned: 3, Units: 3}, {Variant: "t", Assigned: 0}},
		Control: "c", WeightsBP: even("c", "t"), MinSamplePerArm: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SRM.Checked || res.SRM.Failed || res.Verdict != VerdictInconclusive || res.Reason != ReasonBelowMinSample {
		t.Errorf("SRM %+v, verdict %s (%s)", res.SRM, res.Verdict, res.Reason)
	}
}

func TestAnalyzeNoData(t *testing.T) {
	res, err := Analyze(Input{
		Arms:    []Arm{{Variant: "control"}},
		Control: "control", WeightsBP: even("control", "treatment"), MinSamplePerArm: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictNoData || res.SRM.Checked {
		t.Fatalf("verdict %s, SRM %+v; want no_data", res.Verdict, res.SRM)
	}
	// The planned treatment missing from Arms is added, zero.
	if len(res.Arms) != 2 || res.Arms[1].Variant != "treatment" || res.Comparisons[0].Verdict != VerdictNoData {
		t.Errorf("arms %+v, comparisons %+v", res.Arms, res.Comparisons)
	}
	if r := res.Arms[0]; r.Rate != 0 || r.RateCI != (Interval{0, 1}) {
		t.Errorf("an empty arm's rate %v %v", r.Rate, r.RateCI)
	}
}

func TestAnalyzeZeroControlRate(t *testing.T) {
	res, err := Analyze(Input{
		Arms:    []Arm{{Variant: "c", Assigned: 20, Units: 20}, {Variant: "t", Assigned: 10, Units: 10, Conversions: 10}},
		Control: "c", WeightsBP: map[string]int64{"c": 6667, "t": 3333}, MinSamplePerArm: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Comparisons[0]
	if c.RelativeLift != nil {
		t.Errorf("relative lift over a zero control rate = %v, want undefined", *c.RelativeLift)
	}
	near(t, "diff low", c.DiffCI.Low, 0.6790860371419145, 1e-12)
	if c.Verdict != VerdictTreatmentBetter {
		t.Errorf("verdict %s", c.Verdict)
	}
}

func TestAnalyzeRefusesInconsistentInput(t *testing.T) {
	ok := Input{
		Arms:    []Arm{{Variant: "c", Assigned: 10, Units: 5, Conversions: 1}, {Variant: "t", Assigned: 10, Units: 5, Conversions: 1}},
		Control: "c", WeightsBP: even("c", "t"), MinSamplePerArm: 1,
	}
	if _, err := Analyze(ok); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Input){
		"unknown control":        func(in *Input) { in.Control = "x" },
		"no treatment":           func(in *Input) { in.Arms = in.Arms[:1]; in.WeightsBP = map[string]int64{"c": 1} },
		"duplicate variant":      func(in *Input) { in.Arms[1].Variant = "c" },
		"units above assigned":   func(in *Input) { in.Arms[0].Units = 11 },
		"conversions above unit": func(in *Input) { in.Arms[0].Conversions = 6 },
		"negative count":         func(in *Input) { in.Arms[0].Assigned = -1 },
		"no planned weight":      func(in *Input) { in.WeightsBP = nil },
		"negative weight":        func(in *Input) { in.WeightsBP = map[string]int64{"c": 1, "t": -1} },
		"zero minimum":           func(in *Input) { in.MinSamplePerArm = 0 },
		"alpha above a half":     func(in *Input) { in.Alpha = 0.6 },
		"negative alpha":         func(in *Input) { in.Alpha = -0.1 },
	} {
		in := ok
		in.Arms = append([]Arm{}, ok.Arms...)
		mutate(&in)
		if _, err := Analyze(in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
}
