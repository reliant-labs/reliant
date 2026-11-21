package cli

// DECLARED TELEMETRY ON A LOCAL STACK (forge.Telemetry; docs/proposals/
// third-party-telemetry.md §3.3, §3.4).
//
// `forge env up` collects what the env declares for the systems it runs on
// the developer's machine — HostInfra engines, forge.OnHost processes,
// compose services on their published ports, Observability `address`
// targets — into the env's local collector. The signals split by who can
// reach them:
//
//   - PULLED signals (metrics scrapes, pprof pulls, checks) are the local
//     telemetry agent's: an otelcol-contrib host process
//     (internal/hostinfra/telemetryagent.go), because the engines bind
//     127.0.0.1, which no container reaches. Its configuration is rendered
//     here, on every run, by pkg/telemetry/collector from the env's targets,
//     and it exports OTLP to the local collector's published port (and
//     profiles to the profiles collector's). No pulled target, no agent.
//   - LOGS are already tailed by the local collector (the ClickStack
//     container's filelog/forge, or the forwarder's filelog) from
//     .forge/logs/<env>/<name>.log. A target's declared parse is spliced into
//     that chain: the forwarder's configuration is rendered here anyway, so it
//     gets the pieces directly; deploy/observability/otel-collector.yaml is
//     the project's own file, written once, so it INCLUDES them from
//     clickstack.TelemetryIncludeDir, which this file rewrites on every run
//     (a noop when nothing parses). A project whose collector file predates
//     the include is told so, and how to adopt it.
//
// None of it fails `forge env up`: telemetry that cannot be collected is
// reported, and the stack comes up.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/clickstack"
	"github.com/reliant-labs/forge/internal/hostinfra"
	"github.com/reliant-labs/forge/pkg/deploy"
	"github.com/reliant-labs/forge/pkg/telemetry"
	"github.com/reliant-labs/forge/pkg/telemetry/collector"
)

// The agent's own component names.
const (
	agentLocalExporter    = "otlphttp/forge_local"
	agentProfilesExporter = "otlphttp/forge_profiles"
	agentIsolatedExporter = "otlphttp/forge_isolated"
	// profilesFeatureGate lets the pinned collector load profiles pipelines.
	profilesFeatureGate = "service.profilesSupport"
)

// Seams, so the orchestration is testable without a collector binary.
var (
	startTelemetryAgentFn = hostinfra.StartTelemetryAgent
	stopTelemetryAgentFn  = hostinfra.StopTelemetryAgent
)

// hostTelemetryTargets are the env's declared targets the local stack
// reaches: host processes, host-run engines and addresses, and compose
// services on their published loopback ports.
func hostTelemetryTargets(e *KCLEntities) []telemetry.Target {
	if e == nil {
		return nil
	}
	var out []telemetry.Target
	for _, t := range e.Telemetry {
		if t.Runtime == telemetry.RuntimeHost || t.Runtime == telemetry.RuntimeCompose {
			out = append(out, t)
		}
	}
	return out
}

// telemetryLogs is the logs half of a local stack's declared telemetry: what
// the local collector's filelog chain splices in.
type telemetryLogs struct {
	// Preserve is the filelog operator (nil when nothing parses).
	Preserve map[string]any
	// Processors are the definitions of Pipeline's processors.
	Processors map[string]any
	// Pipeline is collector.Fragment.Logs.Processors, in order.
	Pipeline []string
	// Parsed names the services whose lines are parsed.
	Parsed []string
	// Warnings name what was not spliced.
	Warnings []string
}

// localTelemetryLogs renders the logs half for the env's host targets. The
// local collector tails every env's log directory and takes
// deployment.environment.name from the path, so the env's resource
// attributes are not stamped here.
func localTelemetryLogs(e *KCLEntities) (telemetryLogs, error) {
	targets := hostTelemetryTargets(e)
	var out telemetryLogs
	if len(targets) == 0 {
		return out, nil
	}
	// The pulled half is rendered for the agent, not here: its exporters
	// and mount root are placeholders Build needs and nothing reads.
	f, err := collector.Build(targets, collector.Options{
		Runtime:           telemetry.RuntimeHost,
		MountRoot:         "/unused",
		MetricsExporters:  []string{"unused"},
		ProfilesExporters: []string{"unused"},
		Isolated:          &collector.IsolatedExporter{Name: "otlphttp/unused", Config: map[string]any{"endpoint": "http://127.0.0.1:1"}},
	})
	if err != nil {
		return out, err
	}
	out.Preserve = f.Logs.Preserve
	out.Pipeline = f.Logs.Processors
	out.Processors = map[string]any{}
	for _, name := range f.Logs.Processors {
		out.Processors[name] = logsOnly(f.Processors[name])
	}
	for _, t := range targets {
		if t.Logs != nil {
			out.Parsed = append(out.Parsed, t.ServiceName)
		}
	}
	for _, w := range f.Warnings {
		if strings.Contains(w, "logs not parsed") {
			out.Warnings = append(out.Warnings, w)
		}
	}
	return out, nil
}

// logsOnly is a transform processor's config without the statements for
// other signals: the local collector runs it in its logs pipeline only, and
// ClickStack's own collector build loads no profiles support.
func logsOnly(proc any) any {
	m, ok := proc.(map[string]any)
	if !ok {
		return proc
	}
	out := map[string]any{}
	for k, v := range m {
		if k != "metric_statements" && k != "profile_statements" && k != "trace_statements" {
			out[k] = v
		}
	}
	return out
}

// telemetryIncludes are the files the ClickStack collector's filelog chain
// includes (otel-collector.yaml.tmpl): the preserve operator and the two
// processors, each a no-op when nothing is declared, so the file the
// collector loads always exists and always loads.
func telemetryIncludes(l telemetryLogs) (map[string][]byte, error) {
	noop := map[string]any{"error_mode": "ignore"}
	preserve := l.Preserve
	if preserve == nil {
		preserve = map[string]any{"type": "noop", "id": "forge_telemetry_original"}
	}
	identity, parse := any(noop), any(noop)
	if p, ok := l.Processors[collector.IdentityProcessor]; ok {
		identity = p
	}
	if p, ok := l.Processors[collector.LogsProcessor]; ok {
		parse = p
	}
	out := map[string][]byte{}
	for name, doc := range map[string]any{
		clickstack.TelemetryPreserveFile: preserve,
		clickstack.TelemetryIdentityFile: identity,
		clickstack.TelemetryParseFile:    parse,
	} {
		b, err := yaml.Marshal(doc)
		if err != nil {
			return nil, err
		}
		out[name] = append([]byte("# Written by `forge env up` from the env's forge.Telemetry declarations; included by\n# deploy/observability/otel-collector.yaml. Regenerated on every run.\n"), b...)
	}
	return out, nil
}

// writeTelemetryIncludes writes the include files and reports whether one a
// running collector may have loaded changed (it must then reload them); a
// file written for the first time is read when the collector starts.
func writeTelemetryIncludes(projectDir string, l telemetryLogs) (bool, error) {
	files, err := telemetryIncludes(l)
	if err != nil {
		return false, err
	}
	dir := filepath.Join(projectDir, clickstack.TelemetryIncludeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	changed := false
	for _, name := range sortedKeys(files) {
		p := filepath.Join(dir, name)
		cur, readErr := os.ReadFile(p) // #nosec G304 -- forge's own file
		if readErr == nil && bytes.Equal(cur, files[name]) {
			continue
		}
		if err := atomicfile.WriteFile(p, files[name], 0o644); err != nil {
			return false, err
		}
		changed = changed || readErr == nil
	}
	return changed, nil
}

// forwarderLogs is l as the forwarder splices it.
func (l telemetryLogs) forwarderLogs() deploy.ForwarderLogs {
	return deploy.ForwarderLogs{Preserve: l.Preserve, Processors: l.Processors, Pipeline: l.Pipeline}
}

// collectorIncludesTelemetry reports whether the project's ClickStack
// collector file includes the declared log parsing (scaffolded since this
// forge; older files lack it until `forge project upgrade` or a hand edit).
func collectorIncludesTelemetry(projectDir string) bool {
	b, err := os.ReadFile(filepath.Join(projectDir, clickstack.CollectorConfig)) // #nosec G304 -- the project's own file
	if err != nil {
		return false
	}
	return strings.Contains(string(b), clickstack.TelemetryIncludeMount+"/"+clickstack.TelemetryParseFile)
}

// reloadLocalCollector makes the running local collector re-read its
// configuration after env up rewrote an input of it. The collector reloads
// on SIGHUP, re-resolving every ${file:} it includes. A collector that is not
// running yet reads the new files when it starts, so a miss is not an error.
func reloadLocalCollector(ctx context.Context, projectDir, env, svc string) {
	args := localClickstackComposeArgs(ctx, projectDir, env)
	switch svc {
	case clickstack.Service:
		// The ClickStack collector runs under its OpAMP supervisor, PID 1 is
		// tini: signal the collector process itself.
		args = append(args, "exec", "-T", svc, "sh", "-c", `pid=$(pgrep -f '^/otelcontribcol'); [ -z "$pid" ] || kill -HUP $pid`)
	case clickstack.ForwarderService:
		args = append(args, "kill", "-s", "HUP", svc)
	default:
		return
	}
	if out, err := doctorCompose(ctx, projectDir, args...); err != nil && len(out) > 0 {
		fmt.Fprintf(os.Stderr, "  telemetry: the local collector did not reload its configuration (%v); it applies at its next restart\n", err)
	}
}

// localTelemetryAgent is the agent the env's targets need, or nil when no
// target is pulled.
type localTelemetryAgent struct {
	Agent hostinfra.TelemetryAgent
	// Pulled are the targets the agent collects, in declaration order.
	Pulled []telemetry.Target
	// Mounts are the credentials the agent reads, by path under the
	// secrets directory.
	Mounts []telemetry.SecretMount
	// Warnings name what Build skipped.
	Warnings []string
}

// planTelemetryAgent renders the agent's configuration for the env's host
// targets. nil when no target declares a scrape, pull or check.
func planTelemetryAgent(projectDir, env string, e *KCLEntities) (*localTelemetryAgent, error) {
	var pulled []telemetry.Target
	for _, t := range hostTelemetryTargets(e) {
		if t.Pulled() {
			pulled = append(pulled, t)
		}
	}
	if len(pulled) == 0 {
		return nil, nil
	}
	o := e.Observability
	if o == nil || o.LocalEndpoint == "" {
		return nil, fmt.Errorf("the env declares no local collector endpoint (observability.local_port) for the agent to export to")
	}
	profilesEndpoint := o.ProfilesEndpoint
	if profilesEndpoint == "" && o.Destination != nil && o.Signals.Profiles {
		// The forwarder takes profiles on its OTLP port.
		profilesEndpoint = strings.TrimSuffix(o.LocalEndpoint, "/") + "/v1development/profiles"
	}
	opts := collector.Options{
		Runtime: telemetry.RuntimeHost,
		// Logs are the local collector's (localTelemetryLogs): the agent
		// renders none.
		Discovery:        collector.DiscoverAddresses,
		MountRoot:        hostinfra.TelemetryAgentSecretsDir(projectDir, env),
		ResourceAttrs:    map[string]string{"deployment.environment.name": env},
		EnableProfiles:   profilesEndpoint != "",
		Processors:       []string{"memory_limiter"},
		PostProcessors:   []string{"batch"},
		MetricsExporters: []string{agentLocalExporter},
		Isolated: &collector.IsolatedExporter{
			Name:   agentIsolatedExporter,
			Config: map[string]any{"endpoint": o.LocalEndpoint},
		},
	}
	if opts.EnableProfiles {
		opts.ProfilesExporters = []string{agentProfilesExporter}
	}
	f, err := collector.Build(pulled, opts)
	if err != nil {
		return nil, err
	}
	exporters := map[string]any{agentLocalExporter: map[string]any{"endpoint": o.LocalEndpoint}}
	var gates []string
	if opts.EnableProfiles {
		exporters[agentProfilesExporter] = map[string]any{"profiles_endpoint": profilesEndpoint}
		gates = append(gates, profilesFeatureGate)
	}
	cfg := map[string]any{
		"processors": map[string]any{
			"memory_limiter": map[string]any{"check_interval": "1s", "limit_mib": 200, "spike_limit_mib": 50},
			"batch":          map[string]any{"send_batch_size": 8192, "timeout": "5s"},
		},
		"exporters": exporters,
	}
	if err := f.MergeInto(cfg); err != nil {
		return nil, err
	}
	plan := &localTelemetryAgent{
		Agent:    hostinfra.TelemetryAgent{Env: env, Config: cfg, FeatureGates: gates},
		Pulled:   pulled,
		Mounts:   agentMounts(pulled),
		Warnings: f.Warnings,
	}
	return plan, nil
}

// agentMounts are the credentials the pulled targets read, de-duplicated by
// path.
func agentMounts(targets []telemetry.Target) []telemetry.SecretMount {
	seen := map[string]bool{}
	var out []telemetry.SecretMount
	add := func(m *telemetry.SecretMount) {
		if m == nil || seen[m.Path] {
			return
		}
		seen[m.Path] = true
		out = append(out, *m)
	}
	ca := func(t *telemetry.TLS) *telemetry.SecretMount {
		if t == nil {
			return nil
		}
		return t.CA
	}
	for _, t := range targets {
		for _, m := range t.Metrics {
			add(m.BearerToken)
			add(ca(m.TLS))
		}
		for _, p := range t.Profiles {
			add(p.BearerToken)
			add(ca(p.TLS))
		}
	}
	return out
}

// agentSecrets is the agent's next credential set, staged beside the live
// one (<dir>.new) by stageAgentSecrets, so the set a running agent reads is
// never touched until its replacement is complete: swap puts it in place
// (keeping the previous set at <dir>.prev), rollback restores the previous
// set when the agent did not start on the new one, and commit drops it.
type agentSecrets struct {
	dir string
	// revision fingerprints the set's paths and contents
	// (hostinfra.TelemetryAgent.CredentialsRevision): the agent reads its
	// credentials at start, so another revision restarts it.
	revision string
}

// stageAgentSecrets resolves each mount's value from the env's secret store
// and writes the whole set to <dir>.new, each at <path>, 0600. A credential
// the declaration dropped is not in the new set, so it does not linger. A
// host target's credential comes from the store (SecretRef.store_key); one
// without a store key, or whose key the store lacks, is an error naming it —
// unless it is optional, then skipped. Every value is resolved before
// anything is written, and nothing at dir changes here, so an error leaves
// the running agent's credentials as they were.
func stageAgentSecrets(dir string, mounts []telemetry.SecretMount, store map[string]string, env string) (*agentSecrets, error) {
	values := map[string]string{}
	var missing []string
	for _, m := range mounts {
		v, ok := store[m.StoreKey]
		if m.StoreKey == "" || !ok {
			if m.Optional {
				continue
			}
			if m.StoreKey == "" {
				missing = append(missing, fmt.Sprintf("%s (a Secret reference with no store_key: a local env reads credentials from its secret store)", m.Path))
			} else {
				missing = append(missing, fmt.Sprintf("%s (store key %s: `%s secret set --env %s %s`)", m.Path, m.StoreKey, Name(), env, m.StoreKey))
			}
			continue
		}
		values[m.Path] = v
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("credentials the scrapes need are not in the env's secret store:\n    %s", strings.Join(missing, "\n    "))
	}
	paths := make([]string, 0, len(values))
	for p := range values {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	staged := dir + ".new"
	if err := os.RemoveAll(staged); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(staged, 0o700); err != nil {
		return nil, err
	}
	sum := sha256.New()
	for _, rel := range paths {
		p := filepath.Join(staged, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil, err
		}
		if err := atomicfile.WriteFile(p, []byte(values[rel]), 0o600); err != nil {
			return nil, err
		}
		fmt.Fprintf(sum, "%d:%s%d:%s", len(rel), rel, len(values[rel]), values[rel])
	}
	rev := ""
	if len(paths) > 0 {
		rev = "sha256:" + hex.EncodeToString(sum.Sum(nil))
	}
	return &agentSecrets{dir: dir, revision: rev}, nil
}

// swap makes the staged set the live one, keeping the previous at <dir>.prev.
func (s *agentSecrets) swap() error {
	prev := s.dir + ".prev"
	if err := os.RemoveAll(prev); err != nil {
		return err
	}
	if err := os.Rename(s.dir, prev); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(s.dir+".new", s.dir); err != nil {
		_ = os.Rename(prev, s.dir)
		return err
	}
	return nil
}

// rollback puts the previous set back: the agent did not start on the new
// one, so the agent still running (if any) keeps the credentials it reads.
func (s *agentSecrets) rollback() {
	prev := s.dir + ".prev"
	if _, err := os.Stat(prev); err != nil {
		return
	}
	_ = os.RemoveAll(s.dir)
	_ = os.Rename(prev, s.dir)
}

// commit drops the previous set once the agent runs on the new one.
func (s *agentSecrets) commit() { _ = os.RemoveAll(s.dir + ".prev") }

// upLocalTelemetry converges the env's local telemetry agent: started (or
// adopted, or restarted) with this run's configuration when a host target is
// pulled, stopped when none is. It runs after the infrastructure is up, so
// the local collector it exports to is serving. Best-effort: every problem is
// printed, never returned.
func upLocalTelemetry(ctx context.Context, w io.Writer, projectDir, env string, e *KCLEntities, store map[string]string) {
	targets := hostTelemetryTargets(e)
	svc := localCollectorService(e)
	if logs, err := localTelemetryLogs(e); err == nil && len(logs.Parsed) > 0 {
		switch {
		case svc == clickstack.Service && !collectorIncludesTelemetry(projectDir):
			fmt.Fprintf(w, "  telemetry: ⚠ the declared log parsing for %s is not applied: %s predates the include that loads it.\n"+
				"    Adopt it with `%s project upgrade`, or add to filelog/forge's operators (after the move of attributes.env)\n"+
				"      - ${file:%s/%s}\n"+
				"    to its processors section\n"+
				"      transform/telemetry_identity: ${file:%s/%s}\n"+
				"      transform/telemetry_logs: ${file:%s/%s}\n"+
				"    and both, in that order, to logs/forge's processors before batch.\n",
				strings.Join(logs.Parsed, ", "), clickstack.CollectorConfig, Name(),
				clickstack.TelemetryIncludeMount, clickstack.TelemetryPreserveFile,
				clickstack.TelemetryIncludeMount, clickstack.TelemetryIdentityFile,
				clickstack.TelemetryIncludeMount, clickstack.TelemetryParseFile)
		case svc != "":
			fmt.Fprintf(w, "  telemetry: log lines parsed as declared for %s (in the local collector)\n", strings.Join(logs.Parsed, ", "))
		}
		for _, warn := range logs.Warnings {
			fmt.Fprintf(w, "  telemetry: %s\n", warn)
		}
	}

	plan, err := planTelemetryAgent(projectDir, env, e)
	stop := func(why string) {
		if stopped, serr := stopTelemetryAgentFn(projectDir, env); serr != nil {
			fmt.Fprintf(w, "  telemetry agent: stop: %v\n", serr)
		} else if stopped {
			fmt.Fprintf(w, "  telemetry agent: stopped (%s)\n", why)
		}
	}
	switch {
	case plan == nil && err == nil:
		stop("no host target declares a scrape, pull or check")
		return
	case svc == "":
		// No local collector: hosted dev telemetry, or observability off.
		fmt.Fprintf(w, "  telemetry: ⚠ not collecting %s: this run has no local collector to send to (hosted dev telemetry, or observability off)\n", serviceNames(pulledOf(targets)))
		stop("no local collector")
		return
	case err != nil:
		fmt.Fprintf(w, "  telemetry agent: ⚠ not started: %v\n", err)
		return
	}
	for _, warn := range plan.Warnings {
		fmt.Fprintf(w, "  telemetry agent: %s\n", warn)
	}
	// The new credentials replace the live ones only once all of them are
	// written, and go back if the agent does not start on them: a running
	// agent is left with the set it was started with, never a partial one.
	secrets, err := stageAgentSecrets(hostinfra.TelemetryAgentSecretsDir(projectDir, env), plan.Mounts, store, env)
	if err == nil {
		err = secrets.swap()
	}
	if err != nil {
		fmt.Fprintf(w, "  telemetry agent: ⚠ not started: %v\n", err)
		return
	}
	plan.Agent.CredentialsRevision = secrets.revision
	res, err := startTelemetryAgentFn(ctx, projectDir, plan.Agent)
	if err != nil {
		secrets.rollback()
		fmt.Fprintf(w, "  telemetry agent: ⚠ %v\n", err)
		return
	}
	secrets.commit()
	fmt.Fprintf(w, "  telemetry agent: %s (pid %d, otelcol-contrib %s) → %s\n", res.State, res.PID, hostinfra.OtelcolContribVersion, collectorLabel(svc))
	for _, t := range plan.Pulled {
		fmt.Fprintf(w, "    collecting %s: %s\n", t.ServiceName, strings.Join(pulledSignals(t), ", "))
	}
	fmt.Fprintf(w, "    config %s · log %s\n", shortRel(projectDir, res.ConfigPath), shortRel(projectDir, res.LogPath))
}

// stopLocalTelemetryAgent is `forge env down`'s half.
func stopLocalTelemetryAgent(projectDir, env string) (bool, error) {
	return stopTelemetryAgentFn(projectDir, env)
}

func collectorLabel(svc string) string {
	if svc == clickstack.ForwarderService {
		return "the local forwarder"
	}
	return "the local ClickStack"
}

// pulledSignals names what the agent pulls from t, with the ports.
func pulledSignals(t telemetry.Target) []string {
	var out []string
	for _, m := range t.Metrics {
		out = append(out, fmt.Sprintf("metrics %s:%d%s", t.Address, m.Port, m.Path))
	}
	for _, p := range t.Profiles {
		out = append(out, fmt.Sprintf("profiles %s:%d (%s)", t.Address, p.Port, strings.Join(p.Types, ", ")))
	}
	for _, c := range t.Checks {
		out = append(out, fmt.Sprintf("%s check :%d", c.Kind, c.Port))
	}
	return out
}

func pulledOf(targets []telemetry.Target) []telemetry.Target {
	var out []telemetry.Target
	for _, t := range targets {
		if t.Pulled() {
			out = append(out, t)
		}
	}
	return out
}

func serviceNames(targets []telemetry.Target) string {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.ServiceName
	}
	return strings.Join(names, ", ")
}

// shortRel is p relative to the project, for a message.
func shortRel(projectDir, p string) string {
	if rel, err := filepath.Rel(projectDir, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return p
}
