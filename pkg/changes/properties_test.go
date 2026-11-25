package changes

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
)

// The properties the spec guarantees, over fixed synthetic unit sets from
// testdata/vectors.json. Every count is recorded there too, and the web twin
// asserts the same numbers, so the tolerances below are checks on a fixed
// data set, never a sample that can flake.

// propSnapshot is a one-change snapshot of key with the given variants and
// delivery.
func propSnapshot(t *testing.T, key string, variants map[string]any, delivery map[string]any) *Snapshot {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"schema":   schemaV1,
		"revision": 1,
		"changes": map[string]any{
			key: map[string]any{"variants": variants, "delivery": delivery},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func unitIDs(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + strconv.Itoa(i)
	}
	return out
}

// withinBinomial reports whether count is within five standard deviations of
// n trials at probability p.
func withinBinomial(count, n int, p float64) bool {
	mean := float64(n) * p
	return math.Abs(float64(count)-mean) <= 5*math.Sqrt(mean*(1-p))+1
}

var abVariants = map[string]any{"control": false, "treatment": true}

func experiment(salt string, percentBP int, split any) map[string]any {
	return map[string]any{
		"mode": "experiment", "unit": "user", "run": salt, "salt": salt,
		"percent_bp": percentBP, "split": split,
	}
}

var fiftyFifty = []map[string]any{
	{"variant": "control", "weight_bp": 5000},
	{"variant": "treatment", "weight_bp": 5000},
}

// TestRampOnlyAddsUnits: within a run, raising percent_bp only adds units,
// each keeps its variant, and lowering then raising returns the same units.
func TestRampOnlyAddsUnits(t *testing.T) {
	p := loadVectors(t).Properties
	r := p.Ramp
	ids := unitIDs(p.UnitPrefix, r.Units)
	var counts []int
	steps := make([]map[string]string, 0, len(r.StepsBP))
	for _, pct := range r.StepsBP {
		s := propSnapshot(t, r.Key, abVariants, experiment(r.Salt, pct, fiftyFifty))
		cur := map[string]string{}
		for _, id := range ids {
			if res := Evaluate(s, r.Key, EvalContext{TargetingKey: id}); res.Enrolled {
				cur[id] = res.Variant
			}
		}
		if !withinBinomial(len(cur), r.Units, float64(pct)/fullBP) {
			t.Errorf("percent_bp %d enrolled %d of %d", pct, len(cur), r.Units)
		}
		counts = append(counts, len(cur))
		steps = append(steps, cur)
	}
	// Each pair of steps: the smaller set is inside the larger, with the
	// same variants; equal percentages give equal sets.
	for i := range steps {
		for j := range steps {
			if r.StepsBP[i] > r.StepsBP[j] {
				continue
			}
			for id, variant := range steps[i] {
				if got, ok := steps[j][id]; !ok || got != variant {
					t.Fatalf("%s enrolled as %q at %d bp but %q (enrolled %v) at %d bp",
						id, variant, r.StepsBP[i], got, ok, r.StepsBP[j])
				}
			}
		}
	}
	assertCounts(t, "ramp enrolled", counts, r.Enrolled)
}

// TestNewSaltReRandomizes: a new run's salt draws a fresh, independent
// cohort of the same size.
func TestNewSaltReRandomizes(t *testing.T) {
	p := loadVectors(t).Properties
	r := p.Resalt
	ids := unitIDs(p.UnitPrefix, r.Units)
	sets := make([]map[string]bool, len(r.Salts))
	counts := make([]int, len(r.Salts))
	for i, salt := range r.Salts {
		s := propSnapshot(t, r.Key, abVariants, experiment(salt, r.PercentBP, fiftyFifty))
		sets[i] = map[string]bool{}
		for _, id := range ids {
			if Evaluate(s, r.Key, EvalContext{TargetingKey: id}).Enrolled {
				sets[i][id] = true
			}
		}
		counts[i] = len(sets[i])
	}
	overlap := 0
	for id := range sets[0] {
		if sets[1][id] {
			overlap++
		}
	}
	q := float64(r.PercentBP) / fullBP
	if !withinBinomial(overlap, r.Units, q*q) {
		t.Errorf("overlap %d of %d, want about %.0f (independent cohorts)", overlap, r.Units, float64(r.Units)*q*q)
	}
	assertCounts(t, "resalt enrolled", counts, r.Enrolled)
	if overlap != r.Overlap {
		t.Errorf("overlap %d, recorded %d", overlap, r.Overlap)
	}
}

// TestSplitProportions: at 100%, each arm's share matches its weight.
func TestSplitProportions(t *testing.T) {
	p := loadVectors(t).Properties
	sp := p.Split
	variants := map[string]any{}
	for _, e := range sp.Split {
		variants[e.Variant] = e.Variant
	}
	s := propSnapshot(t, sp.Key, variants, experiment(sp.Salt, fullBP, sp.Split))
	counts := map[string]int{}
	for _, id := range unitIDs(p.UnitPrefix, sp.Units) {
		res := Evaluate(s, sp.Key, EvalContext{TargetingKey: id})
		if !res.Enrolled {
			t.Fatalf("%s not enrolled at 100%%: %+v", id, res)
		}
		counts[res.Variant]++
	}
	for _, e := range sp.Split {
		if !withinBinomial(counts[e.Variant], sp.Units, float64(e.WeightBP)/fullBP) {
			t.Errorf("%s: %d of %d at weight %d", e.Variant, counts[e.Variant], sp.Units, e.WeightBP)
		}
		if counts[e.Variant] != sp.Counts[e.Variant] {
			t.Errorf("%s: %d, recorded %d", e.Variant, counts[e.Variant], sp.Counts[e.Variant])
		}
	}
}

// TestEnrollmentIndependentOfVariant: the treatment share among enrolled
// units is the split's, whatever the percentage, and a unit's variant does
// not depend on the percentage it was enrolled at.
func TestEnrollmentIndependentOfVariant(t *testing.T) {
	p := loadVectors(t).Properties
	in := p.Independence
	split := p.Split.Split
	variants := map[string]any{}
	var treatmentBP int
	for _, e := range split {
		variants[e.Variant] = e.Variant
		if e.Variant == "treatment" {
			treatmentBP = e.WeightBP
		}
	}
	ids := unitIDs(p.UnitPrefix, in.Units)
	assigned := map[string]string{}
	var enrolled, treatment []int
	for _, pct := range in.PercentsBP {
		s := propSnapshot(t, in.Key, variants, experiment(in.Salt, pct, split))
		n, tr := 0, 0
		for _, id := range ids {
			res := Evaluate(s, in.Key, EvalContext{TargetingKey: id})
			if !res.Enrolled {
				continue
			}
			n++
			if res.Variant == "treatment" {
				tr++
			}
			if prev, ok := assigned[id]; ok && prev != res.Variant {
				t.Fatalf("%s is %q at one percentage and %q at %d bp", id, prev, res.Variant, pct)
			}
			assigned[id] = res.Variant
		}
		if !withinBinomial(tr, n, float64(treatmentBP)/fullBP) {
			t.Errorf("percent_bp %d: treatment %d of %d enrolled, want about %d%%", pct, tr, n, treatmentBP/100)
		}
		enrolled = append(enrolled, n)
		treatment = append(treatment, tr)
	}
	assertCounts(t, "independence enrolled", enrolled, in.Enrolled)
	assertCounts(t, "independence treatment", treatment, in.Treatment)
}

// TestOnlyRolloutAndExperimentEnroll sweeps the main snapshot's every change
// over a unit set and several context shapes: everyone, preview, no_unit,
// ineligible (and every other reason) never enroll.
func TestOnlyRolloutAndExperimentEnroll(t *testing.T) {
	v := loadVectors(t)
	s := v.snapshot(t, "main")
	var keys []string
	for _, c := range v.Cases {
		keys = append(keys, c.Key)
	}
	keys = append(keys, v.Invalid.Keys...)
	seen := map[Reason]bool{}
	for _, id := range append(unitIDs(v.Properties.UnitPrefix, 500), "", "alice", "bob") {
		for _, ec := range []EvalContext{
			{TargetingKey: id},
			{TargetingKey: id, Attributes: map[string]string{"plan": "paid", "account": id, "visitor": id}},
			{Attributes: map[string]string{"plan": "free", "account": id}},
		} {
			for _, key := range keys {
				res := Evaluate(s, key, ec)
				seen[res.Reason] = true
				if res.Enrolled != (res.Reason == ReasonRollout || res.Reason == ReasonExperiment) {
					t.Fatalf("%s/%+v: enrolled %v with reason %q", key, ec, res.Enrolled, res.Reason)
				}
				if res.Enrolled && res.Run == "" && key != "pipe-unit" && key != "visitor-zero" {
					t.Fatalf("%s/%+v: enrolled with no run", key, ec)
				}
			}
		}
	}
	for _, r := range []Reason{ReasonUnknown, ReasonDefault, ReasonInvalid, ReasonPreview, ReasonEveryone,
		ReasonNoUnit, ReasonIneligible, ReasonNotEnrolled, ReasonRollout, ReasonExperiment} {
		if !seen[r] {
			t.Errorf("the sweep never produced reason %q", r)
		}
	}
}

// TestPreviewNeverEnrolls lists 100 units in an experiment at 100%: each is
// previewed and none is measured, while every other unit enrolls.
func TestPreviewNeverEnrolls(t *testing.T) {
	ids := unitIDs(loadVectors(t).Properties.UnitPrefix, 1000)
	var preview []map[string]any
	for _, id := range ids[:100] {
		preview = append(preview, map[string]any{"unit_hash": PreviewHash("pv", id), "variant": "treatment"})
	}
	d := experiment("s_pv", fullBP, fiftyFifty)
	d["preview"] = preview
	s := propSnapshot(t, "pv", abVariants, d)
	for i, id := range ids {
		res := Evaluate(s, "pv", EvalContext{TargetingKey: id})
		if i < 100 && (res.Reason != ReasonPreview || res.Enrolled || res.Variant != "treatment") {
			t.Fatalf("%s listed but got %+v", id, res)
		}
		if i >= 100 && !res.Enrolled {
			t.Fatalf("%s unlisted at 100%% but got %+v", id, res)
		}
	}
}

func assertCounts(t *testing.T, what string, got, recorded []int) {
	t.Helper()
	if len(got) != len(recorded) {
		t.Errorf("%s: %v, recorded %v", what, got, recorded)
		return
	}
	for i := range got {
		if got[i] != recorded[i] {
			t.Errorf("%s: %v, recorded %v", what, got, recorded)
			return
		}
	}
}
