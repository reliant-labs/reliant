package obsdash

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// AlertDefaultsVersion identifies the serialized shape of AlertDefaultsFile.
// v2 dropped input.services: each service's entry is its alertServicePart's Alert
// (see alertServicePart for why), and ReadAlertInput puts them back.
const AlertDefaultsVersion = "forge.alert-defaults/v3"

// AlertDefaultsFile is the project-relative file `forge generate` writes: the
// project metadata the default alert rules are derived from, plus the catalog
// of defaults with their tunable parameters. The per-service metadata is in
// each alertServicePart. `forge env shape` reads both back (ReadAlertInput) to
// resolve the defaults for one env (the SQL names the env, so a default is
// rendered per env, not once per project). It sits beside the dashboard spec,
// not in the dashboards directory the HyperDX provisioner scans.
const AlertDefaultsFile = "deploy/observability/alert-defaults_gen.json"

// Alert severities, the values of the `severity` label.
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// AlertService is one Connect service the defaults watch. RPCService is the
// procedure prefix the RPC interceptors label metrics with:
// "<package>.<ProtoService>", the second segment of "/orders.v1.OrdersService/GetOrder".
type AlertService struct {
	Name       string `json:"name"`
	RPCService string `json:"rpc_service"`
}

// AlertInput is what the defaults are derived from.
type AlertInput struct {
	Project string `json:"project"`
	// Services are the project's services that have at least one rpc. The
	// generated file leaves them out (each is in its alertServicePart).
	Services []AlertService `json:"services,omitempty"`
	// FrontendServices are the service.names the project's web frontends'
	// browser SDK reports (naming.FrontendServiceName: "<project>-<frontend>",
	// what each otel_gen.ts bakes as SERVICE_NAME), empty when the project has
	// no web frontend. The frontend defaults fire per service.name.
	FrontendServices []string `json:"frontend_services,omitempty"`
	// UptimeChecks are the names of the env's uptime checks (the release
	// shape's uptime_checks). They are a fact about one env, not the project,
	// so the generated file never records them: the resolver sets them per
	// env. The uptime defaults apply only to an env that has checks, and read
	// only those checks' series, so a check an env opts out of cannot alert.
	UptimeChecks []string `json:"-"`
}

// AlertInputOf derives the defaults' input from the same Input the dashboards
// are built from, so the two can never disagree about what a project has.
func AlertInputOf(in Input) AlertInput {
	out := AlertInput{Project: in.Project, Services: []AlertService{}}
	for _, s := range in.Services {
		if len(s.Methods) == 0 {
			continue
		}
		out.Services = append(out.Services, AlertService{Name: s.Name, RPCService: s.Package + "." + s.ProtoService})
	}
	sort.Slice(out.Services, func(i, j int) bool { return out.Services[i].Name < out.Services[j].Name })
	out.FrontendServices = in.frontendServiceNames()
	return out
}

// AlertParam is one numeric knob of a default's query.
type AlertParam struct {
	Name    string  `json:"name"`
	Default float64 `json:"default"`
	Doc     string  `json:"doc"`
}

// AlertDefaultInfo is a default as the generated file records it.
type AlertDefaultInfo struct {
	ID       string `json:"id"`
	Alert    string `json:"alert"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
	For      string `json:"for"`
	Interval string `json:"interval"`
	// Signal is where the default reads: metrics, traces, metadata (the node
	// agent's customer metadata) or platform (the control plane's own events).
	Signal string `json:"signal"`
	// Requires names the observability category whose opt-out leaves the
	// default nothing to read ("metadata"). Empty = always applicable.
	Requires string `json:"requires,omitempty"`
	// AlertNames are further rule names the default also takes, beyond Alert.
	AlertNames []string     `json:"alert_names,omitempty"`
	Params     []AlertParam `json:"params"`
}

// AlertDefaultsDoc is the serialized AlertDefaultsFile.
type AlertDefaultsDoc struct {
	Version  string             `json:"version"`
	Input    AlertInput         `json:"input"`
	Defaults []AlertDefaultInfo `json:"defaults"`
}

// AlertTuning is how an env adjusts one default (forge.AlertDefault).
type AlertTuning struct {
	Enabled bool
	Labels  map[string]string
	For     string
	Params  map[string]float64
}

// AlertRule is one resolved default: a rule ready for an env's shape.
type AlertRule struct {
	ID          string
	Alert       string
	Expr        string
	For         string
	Interval    string
	Labels      map[string]string
	Annotations map[string]string
}

type alertDef struct {
	AlertDefaultInfo
	annotations map[string]string
	applies     func(AlertInput) bool
	// sql is nil for a platform default: the control plane evaluates those from
	// events it owns (see platformExpr), so forge records only how the env
	// tuned them.
	sql func(env string, in AlertInput, p map[string]float64) string
	// platform lists the rules a platform default stands for.
	platform []platformRule
	// needs is a per-env gate on top of applies: applies decides whether the
	// project has anything the default could watch (and so whether the
	// generated catalog lists it), needs whether this env does. Nil = always.
	needs func(AlertInput) bool
	// levels, when set, make one default several rules, one per severity:
	// the sql runs once per level with the param pCritical set to 1 for a
	// critical level and 0 otherwise, and decides its band from that. The
	// levels share the default's params, so a threshold is tuned once.
	levels []alertLevel
}

// alertLevel is one severity of a default with levels.
type alertLevel struct {
	alert, severity string
	critical        bool
}

type platformRule struct{ key, alert, severity string }

const (
	pWindow      = "window_minutes"
	pMinRequests = "min_requests"
	pMinSamples  = "min_samples"
	// pCritical is the synthetic param a default with levels is given: 1 for
	// the rule of its critical level, 0 for the others. It is not tunable.
	pCritical = "critical_level"

	pFailures         = "failures"
	pCriticalFailures = "critical_failures"
	pWarnDays         = "warn_days"
	pCriticalDays     = "critical_days"
)

// Platform default ids: alerts the control plane raises from events it owns (a
// failed convergence, a quota state). They are tunable in forge.Alerts.defaults
// like any other id, and recorded in the shape's platform_alerts.
const (
	PlatformDeployFailed = "deploy-failed"
	PlatformQuota        = "observability-quota"
	// The issue alerts need the issue registry (new / regressed are state
	// transitions, and a spike is measured per issue), which the control plane
	// owns. They cover every issue the env has, browser or server.
	PlatformNewIssue       = "new-issue"
	PlatformIssueRegressed = "issue-regressed"
	PlatformIssueSpike     = "issue-spike"
	// PlatformReleaseRegressed fires when a promotion's after-window closes
	// and the release-health verdict is FAIL: the env's release_health.fail_on
	// findings, evaluated by the control plane with the same rule as
	// `forge obs check`. It stays silent on PASS and on INCONCLUSIVE.
	PlatformReleaseRegressed = "release-regressed"
	// PlatformServiceAnomaly fires when a service's request rate, error rate
	// or p95 leaves its seasonal baseline: the same hour of the week over the
	// previous 4 weeks (obsquery.EvalAnomalies, over the rpc.server.* metrics,
	// never spans). The control plane evaluates it; its limits (4 MADs, 50
	// requests, a 1 point error-rate rise or a 1.5x p95 rise, 7 days of
	// history) are obsquery.DefaultAnomalyParams, not tunable params. The
	// alert is one per service and release (labels: service, env, release,
	// dedupe_key), with every signal that moved a finding in its why payload,
	// so a summary can quote no signal label.
	PlatformServiceAnomaly = "service-anomaly"
)

// hasIssues reports whether the env has anything that can raise an issue.
func hasIssues(in AlertInput) bool { return len(in.Services) > 0 || hasFrontends(in) }

func hasFrontends(in AlertInput) bool { return len(in.FrontendServices) > 0 }

func hasServices(in AlertInput) bool { return len(in.Services) > 0 }

func hasUptimeChecks(in AlertInput) bool { return len(in.UptimeChecks) > 0 }

// The defaults. Where the signal is rpc traffic they read the RPC server
// METRICS (rpc.server.calls / .errors / .duration, pkg/observe), never spans:
// metrics are not sampled, while the gateway's tail sampler keeps all error
// traces and a fraction of the rest, so a span count of healthy requests
// reports a fraction of the traffic and a ratio of the two is wrong. The one
// span-based default (frontend errors) counts ERROR spans only, which the tail
// sampler always keeps, and compares them to themselves, so sampling cancels.
// The gateway writes no sample-rate attribute, so there is nothing to weight a
// non-error span count by: see the observability/alerts skill.
var alertDefs = []alertDef{
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: "error-rate", Alert: "ServiceErrorRateHigh", Title: "Service error rate above 5%",
			Severity: SeverityCritical, For: "5m", Interval: "1m", Signal: "metrics",
			Params: []AlertParam{
				{"threshold", 0.05, "error ratio (0..1) that fires"},
				{pWindow, 5, "minutes the ratio is computed over"},
				{pMinRequests, 20, "fewer requests than this in the window never fire (a ratio of 1 call is noise)"},
			},
		},
		annotations: map[string]string{"summary": "{{ $labels.service }} fails {{ $value }} of its requests (server errors / all requests)"},
		applies:     func(in AlertInput) bool { return len(in.Services) > 0 },
		sql:         errorRateSQL,
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: "p95-latency", Alert: "ServiceLatencyP95High", Title: "Service p95 latency above 1s",
			Severity: SeverityWarning, For: "10m", Interval: "1m", Signal: "metrics",
			Params: []AlertParam{
				{"threshold_seconds", 1, "p95 latency in seconds that fires"},
				{pWindow, 5, "minutes the percentile is computed over"},
				{pMinRequests, 20, "fewer requests than this in the window never fire"},
			},
		},
		annotations: map[string]string{"summary": "{{ $labels.service }} p95 latency is {{ $value }}s"},
		applies:     func(in AlertInput) bool { return len(in.Services) > 0 },
		sql:         latencySQL,
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: "service-silent", Alert: "ServiceSilent", Title: "A service with steady traffic went quiet",
			Severity: SeverityWarning, For: "0s", Interval: "1m", Signal: "metrics",
			Params: []AlertParam{
				{"silent_minutes", 10, "minutes without a request before it fires"},
				{"min_active_hours", 18, "of the previous 24h, the hours the service must have served requests in to count as steady (a quiet staging service never pages)"},
			},
		},
		annotations: map[string]string{"summary": "{{ $labels.service }} has served no requests for {{ $value }}s, after serving traffic through most of the last day"},
		applies:     func(in AlertInput) bool { return len(in.Services) > 0 },
		sql:         silentSQL,
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: "frontend-js-errors", Alert: "FrontendJSErrorSpike", Title: "Frontend JS errors spiking against the 1h baseline",
			Severity: SeverityWarning, For: "10m", Interval: "1m", Signal: "traces",
			Params: []AlertParam{
				{"spike_factor", 3, "times the baseline rate that fires"},
				{pWindow, 10, "minutes of the current window"},
				{"baseline_minutes", 60, "minutes before the window that set the baseline rate"},
				{"min_errors", 10, "fewer errors than this in the window never fire"},
			},
		},
		annotations: map[string]string{"summary": "{{ $labels.service }} reported {{ $value }} JS errors in the current window, over the spike factor times its baseline"},
		applies:     hasFrontends,
		sql:         frontendErrorsSQL,
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: "frontend-lcp-poor", Alert: "FrontendLCPPoor", Title: "Frontend Largest Contentful Paint p75 is poor",
			Severity: SeverityInfo, For: "10m", Interval: "5m", Signal: "traces",
			Params: []AlertParam{
				{"threshold_ms", 4000, "p75 LCP in milliseconds that fires (Core Web Vitals call above 4000 poor)"},
				{pWindow, 30, "minutes the percentile is computed over"},
				{pMinSamples, 100, "fewer LCP measurements than this in the window never fire"},
			},
		},
		annotations: map[string]string{"summary": "{{ $labels.service }} p75 LCP is {{ $value }} ms, over the poor threshold"},
		applies:     hasFrontends,
		sql:         frontendLCPSQL,
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: PlatformNewIssue, Alert: "NewIssue", Title: "A new issue appeared",
			Severity: SeverityWarning, For: "0s", Interval: "1m", Signal: "platform",
			Params: []AlertParam{},
		},
		annotations: map[string]string{"summary": "New issue in {{ $labels.service }} ({{ $labels.env }}): {{ $labels.title }}. Details: {{ $labels.console_url }}"},
		applies:     hasIssues,
		platform:    []platformRule{{PlatformNewIssue, "NewIssue", SeverityWarning}},
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: PlatformIssueRegressed, Alert: "IssueRegressed", Title: "A resolved issue came back in a later release",
			Severity: SeverityWarning, For: "0s", Interval: "1m", Signal: "platform",
			Params: []AlertParam{},
		},
		annotations: map[string]string{"summary": "A resolved issue came back in {{ $labels.service }} ({{ $labels.env }}): {{ $labels.title }}. Details: {{ $labels.console_url }}"},
		applies:     hasIssues,
		platform:    []platformRule{{PlatformIssueRegressed, "IssueRegressed", SeverityWarning}},
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: PlatformIssueSpike, Alert: "IssueSpike", Title: "An issue's error rate is spiking against its previous day",
			Severity: SeverityWarning, For: "0s", Interval: "5m", Signal: "platform",
			Params: []AlertParam{},
		},
		annotations: map[string]string{"summary": "Issue spiking in {{ $labels.service }} ({{ $labels.env }}): {{ $labels.title }}, {{ $value }} events/hour, over 5 times its rate the day before. Details: {{ $labels.console_url }}"},
		applies:     hasIssues,
		platform:    []platformRule{{PlatformIssueSpike, "IssueSpike", SeverityWarning}},
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: PlatformReleaseRegressed, Alert: "ReleaseRegressed", Title: "A promoted release made the env worse once its health window closed",
			Severity: SeverityWarning, For: "0s", Interval: "1m", Signal: "platform",
			Params: []AlertParam{},
		},
		annotations: map[string]string{"summary": "Release {{ $labels.release }} regressed {{ $labels.env }}: {{ $value }} failing release-health findings after its window closed. Check it: forge obs check {{ $labels.env }} --release {{ $labels.release }}"},
		applies:     func(in AlertInput) bool { return len(in.Services) > 0 },
		platform:    []platformRule{{PlatformReleaseRegressed, "ReleaseRegressed", SeverityWarning}},
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: PlatformServiceAnomaly, Alert: "ServiceAnomaly", Title: "A service's traffic, errors or latency left what is usual for this hour of the week",
			Severity: SeverityWarning, For: "0s", Interval: "5m", Signal: "platform",
			Params: []AlertParam{},
		},
		annotations: map[string]string{"summary": "{{ $labels.service }} left what is usual for this hour of the week ({{ $labels.env }}). What moved, against what, and the evidence are in the alert's why payload: forge obs alerts --json"},
		applies:     hasServices,
		platform:    []platformRule{{PlatformServiceAnomaly, "ServiceAnomaly", SeverityWarning}},
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: "uptime-check-failing", Alert: "UptimeCheckFailing", AlertNames: []string{"UptimeCheckDown"},
			Title:    "An uptime check failed several probes in a row (warning), then many (critical)",
			Severity: SeverityWarning, For: "0s", Interval: "1m", Signal: "metrics", Requires: "metrics",
			Params: []AlertParam{
				{pFailures, 3, "consecutive failed probes that raise UptimeCheckFailing (warning)"},
				{pCriticalFailures, 10, "consecutive failed probes that raise UptimeCheckDown (critical) instead"},
				{pWindow, 30, "minutes of probes considered; keep it above failures times the check's interval"},
			},
		},
		annotations: map[string]string{"summary": "{{ $labels.check }} has failed {{ $value }} probes in a row"},
		applies:     func(AlertInput) bool { return true },
		needs:       hasUptimeChecks,
		levels:      []alertLevel{{"UptimeCheckFailing", SeverityWarning, false}, {"UptimeCheckDown", SeverityCritical, true}},
		sql:         uptimeFailingSQL,
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: "certificate-expiring", Alert: "CertificateExpiring", AlertNames: []string{"CertificateExpiringSoon"},
			Title:    "A probed certificate expires within 14 days (warning) or 3 days (critical)",
			Severity: SeverityWarning, For: "10m", Interval: "5m", Signal: "metrics", Requires: "metrics",
			Params: []AlertParam{
				{pWarnDays, 14, "days to expiry at or below which CertificateExpiring (warning) fires"},
				{pCriticalDays, 3, "days to expiry at or below which CertificateExpiringSoon (critical) fires instead, an expired certificate included"},
				{pWindow, 30, "minutes of probes considered"},
			},
		},
		annotations: map[string]string{"summary": "The certificate {{ $labels.check }} serves expires in {{ $value }} days"},
		applies:     func(AlertInput) bool { return true },
		needs:       hasUptimeChecks,
		levels:      []alertLevel{{"CertificateExpiring", SeverityWarning, false}, {"CertificateExpiringSoon", SeverityCritical, true}},
		sql:         certificateExpiringSQL,
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: "crash-loop", Alert: "WorkloadCrashLooping", Title: "A container keeps restarting",
			Severity: SeverityCritical, For: "0s", Interval: "1m", Signal: "metadata", Requires: "metadata",
			Params: []AlertParam{
				{"min_restarts", 3, "restarts in the window that fire (the agent reports restarts every 5 minutes)"},
				{pWindow, 10, "minutes the restart growth is measured over"},
			},
		},
		annotations: map[string]string{"summary": "{{ $labels.workload }} is crash-looping: {{ $value }} restarts (or back-off events) in the last window"},
		applies:     hasServices,
		sql:         crashLoopSQL,
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: "oom-killed", Alert: "WorkloadOOMKilled", Title: "A container was killed for running out of memory",
			Severity: SeverityCritical, For: "0s", Interval: "1m", Signal: "metadata", Requires: "metadata",
			Params: []AlertParam{
				{pWindow, 10, "minutes a fresh OOM kill stays visible (a container that restarted in the window and last died of OOMKilled)"},
			},
		},
		annotations: map[string]string{"summary": "{{ $labels.workload }} was OOM-killed"},
		applies:     hasServices,
		sql:         oomKilledSQL,
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: "memory-near-limit", Alert: "WorkloadMemoryNearLimit", Title: "A container is close to its memory limit",
			Severity: SeverityWarning, For: "10m", Interval: "1m", Signal: "metadata", Requires: "metadata",
			Params: []AlertParam{
				{"threshold", 0.9, "memory / limit ratio (0..1) that fires"},
				{pWindow, 5, "minutes the ratio is averaged over, so a spike does not fire"},
			},
		},
		annotations: map[string]string{"summary": "{{ $labels.workload }}/{{ $labels.container }} uses {{ $value }} of its memory limit"},
		applies:     hasServices,
		sql:         memoryNearLimitSQL,
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: "image-pull-failed", Alert: "WorkloadImagePullFailed", Title: "A workload cannot pull its image",
			Severity: SeverityWarning, For: "5m", Interval: "1m", Signal: "metadata", Requires: "metadata",
			Params: []AlertParam{
				{"min_events", 1, "pull-failure events in the window that fire"},
				{pWindow, 15, "minutes of events considered"},
			},
		},
		annotations: map[string]string{"summary": "{{ $labels.workload }} cannot pull its image ({{ $value }} failure events)"},
		applies:     hasServices,
		sql:         imagePullFailedSQL,
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: "evicted", Alert: "WorkloadEvicted", Title: "A pod was evicted",
			Severity: SeverityWarning, For: "0s", Interval: "1m", Signal: "metadata", Requires: "metadata",
			Params: []AlertParam{
				{"min_events", 1, "eviction events in the window that fire"},
				{pWindow, 15, "minutes of events considered"},
			},
		},
		annotations: map[string]string{"summary": "{{ $labels.workload }} had {{ $value }} pod evictions"},
		applies:     hasServices,
		sql:         evictedSQL,
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: PlatformDeployFailed, Alert: "DeployFailed", Title: "A promoted release did not converge",
			Severity: SeverityCritical, For: "5m", Interval: "1m", Signal: "platform",
			Params: []AlertParam{},
		},
		annotations: map[string]string{"summary": "The release promoted to this env failed to converge on {{ $labels.cluster }}: {{ $labels.reason }}"},
		applies:     func(AlertInput) bool { return true },
		platform:    []platformRule{{PlatformDeployFailed, "DeployFailed", SeverityCritical}},
	},
	{
		AlertDefaultInfo: AlertDefaultInfo{
			ID: PlatformQuota, Alert: "ObservabilityQuotaOver100", AlertNames: []string{"ObservabilityQuotaOver150"},
			Title:    "Observability usage is at or over its monthly allowance (warning from 100%, critical from 150%)",
			Severity: SeverityWarning, For: "0s", Interval: "1m", Signal: "platform",
			Params: []AlertParam{},
		},
		annotations: map[string]string{"summary": "This org's observability usage is at {{ $value }}% of its monthly allowance; the gateway is limiting ingest"},
		applies:     func(AlertInput) bool { return true },
		platform:    []platformRule{{PlatformQuota + ":over_100", "ObservabilityQuotaOver100", SeverityWarning}, {PlatformQuota + ":over_150", "ObservabilityQuotaOver150", SeverityCritical}},
	},
}

// AlertDefaultIDs are the ids a forge.Alerts.defaults map may tune.
func AlertDefaultIDs() []string {
	ids := make([]string, 0, len(alertDefs))
	for _, d := range alertDefs {
		ids = append(ids, d.ID)
	}
	return ids
}

// AlertDefaultAlertNames are the rule names the defaults take: a user rule may
// not reuse one.
func AlertDefaultAlertNames() []string {
	names := make([]string, 0, len(alertDefs))
	for _, d := range alertDefs {
		names = append(names, d.Alert)
		names = append(names, d.AlertNames...)
	}
	return names
}

// ValidateAlertTuning refuses a tuning that names an unknown default or an
// unknown parameter of one, so a typo is an error at render, not a default
// that silently keeps its old threshold.
func ValidateAlertTuning(tuning map[string]AlertTuning) error {
	ids := map[string]alertDef{}
	for _, d := range alertDefs {
		ids[d.ID] = d
	}
	keys := make([]string, 0, len(tuning))
	for id := range tuning {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		d, ok := ids[id]
		if !ok {
			return fmt.Errorf("alerts.defaults names %q, which is not a generated default (have: %s)", id, strings.Join(AlertDefaultIDs(), ", "))
		}
		known := map[string]bool{}
		var names []string
		for _, p := range d.Params {
			known[p.Name] = true
			names = append(names, p.Name)
		}
		pk := make([]string, 0, len(tuning[id].Params))
		for name := range tuning[id].Params {
			pk = append(pk, name)
		}
		sort.Strings(pk)
		for _, name := range pk {
			if !known[name] {
				return fmt.Errorf("alerts.defaults[%q].params has %q, which %s does not take (params: %s)", id, name, d.ID, strings.Join(names, ", "))
			}
			if v := tuning[id].Params[name]; v <= 0 {
				return fmt.Errorf("alerts.defaults[%q].params[%q] = %v: a threshold or window must be positive", id, name, v)
			}
		}
	}
	return nil
}

// AlertDefaults is the catalog for the generated file: every default the input
// applies to, at its default parameters.
func AlertDefaults(in AlertInput) AlertDefaultsDoc {
	doc := AlertDefaultsDoc{Version: AlertDefaultsVersion, Input: in, Defaults: []AlertDefaultInfo{}}
	for _, d := range alertDefs {
		if d.applies(in) {
			doc.Defaults = append(doc.Defaults, d.AlertDefaultInfo)
		}
	}
	return doc
}

// MarshalAlertDefaults is the file's bytes: the catalog for in, with the
// services left to their alertServiceParts.
func MarshalAlertDefaults(in AlertInput) ([]byte, error) {
	doc := AlertDefaults(in)
	doc.Input.Services = nil
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParseAlertDefaults reads the generated file: the input without its
// services (ReadAlertInput adds them).
func ParseAlertDefaults(b []byte) (AlertInput, error) {
	var doc AlertDefaultsDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return AlertInput{}, fmt.Errorf("%s: %w", AlertDefaultsFile, err)
	}
	if doc.Version != AlertDefaultsVersion {
		return AlertInput{}, fmt.Errorf("%s: version %q, this forge reads %q: run `forge generate`", AlertDefaultsFile, doc.Version, AlertDefaultsVersion)
	}
	return doc.Input, nil
}

// ReadAlertInput reads the alert defaults' input forge generate last wrote
// under projectDir: AlertDefaultsFile, with each alertServicePart's Alert as its
// services. ok is false when there is no AlertDefaultsFile — a project that
// has not run `forge generate` since alerting landed has no defaults, which
// is not an error.
func ReadAlertInput(projectDir string) (in AlertInput, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(AlertDefaultsFile)))
	if errors.Is(err, os.ErrNotExist) {
		return AlertInput{}, false, nil
	}
	if err != nil {
		return AlertInput{}, false, err
	}
	if in, err = ParseAlertDefaults(b); err != nil {
		return AlertInput{}, false, err
	}
	in.Services, err = readAlertServices(projectDir)
	if err != nil {
		return AlertInput{}, false, err
	}
	sort.Slice(in.Services, func(i, j int) bool { return in.Services[i].Name < in.Services[j].Name })
	return in, true, nil
}

// AlertSkip is a default left out of an env's rules, and why.
type AlertSkip struct{ ID, Reason string }

// ResolveAlertDefaults renders the defaults for one env, with the env's tuning
// applied. A default the env disables is left out. Rules come back sorted by
// alert name.
func ResolveAlertDefaults(in AlertInput, env string, tuning map[string]AlertTuning) ([]AlertRule, error) {
	rules, _, err := ResolveAlertDefaultsFor(in, env, tuning, nil)
	return rules, err
}

// ResolveAlertDefaultsFor is ResolveAlertDefaults for an env that opted out of
// the observability categories in disabled. A default that reads a disabled
// category's data has nothing to read, so it is left out and reported as
// skipped, not recorded as a rule that can only ever be empty.
//
// A platform default (deploy-failed, observability-quota) is always recorded,
// because the control plane raises it from its own events: enabled, it is an
// entry carrying the env's `for` and labels; disabled, an entry marked off.
func ResolveAlertDefaultsFor(in AlertInput, env string, tuning map[string]AlertTuning, disabled []string) ([]AlertRule, []AlertSkip, error) {
	if err := ValidateAlertTuning(tuning); err != nil {
		return nil, nil, err
	}
	off := map[string]bool{}
	for _, c := range disabled {
		off[c] = true
	}
	var (
		out     []AlertRule
		skipped []AlertSkip
	)
	for _, d := range alertDefs {
		if !d.applies(in) || (d.needs != nil && !d.needs(in)) {
			continue
		}
		t, tuned := tuning[d.ID]
		if tuned && !t.Enabled && d.platform == nil {
			continue
		}
		if d.Requires != "" && off[d.Requires] {
			skipped = append(skipped, AlertSkip{ID: d.ID, Reason: "the env opted out of " + d.Requires + " telemetry, so there is nothing for it to read"})
			continue
		}
		params := map[string]float64{}
		for _, p := range d.Params {
			params[p.Name] = p.Default
		}
		for k, v := range t.Params {
			params[k] = v
		}
		if d.platform != nil {
			for _, pr := range d.platform {
				r := AlertRule{
					ID: d.ID, Alert: pr.alert, Expr: PlatformExpr(pr.key, tuned && !t.Enabled),
					For: d.For, Interval: d.Interval,
					Labels:      map[string]string{"severity": pr.severity},
					Annotations: map[string]string{},
				}
				for k, v := range d.annotations {
					r.Annotations[k] = v
				}
				if t.For != "" {
					r.For = t.For
				}
				for k, v := range t.Labels {
					r.Labels[k] = v
				}
				out = append(out, r)
			}
			continue
		}
		levels := d.levels
		if levels == nil {
			levels = []alertLevel{{d.Alert, d.Severity, false}}
		}
		for _, lv := range levels {
			lp := map[string]float64{}
			for k, v := range params {
				lp[k] = v
			}
			if lv.critical {
				lp[pCritical] = 1
			}
			r := AlertRule{
				ID: d.ID, Alert: lv.alert, Expr: d.sql(env, in, lp),
				For: d.For, Interval: d.Interval,
				Labels:      map[string]string{"severity": lv.severity},
				Annotations: map[string]string{},
			}
			for k, v := range d.annotations {
				r.Annotations[k] = v
			}
			if t.For != "" {
				r.For = t.For
			}
			for k, v := range t.Labels {
				r.Labels[k] = v
			}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alert < out[j].Alert })
	return out, skipped, nil
}

// EnvIDPlaceholder stands for the env's id in a rule whose data is stamped with
// the id rather than the env's name: customer metadata, which the node agent
// labels from the namespace. The env id is not known when forge renders, so the
// SQL carries this token and whoever evaluates the rule binds it (BindEnvID
// here, for `forge obs alerts check`; the control plane for a deployed env).
const EnvIDPlaceholder = "{reliant_env_id:String}"

// BindEnvID replaces EnvIDPlaceholder with envID as a quoted literal.
func BindEnvID(expr, envID string) string {
	return strings.ReplaceAll(expr, EnvIDPlaceholder, sqlQuote(envID))
}

// PlatformExpr is the expr a platform default is recorded under; key is
// "<id>" or "<id>:<level>". It is a real
// SELECT that returns no row, so a ClickHouse evaluator that does not know the
// marker finds nothing and never fires; the control plane recognizes the
// comment, binds the env id and evaluates the event itself. `off` records that
// the env disabled the default.
func PlatformExpr(key string, off bool) string {
	m := "SELECT 1 AS value WHERE 0 /* reliant:platform " + key + " env:" + EnvIDPlaceholder
	if off {
		m += " off"
	}
	return m + " */"
}

// IsPlatformExpr reports whether expr is a recorded platform default.
func IsPlatformExpr(expr string) bool { return strings.Contains(expr, "/* reliant:platform ") }

// ── SQL ──────────────────────────────────────────────────────────────────────
//
// Native ClickHouse SQL against the OpenTelemetry tables the clickhouseexporter
// creates (otel_metrics_sum, otel_metrics_histogram, otel_traces). Every
// statement returns one row per firing instance: the label columns, then
// `value`, and only when the condition holds. Counters are cumulative by
// default, so each series is turned into its increase over the window from the
// differences of consecutive points, treating a drop as a restart (the new
// value is the increase since zero). Series are grouped by their full
// attribute set first, so two replicas never subtract each other.

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func sqlQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

func frontendServiceList(in AlertInput) string {
	q := make([]string, len(in.FrontendServices))
	for i, s := range in.FrontendServices {
		q[i] = sqlQuote(s)
	}
	return strings.Join(q, ", ")
}

func rpcServiceList(in AlertInput) string {
	q := make([]string, len(in.Services))
	for i, s := range in.Services {
		q[i] = sqlQuote(s.RPCService)
	}
	return strings.Join(q, ", ")
}

// windowFilter keeps the points a window's increase needs: a cumulative series
// needs one point BEFORE the window to difference against (two minutes back
// covers the SDK's default 60s export interval with one missed export to
// spare), a delta series does not (its points are already increases).
func windowFilter(w float64) string {
	return fmt.Sprintf("((AggregationTemporality = 1 AND TimeUnix >= now() - INTERVAL %s MINUTE) OR (AggregationTemporality != 1 AND TimeUnix >= now() - INTERVAL %s MINUTE - INTERVAL 2 MINUTE))", num(w), num(w))
}

func errorRateSQL(env string, in AlertInput, p map[string]float64) string {
	return fmt.Sprintf(`WITH series AS (
    SELECT ServiceName AS service,
           MetricName AS metric,
           Attributes['error_class'] AS error_class,
           arrayMap(x -> x.2, arraySort(x -> x.1, groupArray((TimeUnix, Value)))) AS vals,
           any(AggregationTemporality) AS temporality
    FROM otel_metrics_sum
    WHERE MetricName IN ('rpc.server.calls', 'rpc.server.errors')
      AND %s
      AND ResourceAttributes['deployment.environment.name'] = %s
      AND splitByChar('/', Attributes['procedure'])[2] IN (%s)
    GROUP BY ServiceName, ResourceAttributes, Attributes, MetricName
), increase AS (
    SELECT service, metric, error_class,
           if(temporality = 1, arraySum(vals), arraySum((v, d) -> if(d < 0, v, d), vals, arrayDifference(vals))) AS n
    FROM series
)
SELECT service,
       sumIf(n, metric = 'rpc.server.errors' AND error_class NOT IN ('user', 'canceled')) / sumIf(n, metric = 'rpc.server.calls') AS value
FROM increase
GROUP BY service
HAVING sumIf(n, metric = 'rpc.server.calls') >= %s AND value > %s
ORDER BY service`, windowFilter(p[pWindow]), sqlQuote(env), rpcServiceList(in), num(p[pMinRequests]), num(p["threshold"]))
}

func latencySQL(env string, in AlertInput, p map[string]float64) string {
	return fmt.Sprintf(`WITH series AS (
    SELECT ServiceName AS service,
           ExplicitBounds AS bounds,
           arrayMap(x -> x.2, arraySort(x -> x.1, groupArray((TimeUnix, arrayMap(c -> toFloat64(c), BucketCounts))))) AS snaps,
           any(AggregationTemporality) AS temporality
    FROM otel_metrics_histogram
    WHERE MetricName = 'rpc.server.duration'
      AND %s
      AND ResourceAttributes['deployment.environment.name'] = %s
      AND splitByChar('/', Attributes['procedure'])[2] IN (%s)
    GROUP BY ServiceName, ResourceAttributes, Attributes, ExplicitBounds
), increase AS (
    SELECT service, bounds,
           if(temporality = 1,
              arrayReduce('sumForEach', snaps),
              arrayReduce('sumForEach', arrayMap((cur, prev) -> arrayMap((c, q) -> if(c < q, c, c - q), cur, prev),
                                                 arraySlice(snaps, 2), arraySlice(snaps, 1, length(snaps) - 1)))) AS inc
    FROM series
    WHERE length(snaps) >= 2 OR temporality = 1
), merged AS (
    SELECT service, bounds, sumForEach(inc) AS counts FROM increase GROUP BY service, bounds
)
SELECT service,
       quantilePrometheusHistogram(0.95)(le, cum) AS value,
       max(cum) AS requests
FROM (
    SELECT service,
           arrayJoin(arrayZip(arrayPushBack(bounds, inf), arrayCumSum(counts))) AS pair,
           pair.1 AS le, pair.2 AS cum
    FROM merged
)
GROUP BY service
HAVING requests >= %s AND value > %s
ORDER BY service`, windowFilter(p[pWindow]), sqlQuote(env), rpcServiceList(in), num(p[pMinRequests]), num(p["threshold_seconds"]))
}

func silentSQL(env string, in AlertInput, p map[string]float64) string {
	return fmt.Sprintf(`WITH moves AS (
    SELECT service, arrayJoin(t) AS t
    FROM (
        SELECT ServiceName AS service,
               arraySort(x -> x.1, groupArray((TimeUnix, Value))) AS pts,
               arrayMap(x -> x.1, pts) AS ts,
               arrayMap(x -> x.2, pts) AS vs,
               if(any(AggregationTemporality) = 1, vs, arrayDifference(vs)) AS moved,
               arrayFilter((tm, d) -> d != 0, ts, moved) AS t
        FROM otel_metrics_sum
        WHERE MetricName = 'rpc.server.calls'
          AND TimeUnix >= now() - INTERVAL 24 HOUR - INTERVAL %s MINUTE - INTERVAL 1 MINUTE
          AND ResourceAttributes['deployment.environment.name'] = %s
          AND splitByChar('/', Attributes['procedure'])[2] IN (%s)
        GROUP BY ServiceName, ResourceAttributes, Attributes
    )
)
SELECT service, dateDiff('second', max(t), now()) AS value
FROM moves
GROUP BY service
HAVING max(t) < now() - INTERVAL %s MINUTE
   AND uniqExactIf(toStartOfHour(t), t < now() - INTERVAL %s MINUTE) >= %s
ORDER BY service`, num(p["silent_minutes"]), sqlQuote(env), rpcServiceList(in), num(p["silent_minutes"]), num(p["silent_minutes"]), num(p["min_active_hours"]))
}

func frontendErrorsSQL(env string, in AlertInput, p map[string]float64) string {
	return fmt.Sprintf(`SELECT ServiceName AS service,
       countIf(Timestamp >= now() - INTERVAL %[1]s MINUTE) AS value
FROM otel_traces
WHERE ServiceName IN (%[2]s)
  AND Timestamp >= now() - INTERVAL %[1]s MINUTE - INTERVAL %[3]s MINUTE
  AND ResourceAttributes['deployment.environment.name'] = %[4]s
  AND ResourceAttributes['rum.sessionId'] != ''
  AND lower(StatusCode) = 'error'
  AND SpanAttributes['component'] NOT IN ('fetch', 'xml-http-request')
  AND (SpanName != 'console.error' OR has(Events.Name, 'exception'))
GROUP BY ServiceName
HAVING value >= %[5]s
   AND value > %[6]s * countIf(Timestamp < now() - INTERVAL %[1]s MINUTE) * %[1]s / %[3]s
ORDER BY service`, num(p[pWindow]), frontendServiceList(in), num(p["baseline_minutes"]), sqlQuote(env), num(p["min_errors"]), num(p["spike_factor"]))
}

// frontendLCPSQL fires per frontend when the p75 of its LCP web-vitals spans
// (the SDK emits one `webvitals` span per measurement, with the metric as an
// attribute named for it) is over the poor threshold. It reads the p75 the
// Core Web Vitals program grades by, not an average, and needs a minimum
// sample so a handful of slow loads never fires it.
func frontendLCPSQL(env string, in AlertInput, p map[string]float64) string {
	return fmt.Sprintf(`SELECT ServiceName AS service,
       quantile(0.75)(toFloat64OrZero(SpanAttributes['lcp'])) AS value,
       count() AS samples
FROM otel_traces
WHERE ServiceName IN (%[1]s)
  AND Timestamp >= now() - INTERVAL %[2]s MINUTE
  AND ResourceAttributes['deployment.environment.name'] = %[3]s
  AND ResourceAttributes['rum.sessionId'] != ''
  AND SpanName = 'webvitals'
  AND SpanAttributes['lcp'] != ''
GROUP BY ServiceName
HAVING samples >= %[4]s AND value > %[5]s
ORDER BY service`, frontendServiceList(in), num(p[pWindow]), sqlQuote(env), num(p[pMinSamples]), num(p["threshold_ms"]))
}

// ── customer metadata ────────────────────────────────────────────────────────
//
// The node agent delivers these (control-plane contract K2): container
// restarts and the last terminated reason on otel_metrics_gauge, memory against
// its limit on the same table, and Kubernetes events as otel_logs rows. They
// carry no deployment.environment.name; the namespace labels stamp
// reliant.env.id, so the SQL is scoped by EnvIDPlaceholder. They are gauges, not
// counters: a restart count is the pod's own number, so growth over a window is
// max - min per series (a replaced pod is a new series, never a decrease). The
// agent samples restarts every 5 minutes, so the window gets one export
// interval of lookback.

const (
	metaEnv      = "ResourceAttributes['reliant.env.id'] = " + EnvIDPlaceholder
	metaWorkload = "ResourceAttributes['k8s.workload.name']"
	// restartLookback is one export interval of the restarts gauge.
	restartLookback = 6
	lastReason      = "if(mapContains(Attributes, 'k8s.container.last_terminated_reason'), Attributes['k8s.container.last_terminated_reason'], ResourceAttributes['k8s.container.last_terminated_reason'])"
)

func restartGrowth(w float64, extra string) string {
	return fmt.Sprintf(`SELECT %[1]s AS workload,
           max(Value) - min(Value) AS grown%[4]s
    FROM otel_metrics_gauge
    WHERE MetricName = 'k8s.container.restarts'
      AND TimeUnix >= now() - INTERVAL %[2]s MINUTE - INTERVAL %[3]d MINUTE
      AND %[5]s
      AND %[1]s != ''
    GROUP BY workload, ResourceAttributes['k8s.container.name'], ResourceAttributes['k8s.pod.uid'], ResourceAttributes['k8s.pod.name']`,
		metaWorkload, num(w), restartLookback, extra, metaEnv)
}

func eventCount(w float64, cond string) string {
	return fmt.Sprintf(`SELECT %[1]s AS workload, count() AS n
    FROM otel_logs
    WHERE Timestamp >= now() - INTERVAL %[2]s MINUTE
      AND %[3]s
      AND %[1]s != ''
      AND (%[4]s)
    GROUP BY workload`, metaWorkload, num(w), metaEnv, cond)
}

const (
	eventReason = "LogAttributes['k8s.event.reason']"
	// A crash loop shows as a BackOff event whose message says the kubelet is
	// restarting a failed container; the same reason on an image pull says
	// "pulling image", which is a different problem.
	crashEvent = eventReason + " = 'CrashLoopBackOff' OR (" + eventReason + " = 'BackOff' AND positionCaseInsensitive(Body, 'restarting') > 0)"
	pullEvent  = eventReason + " IN ('ErrImagePull', 'ImagePullBackOff', 'ErrImageNeverPull', 'InspectFailed') OR (" + eventReason + " IN ('Failed', 'BackOff') AND positionCaseInsensitive(Body, 'pull') > 0 AND positionCaseInsensitive(Body, 'image') > 0)"
	oomEvent   = eventReason + " = 'OOMKilling' OR LogAttributes['k8s.container.last_terminated_reason'] = 'OOMKilled'"
	evictEvent = eventReason + " = 'Evicted'"
)

func crashLoopSQL(_ string, _ AlertInput, p map[string]float64) string {
	return fmt.Sprintf(`WITH grown_restarts AS (
    %s
), backoff_events AS (
    %s
)
SELECT workload, greatest(total_restarts, toFloat64(total_backoffs)) AS value
FROM (
    SELECT workload, sum(grown) AS total_restarts, sum(ev) AS total_backoffs
    FROM (
        SELECT workload, grown, 0 AS ev FROM grown_restarts
        UNION ALL
        SELECT workload, 0 AS grown, n AS ev FROM backoff_events
    )
    GROUP BY workload
)
WHERE total_restarts >= %s OR total_backoffs >= 1
ORDER BY workload`, restartGrowth(p[pWindow], ""), eventCount(p[pWindow], crashEvent), num(p["min_restarts"]))
}

func oomKilledSQL(_ string, _ AlertInput, p map[string]float64) string {
	return fmt.Sprintf(`WITH grown_restarts AS (
    %s
), oom_events AS (
    %s
)
SELECT workload, max(oom) AS value
FROM (
    SELECT workload, grown AS oom FROM grown_restarts WHERE grown > 0 AND reason = 'OOMKilled'
    UNION ALL
    SELECT workload, toFloat64(n) AS oom FROM oom_events
)
GROUP BY workload
ORDER BY workload`, restartGrowth(p[pWindow], ",\n           argMax("+lastReason+", TimeUnix) AS reason"), eventCount(p[pWindow], oomEvent))
}

func memoryNearLimitSQL(_ string, _ AlertInput, p map[string]float64) string {
	return fmt.Sprintf(`SELECT workload, container, max(used) AS value
FROM (
    SELECT %[1]s AS workload,
           ResourceAttributes['k8s.container.name'] AS container,
           avg(Value) AS used
    FROM otel_metrics_gauge
    WHERE MetricName = 'k8s.container.memory_limit_utilization'
      AND TimeUnix >= now() - INTERVAL %[2]s MINUTE
      AND %[3]s
      AND %[1]s != ''
    GROUP BY workload, container, ResourceAttributes['k8s.pod.uid'], ResourceAttributes['k8s.pod.name']
)
GROUP BY workload, container
HAVING value > %[4]s
ORDER BY workload, container`, metaWorkload, num(p[pWindow]), metaEnv, num(p["threshold"]))
}

func eventRuleSQL(cond string) func(string, AlertInput, map[string]float64) string {
	return func(_ string, _ AlertInput, p map[string]float64) string {
		return fmt.Sprintf(`SELECT workload, n AS value
FROM (
    %s
)
WHERE n >= %s
ORDER BY workload`, eventCount(p[pWindow], cond), num(p["min_events"]))
	}
}

var (
	imagePullFailedSQL = eventRuleSQL(pullEvent)
	evictedSQL         = eventRuleSQL(evictEvent)
)
