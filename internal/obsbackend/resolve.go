package obsbackend

import (
	"fmt"
	"strings"

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
	// ControlPlane is the env's control_plane endpoint; "" when it declares none.
	ControlPlane string
	// ReleaseHealth is the env's recorded release-health policy; nil means
	// every default.
	ReleaseHealth *release.ShapeReleaseHealth
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
		if f.ControlPlane == "" {
			return Decision{}, Undetermined("--backend cloud: env %q declares no control_plane, so there is no control plane to query", f.Env)
		}
		return Decision{Backend: BackendCloud, Reason: "--backend cloud"}, nil
	}

	if !f.Declared || !f.Enabled {
		return Decision{}, Undetermined("telemetry is off for %s: its observability is %s", f.Env, offWhy(f)).
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
	return Decision{Backend: BackendLocal, Reason: "no destination: host processes feed the local ClickStack"}, nil
}

func offWhy(f Facts) string {
	if !f.Declared {
		return "not declared"
	}
	return "disabled"
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
	return Filter{}, Wrong("--filter %q: use key=value or key!=value (keys: %s)", s, filterKeysHint())
}

func filterKeysHint() string {
	keys := obsquery.AllowedKeys()
	const max = 8
	if len(keys) > max {
		return fmt.Sprintf("%s, … %d more; see `forge obs query --help`", strings.Join(keys[:max], ", "), len(keys)-max)
	}
	return strings.Join(keys, ", ")
}
