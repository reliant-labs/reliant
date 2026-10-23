package obsbackend

// The cloud backend: an env whose telemetry the Reliant control plane stores
// is read through its ObservabilityService, by procedure name, with locally
// declared JSON shapes (forge vendors none of the control plane's protos; see
// internal/cloud's package header). The control plane compiles every request
// with the same pkg/obsquery the local backend runs, so a verb means the same
// on both.
//
// Every request is bounded to one environment (env_ids = [EnvID]) of one
// organization; the env id already names its project. The control plane's read gate enforces the org,
// the env and the caller's authority (membership, or obs:read on a machine
// token); this side only says what it wants.

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// Caller is what the cloud backend needs from a control-plane client.
// *cloud.Client satisfies it; tests substitute a fake.
type Caller interface {
	Call(ctx context.Context, procedure string, req, out any) error
}

// Control-plane procedures the cloud backend calls.
const (
	procSearchLogs  = "controlplane.v1.ObservabilityService/SearchLogs"
	procListTraces  = "controlplane.v1.ObservabilityService/ListTraces"
	procGetTrace    = "controlplane.v1.ObservabilityService/GetTrace"
	procListMetrics = "controlplane.v1.ObservabilityService/ListMetrics"
	procQueryMetric = "controlplane.v1.ObservabilityService/QueryMetric"
	procQueryPanel  = "controlplane.v1.ObservabilityService/QueryPanel"
)

// Cloud reads one env's telemetry from the control plane.
type Cloud struct {
	Env    string
	EnvID  string
	OrgID  string
	Caller Caller
}

// Info implements Backend.
func (c Cloud) Info() Info { return Info{Env: c.Env, Backend: BackendCloud, EnvID: c.EnvID} }

// call invokes procedure and maps a failure onto the exit table. Every cloud
// read in this package (the Backend methods, and the issue and release-health
// readers beside them) goes through it, so every one classifies its errors one
// way.
func (c Cloud) call(ctx context.Context, procedure string, req, out any) error {
	if c.Caller == nil {
		return Undetermined("no control-plane client for env %s", c.Env)
	}
	return MapCloudError(c.Caller.Call(ctx, procedure, req, out))
}

// MapCloudError classifies a control-plane failure (DESIGN §10): exit 1 when
// the request is wrong (InvalidArgument, NotFound, OutOfRange, Aborted, a
// refused policy), exit 2 when forge could not look (Unavailable,
// Unauthenticated, PermissionDenied, ResourceExhausted, FailedPrecondition,
// DeadlineExceeded, a transport failure). A refused read is "could not look":
// a gate must never read it as a pass.
func MapCloudError(err error) error {
	if err == nil {
		return nil
	}
	var be *Error
	if errors.As(err, &be) {
		return err
	}
	var ce *cloud.Error
	if !errors.As(err, &ce) {
		return Undetermined("the control plane could not be reached").Cause(err)
	}
	switch ce.Code {
	case "invalid_argument", "not_found", "out_of_range", "aborted", "already_exists":
		return (&Error{Code: ExitWrong}).Cause(err)
	case "resource_exhausted":
		return Undetermined("the control plane's query quota is exhausted; retry later").Cause(err)
	}
	return (&Error{Code: ExitUndetermined}).Cause(err)
}

// ── wire shapes shared by every request ─────────────────────────────────

type wireTimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type wireAttrFilter struct {
	Key   string `json:"key"`
	Op    string `json:"op,omitempty"`
	Value string `json:"value,omitempty"`
}

type wireScope struct {
	OrgID     string           `json:"orgId"`
	EnvIDs    []string         `json:"envIds,omitempty"`
	Services  []string         `json:"services,omitempty"`
	TimeRange wireTimeRange    `json:"timeRange"`
	Filters   []wireAttrFilter `json:"filters,omitempty"`
}

// scope is the QueryScope of a request narrowed by n, bounded to this env.
func (c Cloud) scope(n Narrow) wireScope {
	s := wireScope{
		OrgID: c.OrgID, Services: n.Services,
		TimeRange: wireTimeRange{Start: n.Range.From.UTC(), End: n.Range.To.UTC()},
	}
	if c.EnvID != "" {
		s.EnvIDs = []string{c.EnvID}
	}
	for _, f := range n.Filters {
		op := "ATTRIBUTE_OP_EQ"
		if f.Not {
			op = "ATTRIBUTE_OP_NEQ"
		}
		s.Filters = append(s.Filters, wireAttrFilter{Key: f.Key, Op: op, Value: f.Value})
	}
	return s
}

// explainer compiles a request the way the control plane will, without
// running it: --explain on a cloud env prints the statements the shared
// compiler produces, against the local database name (the control plane
// substitutes the org's own).
func (c Cloud) explainer() Local {
	return Local{Env: c.Env, Runner: refuseRun{}}
}

// refuseRun is the Runner of an explain-only compile: nothing may run.
type refuseRun struct{}

func (refuseRun) Run(context.Context, obsquery.Stmt, Limits) (Result, error) {
	return Result{}, Undetermined("--explain compiles the request without running it; drop --explain to run it on the control plane")
}

// explained marks statements compiled locally on a cloud env's behalf.
func explained(st []Statement) []Statement {
	out := make([]Statement, len(st))
	for i, s := range st {
		s.Purpose += " (compiled locally; the control plane runs it against the org's own database)"
		out[i] = s
	}
	return out
}
