package releasehealth

// CHANGED OPS (runtime-context proposal, section C). The service-level
// findings say THAT a service got worse; a release's commit range says WHAT it
// changed. Joining the two — the ops (component methods, rpcs, endpoints) the
// range changed, each with its own before and after figures — names the op
// and the commit when one of them regressed: "notify.Send: error rate 0.2% ->
// 9.1%, changed in 3f2c1ab (#1234)". The caller chooses the ops (forge's op
// index maps the range's diff onto them) and reads their figures; this file
// only judges them, so a control plane that knows its own ops judges them by
// the same rule.
//
// The rule is the service rule made per op, with its own thresholds because
// one op's window is a thinner sample than a service's:
//
//   - Compared only with changed_op_min_calls (default 50, the default
//     min_requests: an op is held to the sample a service is) calls in BOTH
//     windows. A thinner op — one the release added has no before window — is
//     not a finding and not an error: the ops below it are named in one
//     not_evaluated entry ([NotEvaluatedChangedOps]).
//   - changed_op_errors: the after error rate minus the before one is above
//     changed_op_max_error_rate_increase (default 0.05, five percentage
//     points: five times the service-level bar, as a 50-call window moves a
//     point per half error), with at least changed_op_min_errors (default 5)
//     errors after the promotion, so two failed calls of a quiet op are not
//     evidence.
//   - changed_op_latency: the after p95 over the before p95 is above
//     changed_op_max_p95_ratio (default 2, against latency's 1.5: a p95 over
//     50 calls is close to the window's slowest call), with a before p95
//     above 0.
//
// Both warn by default (release.DefaultReleaseHealth's fail_on names
// neither): the op a diff changed is inferred — through the call graph, or
// whole packages as a fallback — and a thin sample of one op is noisier than
// its service, whose error_rate already fails a release. An env that trusts
// them lists them in fail_on. They never prove a release good.
//
// Their evidence is version-scoped. When the subject names a release (a
// hosted env's promotion), an op is judged only on after-window figures of
// that release's own service.version ([OpHealth.AfterVersion]): the calls an
// old version still in rotation made can push an op's error rate up while the
// release is healthy, and they are not the release's. Its baseline is one
// version too ([OpHealth.BeforeVersion]), never the judged release itself (a
// canary of it before the promotion is not a baseline): a before window that
// mixes the previous release with an older one still in rotation would
// otherwise compare the release against neither. Which version the baseline
// is, is the caller's choice, recorded on the op (`forge obs check` takes the
// previous release, else the version that served most of the op's before
// window). An op whose after figures are not the release's, or that has no
// baseline version, is not judged (its spans carry no service.version, or
// the caller did not read them per version); each case is named in a
// not_evaluated entry of its own. Being the release's own calls, a judged
// op's findings are attributed whatever its service's version share
// (attribution.go): only a window that is not the live release's moves them.
// A subject with no release (a local env: everything running is the judged
// code) has no version to scope by, and its ops are judged on all their spans.

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// Defaults of the changed-op thresholds (above).
const (
	DefaultChangedOpMinCalls             = 50
	DefaultChangedOpMaxErrorRateIncrease = 0.05
	DefaultChangedOpMinErrors            = 5
	DefaultChangedOpMaxP95Ratio          = 2.0
)

// ChangedOpThresholds are the changed-op threshold names. A caller that
// sends --threshold overrides to a control plane that may predate them (which
// refuses an unknown name) keeps these back and applies them with
// [WithChangedOps].
var ChangedOpThresholds = []string{ThresholdChangedOpMinCalls, ThresholdChangedOpMaxErrorRateIncrease, ThresholdChangedOpMinErrors, ThresholdChangedOpMaxP95Ratio}

// Op kinds ([OpHealth.Kind]).
const (
	// OpKindComponent is a component method: an INTERNAL span named
	// <pkg>.<Method>.
	OpKindComponent = "component"
	// OpKindRPC is a Connect rpc: a SERVER span named <proto pkg>.<Svc>/<Rpc>.
	OpKindRPC = "rpc"
	// OpKindEndpoint is any other server span: an HTTP route.
	OpKindEndpoint = "endpoint"
)

// NotEvaluatedChangedOps prefixes the not_evaluated entries that name the
// changed ops not judged: one for those with no after figures of the judged
// release, one for those too thin to compare.
const NotEvaluatedChangedOps = "changed_op:"

// OpCommit is a commit of the release's range that changed an op.
type OpCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject,omitempty"`
	// PR is the commit's pull request number, 0 when unknown; a caller that
	// leaves it 0 has it read from a subject ending "(#123)" ([ParsePR]).
	PR int `json:"pr,omitempty"`
}

// OpHealth is one changed op's before and after.
type OpHealth struct {
	// Op is the op's span name: <pkg>.<Method> for a component,
	// <proto pkg>.<Svc>/<Rpc> for an rpc.
	Op      string `json:"op"`
	Service string `json:"service,omitempty"`
	// Kind is OpKindComponent, OpKindRPC or OpKindEndpoint.
	Kind string `json:"kind,omitempty"`
	// Before and After are the op's figures; Requests counts its calls.
	Before WindowStats `json:"before"`
	After  WindowStats `json:"after"`
	// AfterVersion is the service.version the After figures are restricted
	// to; "" is every version's spans. When the report's subject names a
	// release, an op is judged only when AfterVersion is that release (the
	// rule above).
	AfterVersion string `json:"after_version,omitempty"`
	// BeforeVersion is the service.version the Before figures are
	// restricted to: the baseline the op is compared against, chosen by the
	// caller; "" is every version's spans. When the report's subject names
	// a release, an op is judged only when BeforeVersion is set and is not
	// that release (the rule above).
	BeforeVersion string `json:"before_version,omitempty"`
	// Commits are the range's commits that changed the op, newest first.
	Commits []OpCommit `json:"commits,omitempty"`
}

// AfterErrors is the errors the op returned in the after window: what
// changed_op_min_errors is held against.
func (o OpHealth) AfterErrors() int64 {
	return int64(math.Round(o.After.ErrorRate * float64(o.After.Requests)))
}

// Newest is the newest commit that changed the op, with its PR filled in
// from its subject when the caller did not set it.
func (o OpHealth) Newest() (OpCommit, bool) {
	if len(o.Commits) == 0 {
		return OpCommit{}, false
	}
	c := o.Commits[0]
	if c.PR == 0 {
		c.PR = ParsePR(c.Subject)
	}
	return c, true
}

// ParsePR is the pull request number a squash-merged commit's subject ends
// with ("fix(api): retry on reconnect (#1234)"), or 0.
func ParsePR(subject string) int {
	s := strings.TrimSpace(subject)
	if !strings.HasSuffix(s, ")") {
		return 0
	}
	i := strings.LastIndex(s, "(#")
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(s[i+2 : len(s)-1])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// changedOpLimits are the changed-op thresholds, each filled in.
type changedOpLimits struct {
	minCalls    int
	maxIncrease float64
	minErrors   int
	maxRatio    float64
}

// changedOps is t's changed-op thresholds, a zero one read as its default.
func (t Thresholds) changedOps() changedOpLimits {
	l := changedOpLimits{t.ChangedOpMinCalls, t.ChangedOpMaxErrorRateIncrease, t.ChangedOpMinErrors, t.ChangedOpMaxP95Ratio}
	if l.minCalls <= 0 {
		l.minCalls = DefaultChangedOpMinCalls
	}
	if l.maxIncrease <= 0 {
		l.maxIncrease = DefaultChangedOpMaxErrorRateIncrease
	}
	if l.minErrors <= 0 {
		l.minErrors = DefaultChangedOpMinErrors
	}
	if l.maxRatio <= 1 {
		l.maxRatio = DefaultChangedOpMaxP95Ratio
	}
	return l
}

// changedOpFindings judges ops under l for the judged subject s: the
// findings (no policy yet), and the not_evaluated entries of the ops with no
// after figures of s's release, of those with no baseline version, and of
// those too thin to compare (none when every op was compared). A subject
// with no release scopes nothing.
func changedOpFindings(ops []OpHealth, l changedOpLimits, s Subject) ([]Finding, []string) {
	rel := s.Release
	var out []Finding
	var thin, noAfter, noBefore []string
	floor := int64(l.minCalls)
	for _, o := range ops {
		if rel != "" && o.AfterVersion != rel {
			noAfter = append(noAfter, o.Op)
			continue
		}
		if rel != "" && (o.BeforeVersion == "" || o.BeforeVersion == rel) {
			noBefore = append(noBefore, o.Op)
			continue
		}
		if o.Before.Requests < floor || o.After.Requests < floor {
			thin = append(thin, o.Op)
			continue
		}
		label := o.Op
		if o.Service != "" {
			label += " (" + o.Service + ")"
		}
		finding := func(code string, value, threshold float64, figures string) Finding {
			f := Finding{Code: code, Service: o.Service, Ref: o.Op, Op: o.Op, Value: value, Threshold: threshold,
				Message: label + ": " + figures}
			if c, ok := o.Newest(); ok {
				f.Commit, f.PR = c.SHA, c.PR
				// SHA "" is a working tree's uncommitted changes, which a
				// local env's hot reload runs (forge's op index names them so).
				if c.SHA == "" {
					f.Message += "; changed in uncommitted changes"
				} else {
					f.Message += "; changed in " + shortSHA(c.SHA)
				}
				if c.PR > 0 {
					f.Message += fmt.Sprintf(" (#%d)", c.PR)
				}
			}
			return f
		}
		inc, errs := o.After.ErrorRate-o.Before.ErrorRate, o.AfterErrors()
		if inc > l.maxIncrease && errs >= int64(l.minErrors) {
			out = append(out, finding(release.HealthChangedOpErrors, inc, l.maxIncrease,
				fmt.Sprintf("error rate %s -> %s (+%s, limit +%s; %d errors in %d calls)",
					pct(o.Before.ErrorRate), pct(o.After.ErrorRate), pct(inc), pct(l.maxIncrease), errs, o.After.Requests)))
		}
		if o.Before.P95Ms > 0 {
			if ratio := o.After.P95Ms / o.Before.P95Ms; ratio > l.maxRatio {
				out = append(out, finding(release.HealthChangedOpLatency, ratio, l.maxRatio,
					fmt.Sprintf("p95 %s -> %s (%.2fx, limit %.2gx)", ms(o.Before.P95Ms), ms(o.After.P95Ms), ratio, l.maxRatio)))
			}
		}
	}
	var entries []string
	if len(noAfter) > 0 {
		entries = append(entries, unversionedOpsEntry(noAfter, rel))
	}
	if len(noBefore) > 0 {
		entries = append(entries, noBaselineOpsEntry(noBefore, s))
	}
	if len(thin) > 0 {
		entries = append(entries, thinOpsEntry(thin, l.minCalls))
	}
	return out, entries
}

// thinOpsEntry is the one not_evaluated entry naming the ops below
// changed_op_min_calls, sorted, the first few by name: a range that changed
// fifty ops must not bury the report's other entries.
func thinOpsEntry(names []string, minCalls int) string {
	names, list := fewNames(names)
	return fmt.Sprintf("%s %d op(s) below changed_op_min_calls (%d calls in each window): %s",
		NotEvaluatedChangedOps, len(names), minCalls, list)
}

// unversionedOpsEntry is the one not_evaluated entry naming the ops with no
// after-window figures of the judged release rel, in thinOpsEntry's style.
func unversionedOpsEntry(names []string, rel string) string {
	names, list := fewNames(names)
	return fmt.Sprintf("%s %d op(s) have no after-window figures of %s (their spans carry no service.version, or none was read per version): %s",
		NotEvaluatedChangedOps, len(names), rel, list)
}

// noBaselineOpsEntry is the one not_evaluated entry naming the ops with no
// before-window figures of a version other than the judged release, in
// thinOpsEntry's style.
func noBaselineOpsEntry(names []string, s Subject) string {
	names, list := fewNames(names)
	of := "a version before " + s.Release
	if s.PreviousRelease != "" && s.PreviousRelease != s.Release {
		of = "the previous release " + s.PreviousRelease
	}
	return fmt.Sprintf("%s %d op(s) have no before-window figures of %s to compare with (none of their before-window spans carries a service.version other than %s, or none was read per version): %s",
		NotEvaluatedChangedOps, len(names), of, s.Release, list)
}

// fewNames is names sorted and deduplicated, and the first few of them
// joined, the rest counted.
func fewNames(names []string) ([]string, string) {
	names = slices.Compact(slices.Sorted(slices.Values(names)))
	const shown = 3
	list := names
	if len(list) > shown {
		list = append(slices.Clone(list[:shown]), fmt.Sprintf("%d more", len(names)-shown))
	}
	return names, strings.Join(list, ", ")
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// WithChangedOps judges the ops a release changed against a judged report:
// the way the changed-op rule reaches a control plane's answer that predates
// it (`forge obs check` reads the ops itself). It drops the report's
// changed-op findings and its changed-op not_evaluated entry and re-derives
// both from ops, so it is idempotent and holds a newer control plane's answer
// to the same rule; nil ops re-judges the report's own ChangedOps. The
// thresholds are the report's, except that an override (the request's
// --threshold pairs, by name; nil for none) wins; a report judged before they
// existed is held to the defaults, and lists them. Each finding's policy
// follows the report's fail_on, it is attributed as [Attribute] attributes
// one, and the verdict is recomputed as [Rejudge] recomputes it, so it can
// improve when a changed-op finding is gone.
func WithChangedOps(r Report, ops []OpHealth, overrides map[string]string) Report {
	if ops == nil {
		ops = r.ChangedOps
	}
	ths, l := changedOpThresholdsOf(r.Thresholds, overrides)
	out := r
	out.Thresholds = ths
	out.ChangedOps = ops
	isChangedOp := func(f Finding) bool { return slices.Contains(release.ChangedOpFindings, f.Code) }
	out.Findings = slices.DeleteFunc(slices.Clone(r.Findings), isChangedOp)
	if out.Findings == nil {
		out.Findings = []Finding{}
	}
	out.NotEvaluated = slices.DeleteFunc(slices.Clone(r.NotEvaluated), func(e string) bool { return strings.HasPrefix(e, NotEvaluatedChangedOps) })

	fs, thin := changedOpFindings(ops, l, r.Subject)
	failOn := failOnOf(ths)
	for _, f := range fs {
		f.Policy = policyOf(f.Code, failOn)
		out.Findings = append(out.Findings, f)
		out.attribute(len(out.Findings) - 1)
	}
	out.NotEvaluated = withNotEvaluated(out.NotEvaluated, thin)
	if len(out.NotEvaluated) == 0 {
		out.NotEvaluated = nil
	}
	sortFindings(out.Findings)
	if slices.Equal(out.Findings, r.Findings) && out.RolloutIncomplete == r.RolloutIncomplete {
		return out // nothing the verdict rests on changed: the verdict as judged
	}
	out.Verdict = verdictOf(out.Findings, out.AfterComplete, out.Services, gaugeOf(out.Thresholds), out.unproven())
	return out
}

// changedOpThresholdsOf reads the changed-op thresholds from a judged
// report's list, each override that is valid winning (tagged SourceRequest),
// and returns the list with every changed-op row present: one the report
// lacks is added with its default before fail_on, where [Thresholds.List]
// puts it.
func changedOpThresholdsOf(ths []Threshold, overrides map[string]string) ([]Threshold, changedOpLimits) {
	l := changedOpLimits{}
	out := slices.Clone(ths)
	value := func(name string, def string) {
		v, src := def, SourceDefault
		i := slices.IndexFunc(out, func(t Threshold) bool { return t.Name == name })
		if i >= 0 && validChangedOp(name, out[i].Value) {
			v, src = out[i].Value, out[i].Source
		}
		if o, ok := overrides[name]; ok && validChangedOp(name, strings.TrimSpace(o)) {
			v, src = strings.TrimSpace(o), SourceRequest
		}
		th := Threshold{name, v, src}
		switch {
		case i >= 0:
			out[i] = th
		default:
			at := slices.IndexFunc(out, func(t Threshold) bool { return t.Name == ThresholdFailOn })
			if at < 0 {
				at = len(out)
			}
			out = slices.Insert(out, at, th)
		}
		switch name {
		case ThresholdChangedOpMinCalls:
			l.minCalls, _ = strconv.Atoi(v)
		case ThresholdChangedOpMinErrors:
			l.minErrors, _ = strconv.Atoi(v)
		case ThresholdChangedOpMaxErrorRateIncrease:
			l.maxIncrease, _ = strconv.ParseFloat(v, 64)
		case ThresholdChangedOpMaxP95Ratio:
			l.maxRatio, _ = strconv.ParseFloat(v, 64)
		}
	}
	value(ThresholdChangedOpMinCalls, strconv.Itoa(DefaultChangedOpMinCalls))
	value(ThresholdChangedOpMaxErrorRateIncrease, strconv.FormatFloat(DefaultChangedOpMaxErrorRateIncrease, 'g', -1, 64))
	value(ThresholdChangedOpMinErrors, strconv.Itoa(DefaultChangedOpMinErrors))
	value(ThresholdChangedOpMaxP95Ratio, strconv.FormatFloat(DefaultChangedOpMaxP95Ratio, 'g', -1, 64))
	return out, l
}

// validChangedOp reports whether v is in range for changed-op threshold
// name, by [Thresholds.validate]'s bounds.
func validChangedOp(name, v string) bool {
	switch name {
	case ThresholdChangedOpMinCalls, ThresholdChangedOpMinErrors:
		n, err := strconv.Atoi(v)
		return err == nil && n >= 1
	case ThresholdChangedOpMaxErrorRateIncrease:
		f, err := strconv.ParseFloat(v, 64)
		return err == nil && f > 0 && f <= 1
	case ThresholdChangedOpMaxP95Ratio:
		f, err := strconv.ParseFloat(v, 64)
		return err == nil && f > 1
	}
	return false
}

// failOnOf is the fail-on set a judged report lists; the default policy's
// when it lists none.
func failOnOf(ths []Threshold) []string {
	for _, th := range ths {
		if th.Name == ThresholdFailOn {
			var out []string
			for _, c := range strings.Split(th.Value, ",") {
				if c = strings.TrimSpace(c); c != "" {
					out = append(out, c)
				}
			}
			return out
		}
	}
	return release.DefaultReleaseHealth().FailOn
}
