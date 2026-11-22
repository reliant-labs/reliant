package cli

// WHERE A LOCAL STACK'S TELEMETRY GOES (control-plane
// docs/design/dev-telemetry-to-hosted.md §2, §5).
//
// A `forge env up` stack either stores its telemetry in the local ClickStack
// (docker) or ships it to the org's hosted store, keyed by the stack's
// session — the instance. The env declares the intent
// (observability.dev_telemetry, default "auto"); `--telemetry` and
// FORGE_DEV_TELEMETRY override it; "local" from any of them wins, because it
// is the opt-out.
//
// "auto" resolves HERE, at env up, from two facts: the env declares a
// control_plane, and a Reliant credential exists. Everything after that is a
// network exchange with the control plane (ensure the LOCAL environment,
// report the session live, mint the stack's two tokens), and under "auto"
// any failure of it falls back to the local store with the reason printed.
// "hosted" fails the run instead. The decision and its reason are written to
// .forge/obs/backend-<env>.json (internal/devtelemetry) so `forge obs` and
// `forge env status` read what was decided rather than re-deriving it from
// ambient login state.
//
// In hosted mode the local collector, the forwarder and the rest of the local
// observability compose services are not started at all, so a container-free
// host env works; the host processes export OTLP/HTTP straight to the hosted
// edge with the process token's headers file, and the frontend dev server's
// /_otel proxy attaches the browser token server-side.
//
// THE APP IS NEVER AFFECTED BY A TOKEN. The stack's telemetry keeper
// (devtelemetry_keeper.go) renews at each half-life on its own timer,
// retrying with backoff, and fails silently: an expired token makes exports
// get 401 and pkg/observe drop the batch; `forge env status` says so.
//
// EVERY SETUP RECORDS ITS DECISION FIRST. A run that ends local — by
// choice, by fallback, or because hosted setup failed — overwrites
// backend-<env>.json with "local" and its reason and removes any token a
// previous hosted run left (settleDevTelemetryLocal).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/clickstack"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/devtelemetry"
	"github.com/reliant-labs/forge/pkg/release"
)

// The dev_telemetry values.
const (
	devTelemetryAuto   = "auto"
	devTelemetryHosted = "hosted"
	devTelemetryLocal  = "local"
)

// devTelemetryEnvVar overrides the env's dev_telemetry, under --telemetry.
const devTelemetryEnvVar = "FORGE_DEV_TELEMETRY"

const procMintLocalTelemetryToken = "controlplane.v1.DeployService/MintLocalTelemetryToken"

// The FailedPrecondition reasons MintLocalTelemetryToken refuses with.
const (
	reasonDevTelemetryDisabled   = "dev_telemetry_disabled"
	reasonDevTelemetryNotEnabled = "dev_telemetry_not_enabled"
	reasonSessionNotLive         = "session_not_live"
	reasonDevInstanceLimit       = "dev_instance_limit"
)

// frontendOTLPHeadersFileEnv names, for a frontend dev server, the file
// holding the headers its /_otel proxy attaches upstream. Read per request,
// so a renewed token is picked up without a restart.
const frontendOTLPHeadersFileEnv = "FORGE_OTEL_HEADERS_FILE"

// devTelemetryMintTimeout bounds one mint. Like a presence report, the mint
// stands between an unreachable control plane and the startup path, and
// under "auto" its failure only means "store it locally".
const devTelemetryMintTimeout = 5 * time.Second

// devTelemetryMode is the requested mode and where the request came from.
type devTelemetryMode struct {
	Value  string
	Source string
}

func (m devTelemetryMode) String() string { return "dev_telemetry=" + m.Value + " (" + m.Source + ")" }

// resolveDevTelemetryMode combines the three places a mode is stated.
// "local" from any of them wins (it is the opt-out); otherwise the first set
// of --telemetry, FORGE_DEV_TELEMETRY and the declaration, else "auto".
func resolveDevTelemetryMode(flag, envVar, declared string) (devTelemetryMode, error) {
	sources := []devTelemetryMode{
		{strings.TrimSpace(flag), "--telemetry"},
		{strings.TrimSpace(envVar), devTelemetryEnvVar},
		{strings.TrimSpace(declared), "observability.dev_telemetry"},
	}
	var chosen *devTelemetryMode
	for i := range sources {
		m := &sources[i]
		switch m.Value {
		case "":
			continue
		case devTelemetryAuto, devTelemetryHosted, devTelemetryLocal:
		default:
			return devTelemetryMode{}, fmt.Errorf("%s=%q: must be auto, hosted or local", m.Source, m.Value)
		}
		if m.Value == devTelemetryLocal {
			return *m, nil
		}
		if chosen == nil {
			chosen = m
		}
	}
	if chosen == nil {
		return devTelemetryMode{Value: devTelemetryAuto, Source: "default"}, nil
	}
	return *chosen, nil
}

// devTelemetryRetention is the retention a stack asks for, as the mint
// request's `retention` carries it. Zero days are omitted: the server's dev
// default (3 days) applies.
type devTelemetryRetention struct {
	TracesDays  int                       `json:"tracesDays,omitempty"`
	MetricsDays int                       `json:"metricsDays,omitempty"`
	LogsDays    int                       `json:"logsDays,omitempty"`
	BrowserDays int                       `json:"browserDays,omitempty"`
	Overrides   []devTelemetryOverrideReq `json:"overrides,omitempty"`
}

type devTelemetryOverrideReq struct {
	Service string `json:"service"`
	Signal  string `json:"signal"`
	Days    int    `json:"days"`
}

// devTelemetryRetentionOf is the env's declared retention, for the request.
func devTelemetryRetentionOf(o *ObservabilityEntity) *devTelemetryRetention {
	if o == nil {
		return nil
	}
	r := &devTelemetryRetention{
		TracesDays:  o.Retention.TracesDays,
		MetricsDays: o.Retention.MetricsDays,
		LogsDays:    o.Retention.LogsDays,
		BrowserDays: o.Retention.BrowserDays,
	}
	for _, ov := range o.Retention.Overrides {
		r.Overrides = append(r.Overrides, devTelemetryOverrideReq(ov))
	}
	if r.TracesDays == 0 && r.MetricsDays == 0 && r.LogsDays == 0 && r.BrowserDays == 0 && len(r.Overrides) == 0 {
		return nil
	}
	return r
}

// devTelemetryGrant is MintLocalTelemetryToken's answer.
type devTelemetryGrant struct {
	ProcessToken  string `json:"processToken"`
	BrowserToken  string `json:"browserToken"`
	SessionReplay bool   `json:"sessionReplay"`
	ExpiresAt     string `json:"expiresAt"`
	Endpoint      string `json:"endpoint"`
	OrgID         string `json:"orgId"`
	EnvironmentID string `json:"environmentId"`
	Instance      struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		User  string `json:"user"`
	} `json:"instance"`
	Retention devtelemetry.Retention `json:"-"`
	WireRet   struct {
		TracesDays  int `json:"tracesDays"`
		MetricsDays int `json:"metricsDays"`
		LogsDays    int `json:"logsDays"`
		BrowserDays int `json:"browserDays"`
	} `json:"retention"`

	// mintedAt and expires are parsed, for the half-life.
	mintedAt time.Time
	expires  time.Time
}

// mintDevTelemetry asks the control plane for this session's two tokens.
func mintDevTelemetry(ctx context.Context, client cloudCaller, sessionID, project, env string, retention *devTelemetryRetention, now time.Time, sessionReplay bool) (devTelemetryGrant, error) {
	// Always send false too: a local session must not inherit another stack's
	// or the deployed environment's replay opt-in. Old servers may ignore the
	// field, so capture additionally requires an affirmative response below.
	replay := sessionReplay
	req := map[string]any{"sessionId": sessionID, "project": project, "environment": env, "sessionReplay": replay}
	if retention != nil {
		req["retention"] = retention
	}
	ctx, cancel := context.WithTimeout(ctx, devTelemetryMintTimeout)
	defer cancel()
	var g devTelemetryGrant
	if err := client.Call(ctx, procMintLocalTelemetryToken, req, &g); err != nil {
		return devTelemetryGrant{}, err
	}
	// instance.id is the control plane's id for this instance (its session
	// row), which the tokens' `sid` carries and every telemetry row is keyed
	// on — NOT the session id forge sent, which repeats across runs and
	// orgs. Without it nothing could be scoped to this instance.
	if g.ProcessToken == "" || g.BrowserToken == "" || g.Endpoint == "" || g.Instance.ID == "" {
		return devTelemetryGrant{}, fmt.Errorf("%s answered without a token, an endpoint or an instance id", procMintLocalTelemetryToken)
	}
	exp, err := time.Parse(time.RFC3339Nano, g.ExpiresAt)
	if err != nil {
		return devTelemetryGrant{}, fmt.Errorf("%s answered an unreadable expiresAt %q: %w", procMintLocalTelemetryToken, g.ExpiresAt, err)
	}
	g.SessionReplay = replay && g.SessionReplay
	g.mintedAt, g.expires = now, exp
	g.Endpoint = strings.TrimRight(g.Endpoint, "/")
	g.Retention = devtelemetry.Retention{TracesDays: g.WireRet.TracesDays, MetricsDays: g.WireRet.MetricsDays, LogsDays: g.WireRet.LogsDays, BrowserDays: g.WireRet.BrowserDays}
	return g, nil
}

// mintRefusal says, in one line, why the control plane would not mint.
func mintRefusal(err error) string {
	var ce *cloud.Error
	if !errors.As(err, &ce) {
		return "the control plane is unreachable: " + err.Error()
	}
	reason := ce.Reason
	if reason == "" {
		// A reason sent only in the message (or a detail's debug JSON)
		// still names the refusal.
		for _, r := range []string{reasonDevTelemetryDisabled, reasonDevTelemetryNotEnabled, reasonSessionNotLive, reasonDevInstanceLimit} {
			if strings.Contains(ce.Message, r) {
				reason = r
			}
		}
	}
	switch {
	case ce.HasCode(cloud.CodeUnimplemented):
		return "the control plane predates hosted dev telemetry (MintLocalTelemetryToken is unimplemented)"
	case reason == reasonDevTelemetryDisabled:
		return "an org admin switched dev telemetry off for this org (" + reason + ")"
	case reason == reasonDevTelemetryNotEnabled:
		return "hosted dev telemetry is not enabled for this org yet (" + reason + ")"
	case reason == reasonDevInstanceLimit:
		return "you already run as many live dev instances as the org allows; stop one (`" + Name() + " env down <env>` in its checkout) (" + reason + ")"
	case reason == reasonSessionNotLive:
		return "the control plane does not see this stack's session as live (" + reason + ")"
	case ce.HasCode(cloud.CodeNotFound):
		return "the control plane has no session or LOCAL environment for this stack (not_found)"
	case ce.HasCode(cloud.CodePermissionDenied):
		return "the credential may not mint telemetry tokens for this project (permission_denied)"
	case ce.HasCode(cloud.CodeFailedPrecondition):
		return "the control plane refused: " + ce.Message
	}
	return "could not mint telemetry tokens: " + err.Error()
}

// devTelemetryFacts is everything the decision reads, gathered by the caller.
type devTelemetryFacts struct {
	Env  string
	Mode devTelemetryMode
	// TelemetryOn: the env declares observability and it is enabled.
	TelemetryOn  bool
	ControlPlane bool
	// CredentialErr is nil when a Reliant credential exists.
	CredentialErr error
	// Session is the stack's session, reported to a control plane; nil
	// when there is none (SessionSkip says why).
	Session       *upSession
	SessionSkip   string
	Client        cloudCaller
	Project       string
	Retention     *devTelemetryRetention
	SessionReplay bool
}

// devTelemetryDecision is where this run's telemetry goes.
type devTelemetryDecision struct {
	Hosted bool
	Reason string
	Grant  devTelemetryGrant
}

// decideDevTelemetry resolves the mode, and for a hosted stack runs the
// exchange with the control plane. Under "hosted" a reason not to is an
// error; under "auto" it is a local decision carrying that reason.
func decideDevTelemetry(ctx context.Context, f devTelemetryFacts, now time.Time) (devTelemetryDecision, error) {
	local := func(reason string) (devTelemetryDecision, error) {
		if f.Mode.Value == devTelemetryHosted {
			return devTelemetryDecision{}, fmt.Errorf("%s, but %s.\n"+
				"fix: resolve that, or let it fall back with `--telemetry=auto` (or dev_telemetry = \"auto\" in deploy/kcl/%s/main.k)", f.Mode, reason, f.Env)
		}
		return devTelemetryDecision{Reason: reason}, nil
	}
	switch {
	case f.Mode.Value == devTelemetryLocal:
		return devTelemetryDecision{Reason: f.Mode.String()}, nil
	case !f.TelemetryOn:
		return local("observability is off for this env")
	case !f.ControlPlane:
		return local("the env declares no control_plane")
	case f.CredentialErr != nil:
		return local("no Reliant credential (" + firstLine(f.CredentialErr.Error()) + ")")
	case f.Session == nil || f.Client == nil:
		why := f.SessionSkip
		if why == "" {
			why = "its presence is not reported to the control plane"
		}
		return local("this stack has no session to key its telemetry on: " + why)
	}
	// The mint needs a LIVE session, so the report (and the ensure of
	// the env it reports into) has to land first.
	if err := f.Session.reportLive(now); err != nil {
		return local("the control plane did not take this stack's session: " + firstLine(err.Error()))
	}
	g, err := mintDevTelemetry(ctx, f.Client, f.Session.session.ID, f.Project, f.Env, f.Retention, now, f.SessionReplay)
	if err != nil {
		return local(mintRefusal(err))
	}
	reason := f.Mode.String()
	if f.Mode.Value == devTelemetryAuto {
		reason = "dev_telemetry=auto: control_plane declared and signed in"
	}
	return devTelemetryDecision{Hosted: true, Reason: reason, Grant: g}, nil
}

// devTelemetryRun is a hosted stack's token state for as long as it runs.
type devTelemetryRun struct {
	projectDir, env, project string
	client                   cloudCaller
	sessionID                string
	retention                *devTelemetryRetention
	sessionReplay            bool
	frontends                []FrontendEntity
	log                      io.Writer
	// endpoint and session are what the keeper (devtelemetry_keeper.go)
	// needs to keep the stack's session live and its tokens current.
	endpoint cloud.Endpoint
	session  release.LocalSession
	// stillHosted, when set, is asked before each renewal and before the
	// renewed tokens are written: false means this env's telemetry was
	// decided again (local, or another instance) and this run must stop
	// rather than restore a grant the decision withdrew. The keeper sets it
	// (devTelemetryKeeperOwns); nil means always.
	stillHosted func() bool

	mu    sync.Mutex
	grant devTelemetryGrant
}

// write puts the grant's tokens and state on disk.
func (r *devTelemetryRun) write(g devTelemetryGrant, now time.Time) error {
	if err := devtelemetry.WriteHeaders(r.projectDir, g.ProcessToken, g.BrowserToken); err != nil {
		return err
	}
	return devtelemetry.WriteState(r.projectDir, devtelemetry.State{Env: r.env, ExpiresAt: g.expires, RenewedAt: now, Retention: g.Retention})
}

// devTelemetryClock is the renewal scheduler's clock. A seam so a test can
// move time past a token's half-life without waiting half an hour.
type devTelemetryClock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realDevTelemetryClock struct{}

func (realDevTelemetryClock) Now() time.Time                         { return time.Now() }
func (realDevTelemetryClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// The renewal retry backoff: from the first, doubling to the cap.
const (
	devTelemetryRetryFirst = 15 * time.Second
	devTelemetryRetryCap   = 5 * time.Minute
)

// renewAt is when the current tokens are due for renewal: their half-life.
func (r *devTelemetryRun) renewAt() time.Time {
	r.mu.Lock()
	g := r.grant
	r.mu.Unlock()
	return g.mintedAt.Add(g.expires.Sub(g.mintedAt) / 2)
}

// renewNow replaces both tokens. On failure the old tokens stay (they work
// until they expire) and the reason is recorded for `forge env status` —
// never printed into the stack's output.
// errDevTelemetrySuperseded is renewNow's answer when the env's telemetry was
// decided again while it ran: nothing was written, and the run must stop.
var errDevTelemetrySuperseded = errors.New("this env's telemetry was decided again; the grant is withdrawn")

func (r *devTelemetryRun) superseded() bool {
	return r.stillHosted != nil && !r.stillHosted()
}

func (r *devTelemetryRun) renewNow(ctx context.Context, now time.Time) error {
	if r.superseded() {
		return errDevTelemetrySuperseded
	}
	g, err := mintDevTelemetry(ctx, r.client, r.sessionID, r.project, r.env, r.retention, now, r.sessionReplay)
	if err == nil {
		// Asked again after the mint: a decision taken while it was in
		// flight must not see its cleared tokens written back.
		if r.superseded() {
			return errDevTelemetrySuperseded
		}
		r.mu.Lock()
		wasReplay := r.grant.SessionReplay
		r.mu.Unlock()
		err = r.write(g, now)
		if err == nil {
			// Persistence is the commit point. Capture withdrawal is a
			// separate effect: it must not roll an active grant back.
			r.mu.Lock()
			r.grant = g
			r.mu.Unlock()
			if wasReplay && !g.SessionReplay {
				if withdrawalErr := r.stopReplayForReloads(); withdrawalErr != nil {
					return r.replayWithdrawalFailed(withdrawalErr, now)
				}
			}
		}
	}
	if err != nil {
		r.mu.Lock()
		cur := r.grant
		r.mu.Unlock()
		_ = devtelemetry.WriteState(r.projectDir, devtelemetry.State{
			Env: r.env, ExpiresAt: cur.expires, RenewedAt: cur.mintedAt, Retention: cur.Retention,
			RenewError: firstLine(mintRefusal(err)),
		})
		return err
	}
	return nil
}

// renewUntil renews the tokens at each half-life until ctx is done, on its
// OWN timer: it never waits on a presence report, so a slow or failing
// report cannot push renewal past expiry. The mint needs a live session, and
// the mint itself is what proves liveness server-side; a refusal is retried
// with backoff (15s doubling to 5m) like any other failure, while the
// heartbeat keeps trying to make the session live.
func (r *devTelemetryRun) renewUntil(ctx context.Context, clock devTelemetryClock) {
	retry := time.Duration(0)
	for {
		wait := retry
		if wait == 0 {
			wait = r.renewAt().Sub(clock.Now())
		}
		if wait < 0 {
			wait = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-clock.After(wait):
		}
		if err := r.renewNow(ctx, clock.Now()); err != nil {
			if ctx.Err() != nil || errors.Is(err, errDevTelemetrySuperseded) || errors.Is(err, errDevReplayWithdrawal) {
				return
			}
			switch {
			case retry == 0:
				retry = devTelemetryRetryFirst
			case retry < devTelemetryRetryCap:
				retry = min(retry*2, devTelemetryRetryCap)
			}
			continue
		}
		retry = 0
	}
}

// recordDevTelemetryDecision writes .forge/obs/backend-<env>.json.
func recordDevTelemetryDecision(projectDir, env string, d devTelemetryDecision, now time.Time) error {
	// InstanceID is the control plane's instance id from the mint response,
	// never forge's local session id (see mintDevTelemetry).
	rec := devtelemetry.Record{Env: env, Backend: "local", Reason: d.Reason, DecidedAt: now}
	if d.Hosted {
		g := d.Grant
		rec.Backend = "cloud"
		rec.OrgID, rec.EnvironmentID = g.OrgID, g.EnvironmentID
		rec.InstanceID, rec.InstanceLabel, rec.InstanceUser = g.Instance.ID, g.Instance.Label, g.Instance.User
		rec.Endpoint = g.Endpoint
	}
	return devtelemetry.Write(projectDir, rec)
}

// localObservabilityServices are the compose services that store or forward
// a stack's telemetry locally. A hosted stack runs none of them.
var localObservabilityServices = map[string]bool{
	clickstack.Service:             true,
	clickstack.ForwarderService:    true,
	clickstack.ProfilesService:     true,
	clickstack.AlertmanagerService: true,
}

// applyHostedDevTelemetry rewires e for a hosted run: the local
// observability compose services (and what requires them) are dropped, host
// processes export to the hosted edge, and frontends proxy there.
func applyHostedDevTelemetry(e *KCLEntities, projectDir string, g devTelemetryGrant, shell []string) (skipped []string) {
	skip := map[string]bool{}
	for _, wl := range e.WorkloadsOn(RuntimeCompose) {
		if localObservabilityServices[composeServiceName(wl)] {
			skip[wl.Name] = true
		}
	}
	// closeOverRequires adds to skip what requires a dropped service (the
	// local alert evaluator), so it is dropped with it.
	closeOverRequires(e, skip)
	if len(skip) > 0 {
		kept := make([]WorkloadEntity, 0, len(e.Workloads))
		for _, wl := range e.Workloads {
			if !skip[wl.Name] {
				kept = append(kept, wl)
			}
		}
		e.Workloads = kept
		e.OptionalSkipped = mergeSorted(e.OptionalSkipped, sortedKeys(skip))
	}
	headers := devtelemetry.HeadersPath(projectDir)
	for _, wl := range e.Workloads {
		h := wl.Runtime.Host
		if h == nil {
			continue
		}
		add := hostedTelemetryLaunchEnv(wl.HostEnv(), shell, g.Endpoint, headers)
		if len(add) == 0 {
			continue
		}
		if h.LaunchEnv == nil {
			h.LaunchEnv = map[string]string{}
		}
		for k, v := range add {
			h.LaunchEnv[k] = v
		}
	}
	// An old server's missing acknowledgement is fail-closed. Never change
	// owned declarations or build artifacts, just this run's resolved document.
	if !g.SessionReplay {
		disableDevReplay(e)
	}
	if e.Observability != nil {
		o := *e.Observability
		o.LocalEndpoint = g.Endpoint
		o.ProfilesEndpoint = ""
		o.AlertmanagerPort, o.AlertsPort = 0, 0
		o.DevBrowserHeadersFile = devtelemetry.BrowserHeadersPath(projectDir)
		e.Observability = &o
	}
	return sortedKeys(skip)
}

// otlpTransportKeys are the variables that decide where a process exports
// and with what credential. One set by the developer's shell means the
// developer chose the transport.
var otlpTransportKeys = func() []string {
	var out []string
	for _, suffix := range []string{"ENDPOINT", "PROTOCOL", "HEADERS", "HEADERS_FILE"} {
		for _, sig := range []string{"", "TRACES_", "METRICS_", "LOGS_"} {
			out = append(out, clickstack.EnvOTLPPrefix+sig+suffix)
		}
	}
	return out
}()

// hostedTelemetryLaunchEnv is what a hosted run adds to one host process's
// declared env (empty: leave it alone). The endpoint, protocol and headers
// file are ONE unit, as the local pair is: a transport the developer exported
// or the author declared wins whole, and only a loopback endpoint forge
// derived for the local collector is replaced.
func hostedTelemetryLaunchEnv(declared map[string]string, shell []string, endpoint, headersFile string) map[string]string {
	shellSet := nonEmptyEnvKeys(shell)
	for _, k := range otlpTransportKeys {
		if shellSet[k] {
			return nil
		}
		if k == clickstack.EnvOTLPEndpoint || k == clickstack.EnvOTLPProtocol {
			continue
		}
		if declared[k] != "" {
			return nil
		}
	}
	if ep := declared[clickstack.EnvOTLPEndpoint]; ep != "" && !isLoopbackDerived(ep) {
		return nil
	}
	out := map[string]string{
		clickstack.EnvOTLPEndpoint:                endpoint,
		clickstack.EnvOTLPProtocol:                "http/protobuf",
		clickstack.EnvOTLPPrefix + "HEADERS_FILE": headersFile,
	}
	// No collector tails the log files in this mode, so the process ships
	// its own logs — unless the env switched logs off (OTEL_LOGS_EXPORTER
	// =none is declared then) or the developer chose.
	if declared["OTEL_LOGS_EXPORTER"] == "" && !shellSet["OTEL_LOGS_EXPORTER"] {
		out["OTEL_LOGS_EXPORTER"] = "otlp"
	}
	// The local profiles collector is not running, and the hosted edge
	// takes no profiles in v1.
	if isLoopbackDerived(declared[clickstack.EnvOTLPProfilesEndpoint]) {
		out[clickstack.EnvOTLPProfilesEndpoint] = ""
	}
	if declared["OTEL_PROFILES_EXPORTER"] == "" && !shellSet["OTEL_PROFILES_EXPORTER"] && !shellSet[clickstack.EnvOTLPProfilesEndpoint] {
		out["OTEL_PROFILES_EXPORTER"] = "none"
	}
	return out
}

// retentionLabel is the granted retention in a few characters: "3d" when
// every signal agrees.
func retentionLabel(r devtelemetry.Retention) string {
	type kv struct {
		k string
		v int
	}
	all := []kv{{"traces", r.TracesDays}, {"metrics", r.MetricsDays}, {"logs", r.LogsDays}, {"browser", r.BrowserDays}}
	same := true
	for _, x := range all[1:] {
		if x.v != all[0].v {
			same = false
		}
	}
	if same {
		if all[0].v == 0 {
			return "default retention"
		}
		return fmt.Sprintf("%dd", all[0].v)
	}
	var parts []string
	for _, x := range all {
		if x.v > 0 {
			parts = append(parts, fmt.Sprintf("%s %dd", x.k, x.v))
		}
	}
	return strings.Join(parts, ", ")
}

// printDevTelemetryNotice is the one-time notice of a project's first hosted
// stack: what is sent, where, for how long, and how to stop it.
func printDevTelemetryNotice(w io.Writer, env string, g devTelemetryGrant) {
	fmt.Fprintf(w, "[up] telemetry: this stack's telemetry goes to Reliant's hosted store, not the local ClickStack\n")
	replay := "no session replay"
	if g.SessionReplay {
		replay = "masked session replay; DNT/GPC and consent still apply"
	}
	fmt.Fprintf(w, "     what:      traces, metrics and logs of every host process, and the browser's errors, console and network spans (%s)\n", replay)
	fmt.Fprintf(w, "     note:      that includes what your code logs and the query text of database spans; credential-shaped strings are scrubbed before storage\n")
	fmt.Fprintf(w, "     where:     org %s, instance %s", g.OrgID, emptyAs(g.Instance.Label, g.Instance.ID))
	if g.Instance.User != "" {
		fmt.Fprintf(w, " (%s)", g.Instance.User)
	}
	fmt.Fprintf(w, "; readable by members of the org with obs:read\n")
	fmt.Fprintf(w, "     retention: %s\n", retentionLabel(g.Retention))
	fmt.Fprintf(w, "     opt out:   `%s env up %s --telemetry=local`, %s=local, or observability.dev_telemetry = \"local\" in deploy/kcl/%s/main.k; an org admin can switch it off for the org\n",
		Name(), env, devTelemetryEnvVar, env)
	fmt.Fprintf(w, "     (shown once per project; `%s env status %s` shows where telemetry goes)\n", Name(), env)
}

// devTelemetryStatusLine is `forge env status`'s telemetry line, or "" when
// no `forge env up` of this env recorded a decision here.
func devTelemetryStatusLine(projectDir, env string, now time.Time) string {
	rec, ok, err := devtelemetry.Read(projectDir, env)
	if err != nil {
		return "telemetry: unknown (" + err.Error() + ")"
	}
	if !ok {
		return ""
	}
	if rec.Backend != "cloud" {
		return "telemetry: local (" + rec.Reason + ")"
	}
	st, ok, err := devtelemetry.ReadState(projectDir, env)
	if err != nil || !ok {
		return "telemetry: not delivering (no token state in .forge/obs; re-run `" + Name() + " env up " + env + "`)"
	}
	if !now.Before(st.ExpiresAt) {
		why := "token expired"
		if st.RenewError != "" {
			why += "; " + st.RenewError
		} else {
			why += "; the stack is not running, or its " + devTelemetryKeeperName + " is not (see .forge/logs/" + env + "/" + devTelemetryKeeperName + ".log)"
		}
		return "telemetry: not delivering (" + why + ")"
	}
	return fmt.Sprintf("telemetry: hosted (org %s, instance %s, %s)", rec.OrgID, emptyAs(rec.InstanceLabel, rec.InstanceID), retentionLabel(st.Retention))
}

// devTelemetrySetupIn is what runUp hands setupDevTelemetry: the project and
// env, the --telemetry flag, the run's session and its target, where to say
// things, and the clock.
type devTelemetrySetupIn struct {
	ProjectDir, Env, Flag string
	Session               *upSession
	Target                sessionTarget
	Out                   io.Writer
	Now                   time.Time
}

// devTelemetrySetup is runUp's half: gather the facts from the render and the
// session, decide, record, and rewire the entities for a hosted run.
func setupDevTelemetry(ctx context.Context, e *KCLEntities, in devTelemetrySetupIn) (*devTelemetryRun, error) {
	projectDir, env, flag, session, target, w, now := in.ProjectDir, in.Env, in.Flag, in.Session, in.Target, in.Out, in.Now
	if e == nil {
		return nil, nil
	}
	declared := ""
	if e.Observability != nil {
		declared = e.Observability.DevTelemetry
	}
	mode, err := resolveDevTelemetryMode(flag, os.Getenv(devTelemetryEnvVar), declared)
	if err != nil {
		settleDevTelemetryLocal(projectDir, env, "the telemetry mode is invalid: "+err.Error(), now, w)
		return nil, err
	}
	decl := declarationFromEntities(e)
	facts := devTelemetryFacts{
		Env:           env,
		Mode:          mode,
		TelemetryOn:   e.Observability != nil && e.Observability.Enabled,
		ControlPlane:  decl != nil,
		Session:       session,
		SessionSkip:   target.Skip,
		Client:        target.Client,
		Project:       target.Project,
		Retention:     devTelemetryRetentionOf(e.Observability),
		SessionReplay: devReplayRequested(e),
	}
	if facts.ControlPlane && mode.Value != devTelemetryLocal {
		facts.CredentialErr = devTelemetryCredential(env, decl)
	}
	d, err := decideDevTelemetry(ctx, facts, now)
	if err != nil {
		// A hosted setup that failed leaves this env local (the run
		// stops): no previous grant may survive it on disk.
		settleDevTelemetryLocal(projectDir, env, "dev_telemetry=hosted failed: "+firstLine(err.Error()), now, w)
		return nil, err
	}
	if !d.Hosted {
		settleDevTelemetryLocal(projectDir, env, d.Reason, now, w)
		// Said only when there was a hosted path to fall back FROM: a
		// standalone project with no control plane has nothing to hear.
		printLocalReplayAvailability(e, facts, mode, d.Reason, w)
		return nil, nil
	}
	session.mu.Lock()
	snapshot := session.session
	session.mu.Unlock()
	run := &devTelemetryRun{
		projectDir: projectDir, env: env, project: target.Project,
		client: target.Client, sessionID: snapshot.ID,
		retention: facts.Retention, grant: d.Grant, session: snapshot,
		sessionReplay: facts.SessionReplay,
		frontends:     e.Frontends, log: w,
	}
	if ep, eerr := projectEndpoint(env, decl); eerr == nil {
		run.endpoint = ep
	}
	// Recorded the moment it is decided, before the tokens are written.
	if rerr := recordDevTelemetryDecision(projectDir, env, d, now); rerr != nil {
		fmt.Fprintf(w, "[up] note: could not record the telemetry decision in %s: %v\n", devtelemetry.Path(projectDir, env), rerr)
	}
	if err := run.write(d.Grant, now); err != nil {
		reason := "could not write the telemetry tokens: " + err.Error()
		settleDevTelemetryLocal(projectDir, env, reason, now, w)
		if mode.Value == devTelemetryHosted {
			return nil, fmt.Errorf("%s: write the telemetry tokens under %s: %w", mode, devtelemetry.HeadersPath(projectDir), err)
		}
		fmt.Fprintf(w, "[up] telemetry: local (%s)\n", reason)
		return nil, nil
	}
	if !devtelemetry.NoticeShown(projectDir) {
		printDevTelemetryNotice(w, env, d.Grant)
		_ = devtelemetry.MarkNoticeShown(projectDir, now)
	}
	skipped := applyHostedDevTelemetry(e, projectDir, d.Grant, os.Environ())
	if facts.SessionReplay && !d.Grant.SessionReplay {
		fmt.Fprintln(w, "[up] session recording: unavailable (the hosted dev telemetry server did not acknowledge sessionReplay); capture is off, ordinary telemetry continues")
	} else if d.Grant.SessionReplay {
		fmt.Fprintln(w, "[up] session recording: masked, subject to DNT/GPC, consent and sampling; set _session_replay = False in this env to stop capture and ingestion")
	}
	fmt.Fprintf(w, "[up] telemetry: hosted (org %s, instance %s, %s) → %s\n",
		d.Grant.OrgID, emptyAs(d.Grant.Instance.Label, d.Grant.Instance.ID), retentionLabel(d.Grant.Retention), d.Grant.Endpoint)
	if len(skipped) > 0 {
		fmt.Fprintf(w, "[up]   not starting the local observability service(s) %s: nothing is stored locally in this mode\n", strings.Join(skipped, ", "))
	}
	// The keeper (devtelemetry_keeper.go) heartbeats the session and renews
	// the tokens from here on; runUp starts it with the stack's processes.
	return run, nil
}

// settleDevTelemetryLocal records env's decision as local — first, before
// anything else can fail — and then removes whatever a previous hosted run
// left: the token state, the keeper spec, and the headers files. So `forge
// env status` and `forge obs` read "local" and the reason, and a local stack
// keeps no usable hosted token on disk.
func settleDevTelemetryLocal(projectDir, env, reason string, now time.Time, w io.Writer) {
	// A keeper a previous hosted run left running would renew the tokens
	// and recreate the files cleared below, and keep the session live: stop
	// it first, through the ledger and the stop path `forge env down` uses,
	// and wait for it to exit. (Should that fail, the keeper still stops
	// itself at its next beat or renewal: it re-reads the record written
	// below — devTelemetryKeeperOwns.)
	if devTelemetryKeeperMayRun(projectDir, env) {
		if err := stopDevTelemetryKeeper(projectDir, env); err != nil {
			fmt.Fprintf(w, "[up] note: could not stop the previous telemetry keeper: %v\n", err)
		}
	}
	if err := recordDevTelemetryDecision(projectDir, env, devTelemetryDecision{Reason: reason}, now); err != nil {
		fmt.Fprintf(w, "[up] note: could not record the telemetry decision in %s: %v\n", devtelemetry.Path(projectDir, env), err)
	}
	if err := devtelemetry.ClearCredentials(projectDir, env); err != nil {
		fmt.Fprintf(w, "[up] note: could not remove a previous run's telemetry tokens: %v\n", err)
	}
	if err := os.Remove(devTelemetryKeeperSpecPath(projectDir, env)); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(w, "[up] note: could not remove %s: %v\n", devTelemetryKeeperSpecPath(projectDir, env), err)
	}
}

// devTelemetryCredential reports whether a Reliant credential for the env's
// control plane exists. A seam: tests state the answer.
var devTelemetryCredential = func(env string, decl *cloud.Declaration) error {
	ep, err := projectEndpoint(env, decl)
	if err != nil {
		return err
	}
	_, err = cloud.ResolveCredential("", ep)
	return err
}
