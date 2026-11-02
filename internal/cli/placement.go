package cli

// PLACEMENT IS A PROPERTY OF EACH WORKLOAD, NEVER OF THE ENV.
//
// Every workload, frontend and database of an env declares where it runs by
// its runtime binding (OnCluster, OnHosted, OnBucket, OnFirebase, OnCompose,
// OnHost, BuildOnly). That binding is the only placement fact. An env holds any
// mix of them, one release carries all of them, and one deploy applies them in
// the order their declared dependencies give (deploy_order.go). The design is
// docs/design/per-workload-placement.md.
//
// This file is the one projection of that fact the rest of the CLI asks
// questions of: placementsOf lists each part with its placement and actuator,
// and placementSummary answers the env-wide questions (is anything hosted, is
// anything applied to a cluster) WITHOUT inventing an env-level mode.

import (
	"sort"

	"github.com/reliant-labs/forge/pkg/release"
)

// The placement vocabulary, as `forge env status --json` reports it per part.
const (
	placementHosted    = "hosted"     // the control plane's platform (OnHosted, a hosted database)
	placementCluster   = "cluster"    // a Kubernetes cluster the author operates (OnCluster, a cluster database)
	placementBucket    = "bucket"     // the author's object storage (OnBucket)
	placementFirebase  = "firebase"   // Firebase Hosting (OnFirebase)
	placementCompose   = "compose"    // docker compose on this machine
	placementHost      = "host"       // a host process / dev server on this machine
	placementBuildOnly = "build-only" // built, never run
)

// The actuator vocabulary: WHO applies a part.
const (
	actuatorPlatform = "platform" // the control plane converges it
	actuatorHub      = "hub"      // the control plane's hub converges its delivery unit onto a connected cluster
	actuatorForge    = "forge"    // `forge env deploy` applies it from the deploying machine
	actuatorMachine  = "machine"  // `forge env up` runs it on a developer machine
	actuatorNone     = "none"     // nothing runs it
)

// partPlacement is one deployable part of an env and where it runs.
type partPlacement struct {
	// Name is the workload, frontend or database name.
	Name string `json:"name"`
	// Kind is "workload", "frontend" or "database".
	Kind string `json:"kind"`
	// Placement is one of the placement* constants.
	Placement string `json:"placement"`
	// Cluster is the rendered cluster a cluster part lands on, "" otherwise.
	Cluster string `json:"cluster,omitempty"`
	// Actuator is one of the actuator* constants.
	Actuator string `json:"actuator"`
}

// placementsOf projects every part of the env. Pure; sorted by kind, then
// name, so the JSON is stable.
//
// A cluster workload's actuator follows the env's delivery-unit split: the hub
// for a unit it converges, forge otherwise. A split the declaration cannot
// honour is reported as forge's, which is what `forge env deploy` falls back
// to (it applies what it can and says why).
func placementsOf(e *KCLEntities) []partPlacement {
	if e == nil {
		return nil
	}
	split, _ := unitSplitOf(e)
	hubUnits := map[string]bool{}
	for _, u := range split.Hub {
		hubUnits[u] = true
	}
	var out []partPlacement
	for _, w := range e.Workloads {
		p := partPlacement{Name: w.Name, Kind: "workload"}
		switch w.Runtime.Type {
		case RuntimeHosted:
			p.Placement, p.Actuator = placementHosted, actuatorPlatform
		case RuntimeCluster:
			p.Placement, p.Actuator = placementCluster, actuatorForge
			if c := w.Runtime.Cluster; c != nil {
				p.Cluster = c.Cluster
			}
			unit := w.DeliveryUnit
			if unit == "" {
				unit = release.DefaultDeliveryUnit
			}
			if hubUnits[unit] {
				p.Actuator = actuatorHub
			}
		case RuntimeCompose:
			p.Placement, p.Actuator = placementCompose, actuatorMachine
		case RuntimeHost:
			p.Placement, p.Actuator = placementHost, actuatorMachine
		default:
			p.Placement, p.Actuator = placementBuildOnly, actuatorNone
		}
		out = append(out, p)
	}
	for _, f := range e.Frontends {
		p := partPlacement{Name: f.Name, Kind: "frontend"}
		switch {
		case frontendIsHosted(f):
			p.Placement, p.Actuator = placementHosted, actuatorPlatform
		case f.Runtime.Type == FrontendRuntimeBucket:
			p.Placement, p.Actuator = placementBucket, actuatorForge
		case f.Runtime.Type == FrontendRuntimeFirebase:
			p.Placement, p.Actuator = placementFirebase, actuatorForge
		case f.Runtime.Type == FrontendRuntimeHost:
			p.Placement, p.Actuator = placementHost, actuatorMachine
		default:
			p.Placement, p.Actuator = placementBuildOnly, actuatorNone
		}
		out = append(out, p)
	}
	for _, d := range e.Databases {
		p := partPlacement{Name: d.Name, Kind: "database"}
		if d.Hosted() {
			p.Placement, p.Actuator = placementHosted, actuatorPlatform
		} else {
			p.Placement, p.Actuator, p.Cluster = placementCluster, actuatorForge, d.Cluster
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// placementSummary answers the env-wide questions as facts over its parts.
type placementSummary struct {
	// Hosted: at least one part runs on the control plane's platform.
	Hosted bool
	// Cluster: at least one part runs on a cluster the author operates.
	Cluster bool
}

// summarizePlacements is the summary of the env's parts. Pure.
func summarizePlacements(e *KCLEntities) placementSummary {
	return placementSummary{Hosted: e.HasHosted(), Cluster: e != nil && runsOnOwnCluster(e)}
}

// Mixed reports whether the env holds hosted parts AND cluster parts.
func (s placementSummary) Mixed() bool { return s.Hosted && s.Cluster }
