package obsbackend

// Flamegraph: the control plane's aggregated profile of one service, the
// stacks the console draws as a flamegraph. The control plane writes them as
// speedscope JSON (pkg/profilequery Speedscope); they are read back here
// (ReadSpeedscope), so `forge obs profile <env>` writes a hosted env's profile
// as pprof, speedscope or collapsed stacks exactly as it writes a local one.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/profilequery"
)

const procGetFlamegraph = "controlplane.v1.ObservabilityService/GetFlamegraph"

// FlamegraphRequest selects one service's profile.
type FlamegraphRequest struct {
	// Service is the OTEL service name (required).
	Service string
	// SampleType is cpu, alloc_space, inuse_space, inuse_objects, goroutine, ...
	SampleType string
	// TraceID narrows to the samples whose goroutine served the trace.
	TraceID string
	// Env is the forge env name (deployment.environment.name) the samples were
	// recorded under. A control plane that predates the filter reads every
	// environment of the org: FlamegraphResult.EnvApplied says which happened.
	Env string
	// Range is the window; the control plane caps it at 30 days.
	Range Range
}

// FlamegraphResult is the aggregated profile. An empty window is not an
// error: Profile has no stacks.
type FlamegraphResult struct {
	// Profile holds the stacks, heaviest first, with Service, SampleType,
	// Unit and the range set.
	Profile profilequery.Profile
	// TotalValue is the control plane's sum over every stack it read.
	TotalValue int64
	// AvailableTypes are the sample types the service has in the range.
	AvailableTypes []string
	// Truncated is true when the control plane cut the stacks at its cap
	// (pkg/obsquery MaxStacks), keeping the heaviest.
	Truncated bool
	// EnvApplied is true when the control plane narrowed the stacks to
	// FlamegraphRequest.Env. False with an Env set means it read the service
	// in every environment of the org.
	EnvApplied bool
}

type wireFlamegraphRequest struct {
	Scope       wireScope `json:"scope"`
	Service     string    `json:"service"`
	ProfileType string    `json:"profileType"`
	TraceID     string    `json:"traceId,omitempty"`
	Env         string    `json:"env,omitempty"`
}

type wireFlamegraphResponse struct {
	SpeedscopeJSON        string      `json:"speedscopeJson"`
	TotalValue            json.Number `json:"totalValue"`
	Unit                  string      `json:"unit"`
	StackCount            int32       `json:"stackCount"`
	AvailableProfileTypes []string    `json:"availableProfileTypes"`
	Truncated             bool        `json:"truncated"`
	Env                   string      `json:"env"`
}

// Flamegraph reads one service's aggregated profile. Like ExportPGOProfile it
// is a whole-env read: profiles keep no dev instance, so the instance
// narrowing (if any) is not sent.
func (c Cloud) Flamegraph(ctx context.Context, req FlamegraphRequest) (FlamegraphResult, error) {
	if req.Service == "" {
		return FlamegraphResult{}, Wrong("a hosted flamegraph is one service's: name it with --service")
	}
	if req.SampleType == "" {
		return FlamegraphResult{}, Wrong("Flamegraph: no sample type named")
	}
	scope := c.scope(Narrow{Range: req.Range})
	scope.InstanceIDs, scope.InstanceUsers = nil, nil
	wire := wireFlamegraphRequest{Scope: scope, Service: req.Service, ProfileType: req.SampleType, TraceID: req.TraceID, Env: req.Env}
	empty := profilequery.Profile{Service: req.Service, SampleType: req.SampleType, From: req.Range.From, To: req.Range.To}
	var out wireFlamegraphResponse
	if err := c.callEnvWide(ctx, procGetFlamegraph, wire, &out); err != nil {
		// NotFound is the control plane's "this service has no profile in
		// the range": an answer, not a failure.
		var ce *cloud.Error
		if errors.As(err, &ce) && ce.Code == cloud.CodeNotFound {
			return FlamegraphResult{Profile: empty, EnvApplied: req.Env != ""}, nil
		}
		return FlamegraphResult{}, notServed(err, "flamegraphs", "GetFlamegraph")
	}
	prof := empty
	if strings.TrimSpace(out.SpeedscopeJSON) != "" {
		p, err := profilequery.ReadSpeedscope(bytes.NewReader([]byte(out.SpeedscopeJSON)))
		if err != nil {
			return FlamegraphResult{}, Undetermined("the control plane answered GetFlamegraph with a profile forge cannot read").Cause(err)
		}
		prof.Stacks = p.Stacks
	}
	prof.Unit = out.Unit
	if prof.Unit == "none" {
		prof.Unit = ""
	}
	res := FlamegraphResult{Profile: prof, AvailableTypes: out.AvailableProfileTypes, Truncated: out.Truncated,
		EnvApplied: req.Env != "" && out.Env == req.Env}
	if out.TotalValue != "" {
		n, err := out.TotalValue.Int64()
		if err != nil {
			return FlamegraphResult{}, Undetermined("the control plane's GetFlamegraph answer could not be read: totalValue %q", out.TotalValue).Cause(err)
		}
		res.TotalValue = n
	}
	return res, nil
}
