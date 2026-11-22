package obsbackend

// ExportPGOProfile: the control plane's merged CPU call edges of a set of
// services, already encoded as the pprof `go build -pgo` reads (pkg/profilequery
// WritePGO on the server, so hosted and local pulls produce identical bytes).
// `forge pgo pull` is the caller.

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const procExportPGOProfile = "controlplane.v1.ObservabilityService/ExportPGOProfile"

// MaxPGOServices is how many services one ExportPGOProfile call merges.
const MaxPGOServices = 50

// MaxPGOResponseBytes bounds the ExportPGOProfile answer read: the JSON
// document, whose pprof travels base64. It is the control-plane client's own
// read limit (internal/cloud); a profile of profilequery.DefaultMaxPGOEdges
// edges is about 2.5 MiB of it.
const MaxPGOResponseBytes = 8 << 20

// ExportPGORequest selects the edges to merge.
type ExportPGORequest struct {
	// Services are the OTEL service names whose profiles are merged (1..50).
	Services []string
	// Namespace is the service.namespace the edges were recorded under
	// (forge sets it to the project name); "" is any.
	Namespace string
	// Env is the forge env name the edges were recorded under; "" is any.
	Env string
	// Range is the window; the control plane caps it at 30 days.
	Range Range
}

// ExportPGOResult is the merged profile. An empty window is not an error:
// EdgeCount is 0.
type ExportPGOResult struct {
	// Pprof is the gzipped pprof, ready to be written as default.pgo.
	Pprof       []byte
	EdgeCount   int
	TotalWeight int64
	// Oldest and Newest are the start of the oldest and newest hour that
	// contributed; zero when EdgeCount is 0.
	Oldest, Newest time.Time
}

type wireExportPGORequest struct {
	Scope            wireScope `json:"scope"`
	Services         []string  `json:"services"`
	Env              string    `json:"env,omitempty"`
	ServiceNamespace string    `json:"serviceNamespace,omitempty"`
}

type wireExportPGOResponse struct {
	Pprof       []byte      `json:"pprof"`
	EdgeCount   int32       `json:"edgeCount"`
	TotalWeight json.Number `json:"totalWeight"`
	Oldest      time.Time   `json:"oldest"`
	Newest      time.Time   `json:"newest"`
}

// errPGOResponseTooBig is boundedPGOResponse's refusal.
var errPGOResponseTooBig = errors.New("ExportPGOProfile answer over the read limit")

// boundedPGOResponse decodes the answer only when it is within
// MaxPGOResponseBytes, whatever Caller delivered it.
type boundedPGOResponse struct{ wireExportPGOResponse }

func (r *boundedPGOResponse) UnmarshalJSON(b []byte) error {
	if len(b) > MaxPGOResponseBytes {
		return errPGOResponseTooBig
	}
	return json.Unmarshal(b, &r.wireExportPGOResponse)
}

// ExportPGOProfile reads the env's merged PGO profile. It is a whole-env read:
// a profile-guided build optimizes for the env, never for one dev instance, so
// the instance narrowing (if any) is not sent.
func (c Cloud) ExportPGOProfile(ctx context.Context, req ExportPGORequest) (ExportPGOResult, error) {
	if len(req.Services) == 0 {
		return ExportPGOResult{}, Wrong("ExportPGOProfile: no services named")
	}
	if len(req.Services) > MaxPGOServices {
		return ExportPGOResult{}, Wrong("%d services run this binary; the control plane merges at most %d: name the ones that matter with pgo.services", len(req.Services), MaxPGOServices)
	}
	scope := c.scope(Narrow{Range: req.Range})
	scope.InstanceIDs, scope.InstanceUsers = nil, nil
	var out boundedPGOResponse
	wire := wireExportPGORequest{Scope: scope, Services: req.Services, Env: req.Env, ServiceNamespace: req.Namespace}
	if err := c.callEnvWide(ctx, procExportPGOProfile, wire, &out); err != nil {
		if pgoResponseTooBig(err) {
			return ExportPGOResult{}, Undetermined("the control plane's PGO profile is over %d MiB, more than forge reads: "+
				"pull a shorter window (--since) or one binary at a time (--binary)", MaxPGOResponseBytes>>20).Cause(err)
		}
		return ExportPGOResult{}, notServed(err, "PGO profiles", "ExportPGOProfile")
	}
	res := ExportPGOResult{Pprof: out.Pprof, EdgeCount: int(out.EdgeCount), Oldest: unsetEpoch(out.Oldest), Newest: unsetEpoch(out.Newest)}
	if out.TotalWeight != "" {
		n, err := out.TotalWeight.Int64()
		if err != nil {
			return ExportPGOResult{}, Undetermined("the control plane's ExportPGOProfile answer could not be read: totalWeight %q", out.TotalWeight).Cause(err)
		}
		res.TotalWeight = n
	}
	return res, nil
}

// pgoResponseTooBig reports whether err is an answer over
// MaxPGOResponseBytes: refused here, or cut off at that limit by the client
// (which then fails to decode at the cut).
func pgoResponseTooBig(err error) bool {
	if errors.Is(err, errPGOResponseTooBig) {
		return true
	}
	var se *json.SyntaxError
	return errors.As(err, &se) && se.Offset >= MaxPGOResponseBytes
}
