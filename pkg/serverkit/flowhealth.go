package serverkit

import (
	"encoding/json"
	"net/http"
)

// flowhealth.go — the GET /flow-health endpoint.
//
// /flow-health is the APP-FLOW analogue of /readyz. /readyz answers "can this
// process serve traffic" (listener bound, deps reachable); /flow-health
// answers "is the end-to-end invariant this service OWNS actually holding"
// (e.g. "every Ready managed daemon is attached to the gateway"). The owning
// service is the only place that can assert it — it holds the state — so the
// assertion runs HERE, inside the service, and the result is exposed as a
// plain HTTP status:
//
//	200  every registered FlowCheck passed   (healthy)
//	503  at least one FlowCheck failed        (unhealthy)
//
// PUBLIC / NO-LEAK CONTRACT. The response body is STATUS-ONLY: per-check name +
// ok + a TERSE aggregate Summary ("2 daemons, 0 unattached"). It carries NO
// per-entity / per-user detail, so the endpoint is anonymous-safe and `forge
// smoke` can curl it with no creds. Per-entity detail belongs behind your own
// authenticated endpoint (or an internal-only port), never here.

// flowHealthResponse is the status-only JSON body of /flow-health. Additive:
// new fields may appear; existing keys keep their meaning.
type flowHealthResponse struct {
	Status string                  `json:"status"` // "healthy" | "unhealthy"
	Checks []flowHealthCheckStatus `json:"checks"`
}

// flowHealthCheckStatus is one check's public status. Summary is a terse,
// non-sensitive aggregate — never per-entity detail.
type flowHealthCheckStatus struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Summary string `json:"summary,omitempty"`
}

// flowHealthHandler builds the GET /flow-health handler over the registered
// checks. Every check runs on each request (the assertion is cheap relative to
// the network round-trip and must reflect live state). The overall status is
// healthy iff every check passed; any failure yields 503 so a load balancer /
// `forge env smoke` reads the break directly off the status code.
func flowHealthHandler(checks []FlowCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := flowHealthResponse{Status: "healthy"}
		allOK := true
		for _, c := range checks {
			res := c.Check(r.Context())
			if !res.OK {
				allOK = false
			}
			resp.Checks = append(resp.Checks, flowHealthCheckStatus{
				Name:    c.Name,
				OK:      res.OK,
				Summary: res.Summary,
			})
		}
		if !allOK {
			resp.Status = "unhealthy"
		}

		w.Header().Set("Content-Type", "application/json")
		if allOK {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}
