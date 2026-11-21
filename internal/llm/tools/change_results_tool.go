// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/rctx"
)

// change_results reads one experiment run's readout (ChangesService
// GetChangeReadout): per-arm conversion of the run's frozen metric, each
// treatment against the control, the sample-ratio check and the verdict, all
// computed by the control plane (forge pkg/experimentstats). A tool of its
// own rather than part of get_change because every call reads ClickHouse:
// reading a change should not cost a query, and nothing here should poll.

const ToolChangeResults = "change_results"

// ChangeReadoutArm is one variant's counts and conversion rate
// (intent-to-treat: a unit counts in the arm it was assigned).
type ChangeReadoutArm struct {
	Variant          string  `json:"variant"`
	WeightBP         int32   `json:"weight_bp"`
	Assigned         int64   `json:"assigned"`
	WindowComplete   int64   `json:"window_complete"`
	Conversions      int64   `json:"conversions"`
	Rate             float64 `json:"rate"`
	CILow            float64 `json:"ci_low"`
	CIHigh           float64 `json:"ci_high"`
	Pending          int64   `json:"pending"`
	ExpectedAssigned float64 `json:"expected_assigned"`
}

// ChangeReadoutComparison is one treatment against the control.
type ChangeReadoutComparison struct {
	Treatment   string   `json:"treatment"`
	Control     string   `json:"control"`
	AbsLift     float64  `json:"abs_lift"`
	RelLift     *float64 `json:"rel_lift,omitempty"`
	DiffCILow   float64  `json:"diff_ci_low"`
	DiffCIHigh  float64  `json:"diff_ci_high"`
	PValue      float64  `json:"p_value"`
	Alpha       float64  `json:"alpha"`
	Significant bool     `json:"significant"`
	Verdict     string   `json:"verdict"`
	Reason      string   `json:"reason,omitempty"`
}

// ChangeReadoutSRM is the sample-ratio mismatch check.
type ChangeReadoutSRM struct {
	PValue            float64  `json:"p_value"`
	ChiSquare         float64  `json:"chi_square"`
	Failed            bool     `json:"failed"`
	Skipped           bool     `json:"skipped"`
	UnplannedVariants []string `json:"unplanned_variants,omitempty"`
}

// ChangeReadout is one experiment run's results. Verdict is no_data,
// srm_failed, inconclusive, treatment_better or treatment_worse; Reason says
// why an inconclusive one is.
type ChangeReadout struct {
	ChangeID             string                    `json:"change_id"`
	Key                  string                    `json:"key"`
	RunID                string                    `json:"run_id"`
	Verdict              string                    `json:"verdict"`
	Reason               string                    `json:"reason,omitempty"`
	BestVariant          string                    `json:"best_variant,omitempty"`
	Arms                 []ChangeReadoutArm        `json:"arms"`
	Comparisons          []ChangeReadoutComparison `json:"comparisons"`
	SRM                  ChangeReadoutSRM          `json:"srm"`
	Conflicted           int64                     `json:"conflicted,omitempty"`
	Assigned             int64                     `json:"assigned"`
	Pending              int64                     `json:"pending"`
	UndeclaredVariants   []string                  `json:"undeclared_variants,omitempty"`
	ReadoutStart         time.Time                 `json:"readout_start"`
	ReadoutEnd           time.Time                 `json:"readout_end"`
	ReadoutEndReason     string                    `json:"readout_end_reason"`
	Final                bool                      `json:"final"`
	Metric               *ChangeMetric             `json:"metric,omitempty"`
	Alpha                float64                   `json:"alpha"`
	HorizonNote          string                    `json:"horizon_note,omitempty"`
	TruncatedByRetention bool                      `json:"truncated_by_retention,omitempty"`
}

type ChangeResultsParams struct {
	ChangeID string `json:"change_id" jsonschema:"required,description=A change id from list_changes. Never guess one."`
	RunID    string `json:"run_id,omitempty" jsonschema:"description=One of the change's runs. Default: its open run when that is an experiment; else its most recent experiment run."`
}

const changeResultsDescription = `Read the results of one Forge Change experiment run, as the control plane computes them: the verdict (no data, sample-ratio check failed, inconclusive and why, the new version better, or worse), each version's assigned people, people whose metric window has closed, conversions and conversion rate with its confidence interval, each treatment's lift against the control with its interval, the sample-ratio check, and the window the readout covers (still collecting or final). Every call runs a query, so do not poll it. A fixed-horizon test is decided once, at the pre-registered sample size: do not treat an early significant result as a decision.`

type changeResultsTool struct {
	repo       runCallerStore
	controller ChangeController
}

func NewChangeResultsTool(repo runCallerStore, controller ChangeController) Tool {
	return NewToolWrapper[ChangeResultsParams, ToolResponse](&changeResultsTool{repo: repo, controller: controller})
}

func (r *changeResultsTool) Name() string        { return ToolChangeResults }
func (r *changeResultsTool) Description() string { return changeResultsDescription }
func (r *changeResultsTool) RequiresPermission(ChangeResultsParams) (bool, error) {
	return false, nil
}

func (r *changeResultsTool) Execute(ctx *rctx.ToolContext, p ChangeResultsParams) (ToolResponse, error) {
	caller, failure := resolveRunCaller(ctx, r.repo)
	if failure != nil {
		return *failure, nil
	}
	if r.controller == nil {
		return NewTextErrorResponse(changesUnavailable), nil
	}
	id := strings.TrimSpace(p.ChangeID)
	if id == "" {
		return NewTextErrorResponse("change_id is required; list_changes shows them."), nil
	}
	readout, err := r.controller.GetChangeReadout(ctx.Context, caller.userID, id, strings.TrimSpace(p.RunID))
	if err != nil {
		return NewTextErrorResponse("Could not read the results of change " + id + ": " + describeChangeError(err, "results") + "."), nil
	}
	var b strings.Builder
	writeReadout(&b, readout)
	return WithResponseMetadata(NewTextResponse(strings.TrimRight(b.String(), "\n")), readout), nil
}

// readoutVerdicts are the verdicts in words.
var readoutVerdicts = map[string]string{
	"no_data":          "no data: no one has been assigned yet",
	"srm_failed":       "sample-ratio check FAILED: the versions' sizes do not match the planned split, so assignment or its recording is broken and no effect here can be trusted",
	"inconclusive":     "inconclusive",
	"treatment_better": "the new version is better",
	"treatment_worse":  "the new version is worse",
}

var readoutReasons = map[string]string{
	"below_min_sample":    "a version has fewer people than the minimum sample; keep the run going",
	"window_not_complete": "enough people are assigned, but too few have had their whole metric window",
	"interval_spans_zero": "no detectable difference",
}

var readoutEndReasons = map[string]string{
	"now":             "now (still collecting)",
	"left_experiment": "when the change left experiment mode within the run (final)",
	"run_ended":       "when the run ended (final)",
}

// writeReadout writes a readout for the model: the verdict first, then the
// numbers the verdict rests on.
func writeReadout(b *strings.Builder, r ChangeReadout) {
	verdict := orDefault(readoutVerdicts[r.Verdict], r.Verdict)
	fmt.Fprintf(b, "Results of %s, run %s: %s", r.Key, r.RunID, verdict)
	if r.Verdict == "treatment_better" && r.BestVariant != "" {
		fmt.Fprintf(b, " (best: %s)", r.BestVariant)
	}
	if why := readoutReasons[r.Reason]; why != "" {
		fmt.Fprintf(b, " — %s", why)
	}
	b.WriteString(".\n")
	window := fmt.Sprintf("Counted from %s to %s", fmtTime(r.ReadoutStart), orDefault(readoutEndReasons[r.ReadoutEndReason], r.ReadoutEndReason))
	if !r.ReadoutEnd.IsZero() {
		window += " (" + fmtTime(r.ReadoutEnd) + ")"
	}
	b.WriteString(window + ".")
	if r.Final {
		b.WriteString(" Final.")
	} else {
		b.WriteString(" Still collecting.")
	}
	b.WriteString("\n")
	if m := r.Metric; m != nil {
		fmt.Fprintf(b, "Metric: %s", m.Event)
		if m.Prop != "" {
			fmt.Fprintf(b, " where %s=%s", m.Prop, m.PropValue)
		}
		fmt.Fprintf(b, " within %dh; minimum %d per version; control %s; alpha %s.\n", m.WindowHours, m.MinSamplePerArm, orNone(m.ControlVariant), fmtNum(r.Alpha))
	}
	fmt.Fprintf(b, "Assigned %d, still in their window %d.\n", r.Assigned, r.Pending)
	for _, a := range r.Arms {
		fmt.Fprintf(b, "- %s: %d assigned, %d window complete, %d converted, rate %s [%s, %s], %d pending\n",
			a.Variant, a.Assigned, a.WindowComplete, a.Conversions, fmtPct(a.Rate), fmtPct(a.CILow), fmtPct(a.CIHigh), a.Pending)
	}
	for _, c := range r.Comparisons {
		fmt.Fprintf(b, "- %s vs %s: %s pts [%s, %s]", c.Treatment, c.Control, fmtPts(c.AbsLift), fmtPts(c.DiffCILow), fmtPts(c.DiffCIHigh))
		if c.RelLift != nil {
			fmt.Fprintf(b, ", relative %s", fmtSignedPct(*c.RelLift))
		}
		fmt.Fprintf(b, ", p=%s at alpha %s, %s", fmtNum(c.PValue), fmtNum(c.Alpha), orDefault(readoutVerdicts[c.Verdict], c.Verdict))
		if why := readoutReasons[c.Reason]; why != "" {
			fmt.Fprintf(b, " (%s)", why)
		}
		b.WriteString("\n")
	}
	switch {
	case r.SRM.Failed:
		fmt.Fprintf(b, "Sample-ratio check failed (p=%s)", fmtNum(r.SRM.PValue))
		if len(r.SRM.UnplannedVariants) > 0 {
			fmt.Fprintf(b, "; unplanned variants: %s", strings.Join(r.SRM.UnplannedVariants, ", "))
		}
		b.WriteString(". Do not read the lifts above as a result.\n")
	case r.SRM.Skipped:
		b.WriteString("Sample-ratio check skipped (too few people yet).\n")
	}
	if r.Conflicted > 0 {
		fmt.Fprintf(b, "%d people were assigned more than one version and are in no arm (a bug to find).\n", r.Conflicted)
	}
	if len(r.UndeclaredVariants) > 0 {
		fmt.Fprintf(b, "Assigned variants the split does not have: %s.\n", strings.Join(r.UndeclaredVariants, ", "))
	}
	if r.TruncatedByRetention {
		b.WriteString("The readout starts later than the run: product events older than their retention are gone, so early assignments are not counted.\n")
	}
	if r.HorizonNote != "" {
		b.WriteString(r.HorizonNote + "\n")
	}
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "the run's start"
	}
	return t.UTC().Format(time.RFC3339)
}

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'g', 3, 64) }

func fmtPct(f float64) string { return strconv.FormatFloat(f*100, 'f', 1, 64) + "%" }

func fmtPts(f float64) string {
	s := strconv.FormatFloat(f*100, 'f', 1, 64)
	if f >= 0 {
		s = "+" + s
	}
	return s
}

func fmtSignedPct(f float64) string { return fmtPts(f) + "%" }
