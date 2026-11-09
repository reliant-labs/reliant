package obsbackend

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/devtelemetry"
	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/release"
)

// Facts are what an env's resolved KCL says about where its telemetry goes
// (`forge env shape`): the whole input of [Resolve]. The CLI extracts them from
// the rendered entities, so this package never learns the render's types.
type Facts struct {
	Env string
	// Declared is false when the env has no observability block at all;
	// Enabled is the block's own switch.
	Declared, Enabled bool
	// Destination is the env's declared telemetry destination; nil sends
	// host processes to the local ClickStack.
	Destination *Destination
	// HasHosted is true when anything in the env is bound to forge.OnHosted.
	HasHosted bool
	// HasLocal is true when a workload of the env runs on this machine (host
	// process or docker compose): only then can the local ClickStack hold its
	// telemetry. An env whose workloads all deploy to a cluster has none.
	HasLocal bool
	// HasCluster is true when a workload deploys to a declared cluster.
	// Without local workloads, its services are reached through ControlPlane,
	// not through this machine's ClickStack. It does not imply hosted runtime.
	HasCluster bool
	// NoLocalStore is true for a container-free env: it declares no local
	// ClickStack and no collector port (host processes and forge.HostInfra
	// only), so nothing on this machine holds its telemetry. Its local
	// fallback exports nothing, and a local query has nothing to read.
	NoLocalStore bool
	// LocalLifecycle is the env's explicit local/ephemeral lifecycle. Local
	// host processes may share that scratch env with a developer's cluster.
	LocalLifecycle bool
	// ControlPlane is the env's control_plane endpoint; "" when it declares none.
	ControlPlane string
	// ReleaseHealth is the env's recorded release-health policy; nil means
	// every default.
	ReleaseHealth *release.ShapeReleaseHealth
	// DevTelemetry is where `forge env up` decided this env's telemetry goes
	// (.forge/obs/backend-<env>.json); nil when it recorded nothing. A
	// recorded "cloud" is read back rather than re-derived from whoever is
	// signed in now.
	DevTelemetry *devtelemetry.Record
}

// Destination is Observability.destination.
type Destination struct {
	Endpoint string
	// TokenFromControlPlane marks the destination as the Reliant control
	// plane's own telemetry intake.
	TokenFromControlPlane bool
}

// Decision is the backend an env resolves to and why.
type Decision struct {
	Backend string
	// Reason is one line a human header can show: "no destination: host
	// processes feed the local ClickStack".
	Reason string
	// HostedDev is set when a local env's telemetry goes to the hosted store
	// (`forge env up` recorded dev_telemetry=hosted): the record names the
	// org, the environment id and the instance the reads are bound to, so the
	// cloud backend is built from it rather than from a hosted-env lookup.
	HostedDev *devtelemetry.Record
}

// recordedCloud is what devtelemetry.Record.Backend says for the hosted store.
const recordedCloud = "cloud"

// hostedDevRecord is f's recorded hosted-dev decision, or nil: a record
// counts only for an env that still runs on this machine.
func hostedDevRecord(f Facts) *devtelemetry.Record {
	if r := f.DevTelemetry; r != nil && r.Backend == recordedCloud && f.HasLocal {
		return r
	}
	return nil
}

// hostedDev resolves an env whose `forge env up` sent its telemetry to the
// hosted store: the cloud backend, bound to the record's org, environment and
// instance.
func hostedDev(f Facts, r *devtelemetry.Record, prefix string) (Decision, error) {
	if f.ControlPlane == "" {
		return Decision{}, Undetermined("env %s's telemetry goes to the hosted store (as `forge env up` recorded it) but the env declares no control_plane to query it through", f.Env)
	}
	// Every field the reads are bound by is required: a record without its
	// instance would read every instance of the env instead of this one.
	if r.OrgID == "" || r.EnvironmentID == "" || r.InstanceID == "" {
		return Decision{}, Undetermined("the hosted-telemetry record of env %s (%s) names no org, environment or instance", f.Env, ".forge/obs/backend-"+f.Env+".json").
			With(Cmd("forge", "env", "up", f.Env))
	}
	who := r.InstanceLabel
	if who == "" {
		who = r.InstanceID
	}
	return Decision{Backend: BackendCloud, Reason: prefix + "dev_telemetry=hosted: instance " + who, HostedDev: r}, nil
}

// Resolve applies the §4.2 table. override is --backend: "", "local" or
// "cloud". An override skips the table's refusals (the caller said which
// backend), but "cloud" still needs a control plane to talk to.
func Resolve(f Facts, override string) (Decision, error) {
	switch override {
	case "", BackendLocal, BackendCloud:
	default:
		return Decision{}, Wrong("--backend %q is not a backend; use %s or %s", override, BackendLocal, BackendCloud)
	}
	switch override {
	case BackendLocal:
		return Decision{Backend: BackendLocal, Reason: "--backend local"}, nil
	case BackendCloud:
		if r := hostedDevRecord(f); r != nil {
			return hostedDev(f, r, "--backend cloud: ")
		}
		if f.ControlPlane == "" {
			return Decision{}, Undetermined("--backend cloud: env %q declares no control_plane, so there is no control plane to query", f.Env)
		}
		return Decision{Backend: BackendCloud, Reason: "--backend cloud"}, nil
	}

	// Hosted observability is opt-out: the platform collects a hosted env's
	// telemetry unless the env declares `forge.Observability {enabled =
	// False}`, and a scaffolded prod/staging declares no block at all. So an
	// undeclared block means off only for an env nothing hosted runs in.
	if f.Declared && !f.Enabled {
		return Decision{}, Undetermined("telemetry is off for %s: its observability is disabled", f.Env).
			With("forge skill load observability")
	}
	// `forge env up` sent this env's telemetry to the hosted store.
	if r := hostedDevRecord(f); r != nil {
		return hostedDev(f, r, "")
	}
	if !f.Declared && !f.HasHosted {
		return Decision{}, Undetermined("telemetry is off for %s: its observability is not declared; "+
			"add `observability = forge.Observability {dev_telemetry = \"auto\"}` to the env, with a `control_plane` "+
			"to send it to the hosted store (without one it goes to a local ClickStack), then run `forge env up %s`", f.Env, f.Env).
			With("forge skill load observability")
	}
	if d := f.Destination; d != nil && !d.TokenFromControlPlane {
		return Decision{}, Undetermined("telemetry for %s goes to %s; forge can query the local ClickStack or the Reliant control plane only", f.Env, d.Endpoint)
	}
	toCloud := f.Destination != nil || f.HasHosted
	if toCloud {
		if f.ControlPlane == "" {
			return Decision{}, Undetermined("env %s sends telemetry to the Reliant control plane but declares no control_plane to query it through", f.Env)
		}
		why := "destination.token_from_control_plane"
		if f.Destination == nil {
			why = "a workload is bound to forge.OnHosted"
		}
		return Decision{Backend: BackendCloud, Reason: why + ": telemetry is stored by the control plane"}, nil
	}
	if f.HasLocal && f.NoLocalStore && !f.HasCluster {
		return Decision{}, noLocalStore(f)
	}
	if f.HasLocal && f.LocalLifecycle {
		return Decision{Backend: BackendLocal, Reason: "local lifecycle: host processes feed the env's ClickStack"}, nil
	}
	if f.HasCluster {
		if f.HasLocal {
			return Decision{}, Undetermined("env %s mixes local and cluster workloads but declares no telemetry destination; declare observability.destination so forge can query the whole env", f.Env).
				With("forge skill load observability/destination")
		}
		if f.ControlPlane == "" {
			return Decision{}, Undetermined("env %s runs on a cluster but declares no control_plane or telemetry destination forge can query", f.Env).
				With("forge skill load observability/destination")
		}
		return Decision{Backend: BackendCloud, Reason: "cluster workloads: query the env's declared control plane"}, nil
	}
	if !f.HasLocal {
		return Decision{}, Undetermined("env %s declares no runtime or telemetry destination forge can query; refusing to guess the local ClickStack", f.Env)
	}
	return Decision{Backend: BackendLocal, Reason: "no destination: host processes feed the local ClickStack"}, nil
}

// noLocalStore is the answer for a container-free env whose telemetry is
// local: there is nothing to query, and saying "start the ClickStack" would
// send the developer to a container `forge env up` never starts. So it says
// why it is local (the recorded reason, when `forge env up` wrote one), how
// to ship it to the hosted store, and how to opt into a local store instead.
func noLocalStore(f Facts) *Error {
	why := ""
	if r := f.DevTelemetry; r != nil && r.Reason != "" {
		why = " (" + r.Reason + ")"
	}
	hosted := "sign in to Reliant (`forge login`) and re-run `forge env up " + f.Env + "` to ship it to your org's hosted store"
	if f.ControlPlane == "" {
		hosted = "declare a `control_plane = forge.ControlPlane {}` on the env, sign in to Reliant (`forge login`) and re-run `forge env up " + f.Env + "` to ship it to your org's hosted store"
	}
	return Undetermined("telemetry for %s is local%s, and this env has no local store: it runs no container, so no ClickStack holds it and nothing was collected. "+
		"To read it, %s. Or opt into a local store where Docker runs: declare the scaffold's `clickstack` compose workload and set observability.local_port "+
		"(`forge skill load observability`), then `forge env up %s` (with --telemetry=local to keep it local while signed in)",
		f.Env, why, hosted, f.Env).
		With(Cmd("forge", "login"), Cmd("forge", "env", "up", f.Env))
}

// LocalScope is the scope of every local query: the project's own ClickStack
// (`default` database), narrowed to the env by deployment.environment.name.
// The env is a filter, not an environment restriction: local telemetry carries
// no reliant.env.id.
func LocalScope(n Narrow, env string) obsquery.Scope {
	sc := obsquery.Scope{
		Database: obsquery.LocalDatabase,
		Local:    true,
		Services: n.Services,
		Start:    n.Range.From,
		End:      n.Range.To,
	}
	if env != "" {
		sc.Filters = append(sc.Filters, obsquery.AttrFilter{Key: "deployment.environment.name", Op: "eq", Value: env})
	}
	for _, f := range n.Filters {
		op := "eq"
		if f.Not {
			op = "neq"
		}
		sc.Filters = append(sc.Filters, obsquery.AttrFilter{Key: f.Key, Op: op, Value: f.Value})
	}
	return sc
}

// ParseFilter reads a --filter value: key=value or key!=value.
func ParseFilter(s string) (Filter, error) {
	if k, v, ok := strings.Cut(s, "!="); ok && k != "" {
		return Filter{Key: k, Value: v, Not: true}, nil
	}
	if k, v, ok := strings.Cut(s, "="); ok && k != "" && !strings.HasSuffix(k, "!") {
		return Filter{Key: k, Value: v}, nil
	}
	return Filter{}, Wrong("--filter %q: use key=value or key!=value (keys: %s; or any attribute as span.attr.<k>, log.attr.<k> or resource.attr.<k>)", s, filterKeysHint())
}

func filterKeysHint() string {
	keys := obsquery.AllowedKeys()
	const max = 8
	if len(keys) > max {
		return fmt.Sprintf("%s, … %d more; see `forge obs query --help`", strings.Join(keys[:max], ", "), len(keys)-max)
	}
	return strings.Join(keys, ", ")
}
