package cli

// THE CLAIMABLE PLAN'S CAPS ARE THE CONTROL PLANE'S.
//
// forge used to compile its own copy of the anonymous plan's caps, and every
// change to them on the platform (250m / 256 MiB, then 125m / 128 MiB) left
// every released forge judging by stale numbers: refusing what the platform
// admits, or admitting what it refuses after a build. The control plane
// publishes the caps in its public GetClaimableAvailability answer
// (`limits`), and every claimable check here judges by that answer:
// `env shape --claimable`, `env deploy --claimable --plan-only` and
// `env deploy --claimable`.
//
// The compiled-in copy (deploy.ClaimableLimitsFallback) is used only when the
// control plane cannot be asked (offline, or one that predates publishing
// them), and the output says so: the platform's own limits still decide at
// admission.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/deploy"
)

// claimableLimitsSource is the caps this process judges claimable envs by,
// and where they came from.
type claimableLimitsSource struct {
	limits deploy.ClaimableLimits
	// endpoint is the control plane that published them, or was asked.
	endpoint string
	// published is true when the control plane answered with its caps.
	published bool
	// why is, for the compiled-in copy, why the control plane's were not used.
	why string
}

// note is the one line every claimable check prints about its caps.
func (s claimableLimitsSource) note() string {
	l := s.limits
	caps := fmt.Sprintf("%s per workload, Postgres storage %s", l.WorkloadShape(), l.DatabaseStorage())
	if s.published {
		return fmt.Sprintf("claimable limits: %s, as published by the control plane at %s", caps, s.endpoint)
	}
	return fmt.Sprintf("claimable limits: %s, from forge's compiled-in copy (offline: %s); "+
		"the control plane's own limits decide at deploy", caps, s.why)
}

var (
	claimableLimitsMu sync.Mutex
	// claimableLimitsActive is the resolved caps, nil until a command resolves
	// them (or when none of its checks needs them).
	claimableLimitsActive *claimableLimitsSource
	// claimableLimitsOnline lets a check that finds it needs the caps ask the
	// control plane for them itself (claimableLimitsFor). Off by default, so
	// an offline lane (`ci validate-kcl`) never touches the network.
	claimableLimitsOnline bool
)

// claimableLimitsTimeout bounds the availability question a check asks
// for itself. A var for the tests.
var claimableLimitsTimeout = 10 * time.Second

// useClaimableLimits installs s for every claimable check until restore runs.
func useClaimableLimits(s claimableLimitsSource) (restore func()) {
	claimableLimitsMu.Lock()
	defer claimableLimitsMu.Unlock()
	prev := claimableLimitsActive
	claimableLimitsActive = &s
	return func() {
		claimableLimitsMu.Lock()
		defer claimableLimitsMu.Unlock()
		claimableLimitsActive = prev
	}
}

// resolveClaimableLimitsOnDemand lets claimableLimitsFor ask the control
// plane the env names, the first time a check needs the caps, until restore
// runs. `forge env shape` uses it: whether an env is claimable is known only
// once it is rendered.
func resolveClaimableLimitsOnDemand() (restore func()) {
	claimableLimitsMu.Lock()
	defer claimableLimitsMu.Unlock()
	prev, prevActive := claimableLimitsOnline, claimableLimitsActive
	claimableLimitsOnline = true
	return func() {
		claimableLimitsMu.Lock()
		defer claimableLimitsMu.Unlock()
		claimableLimitsOnline, claimableLimitsActive = prev, prevActive
	}
}

// claimableLimitsFor is the caps a claimable check of env judges by: the
// installed ones, else (when this command may ask) the control plane's,
// else the compiled-in copy.
func claimableLimitsFor(env string, e *KCLEntities) claimableLimitsSource {
	claimableLimitsMu.Lock()
	active, online := claimableLimitsActive, claimableLimitsOnline
	claimableLimitsMu.Unlock()
	if active != nil {
		return *active
	}
	if !online {
		return claimableLimitsSource{limits: deploy.ClaimableLimitsFallback(), why: "this check does not ask the control plane"}
	}
	ep, err := cloud.ResolveEndpoint(env, declarationFromEntities(e))
	if err != nil {
		return claimableLimitsSource{limits: deploy.ClaimableLimitsFallback(), why: "no control plane endpoint: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), claimableLimitsTimeout)
	defer cancel()
	av, ok, err := claimableAvailabilityOf(ctx, ep)
	s := claimableLimitsFrom(ep, av, ok, err)
	useClaimableLimits(s) // resolved once per process; restored with the on-demand window
	return s
}

// claimableLimitsFrom is the caps an availability answer implies: the
// published ones, or the compiled-in copy with the reason it was needed.
func claimableLimitsFrom(ep cloud.Endpoint, av cloud.ClaimableAvailability, ok bool, err error) claimableLimitsSource {
	s := claimableLimitsSource{limits: deploy.ClaimableLimitsFallback(), endpoint: ep.URL}
	switch {
	case err != nil:
		s.why = fmt.Sprintf("could not ask %s: %v", ep.URL, err)
	case !ok:
		s.why = fmt.Sprintf("%s does not answer GetClaimableAvailability", ep.URL)
	case av.Limits == nil:
		s.why = fmt.Sprintf("%s does not publish its claimable limits", ep.URL)
	default:
		if l := limitsFromWire(*av.Limits); l.Valid() {
			s.limits, s.published = l, true
		} else {
			s.why = fmt.Sprintf("%s published incomplete claimable limits", ep.URL)
		}
	}
	return s
}

// limitsFromWire converts the control plane's ClaimableLimits.
func limitsFromWire(w cloud.ClaimableLimits) deploy.ClaimableLimits {
	return deploy.ClaimableLimits{
		MaxServices:                int(w.MaxServices),
		MaxReplicas:                int(w.MaxReplicas),
		MaxMigrationJobs:           int(w.MaxMigrationJobs),
		WorkloadCPUMillicores:      int64(w.WorkloadCPUMillicores),
		WorkloadMemoryBytes:        int64(w.WorkloadMemoryMib) << 20,
		MaxDatabases:               int(w.MaxDatabases),
		DatabaseInstances:          int(w.DatabaseInstances),
		DatabaseDeclaredStorageGiB: w.DatabaseDeclaredStorageGib,
		DatabaseCPUMillicores:      int64(w.DatabaseCPUMillicores),
		DatabaseMemoryBytes:        int64(w.DatabaseMemoryMib) << 20,
		DatabaseStorageBytes:       int64(w.DatabaseStorageMib) << 20,
		DatabaseMaxConnections:     int(w.DatabaseMaxConnections),
		MaxStaticSites:             int(w.MaxStaticSites),
	}
}
