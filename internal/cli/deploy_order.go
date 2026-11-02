package cli

// THE ORDER ONE DEPLOY APPLIES ITS PLACEMENTS IN, derived from declared
// dependencies — never a fixed tier (docs/design/per-workload-placement.md §6).
//
// A `forge env deploy` of an env that mixes placements runs three stages:
//
//   - local     the parts forge applies from this machine: cluster workloads,
//     cluster databases, compose services, host infra
//   - hosted    the parts published to the control plane: OnHosted workloads,
//     hosted databases, OnHosted frontends (StaticSites)
//   - frontends the frontends forge builds and ships itself: OnBucket,
//     OnFirebase, BuildOnly
//
// Each stage waits for what it applied before the next starts (the local apply
// waits on rollout, the hosted publish on the platform's readiness), so a
// stage that runs after another sees it Ready at this release.
//
// THE EDGES ARE DECLARATIONS:
//
//   - `release_dependencies = {peer: contract}` on a workload, frontend or
//     database says "I consume peer's contract", so peer goes first. It is the
//     claim the release-cohort machinery already reads
//     (release_compose_declaration.go); one declaration answers both "must
//     change together" and "goes first".
//   - A FRONTEND whose release_dependencies is absent — which that field
//     defines as UNKNOWN — waits for every server workload of the release,
//     whatever its placement. Explicit {} means independent. A workload with
//     it absent gets no edge, which is the order forge always had.
//
// The complete item graph, including same-stage edges, is validated first.
// With no edge at all the order is local → hosted → frontends. An acyclic
// item graph requiring stage interleaving is explicitly unsupported; a cycle
// in its stage projection must not be reported as an actual dependency cycle.

import (
	"fmt"
	"strings"
)

// deployStage is one of the three stages above.
type deployStage string

const (
	stageLocal     deployStage = "local"
	stageHosted    deployStage = "hosted"
	stageFrontends deployStage = "frontends"
)

// defaultStageOrder is the order with no edges, and the tie-break.
var defaultStageOrder = []deployStage{stageLocal, stageHosted, stageFrontends}

// stageEdge is one declared dependency that crosses stages: Dependent's stage
// runs after Dependency's.
type stageEdge struct {
	Dependency, Dependent string
	From, To              deployStage
}

// stageOfWorkload is the stage a workload deploys in; "" when no deploy
// applies it (build-only, a host process `forge env up` runs).
func stageOfWorkload(w WorkloadEntity) deployStage {
	switch w.Runtime.Type {
	case RuntimeHosted:
		return stageHosted
	case RuntimeCluster, RuntimeCompose:
		return stageLocal
	}
	return ""
}

func stageOfFrontend(f FrontendEntity) deployStage {
	switch {
	case frontendIsHosted(f):
		return stageHosted
	case f.Runtime.Ships() || f.Runtime.Type == FrontendRuntimeBuildOnly:
		return stageFrontends
	}
	return ""
}

func stageOfDatabase(d DatabaseEntity) deployStage {
	if d.Hosted() {
		return stageHosted
	}
	return stageLocal
}

// deployStageEdges lists every declared dependency that crosses stages. Pure;
// sorted, so a refusal names the same edge on every run.
func deployStageEdges(e *KCLEntities) ([]stageEdge, error) {
	return declaredPlacementEdges(e)
}

// deployStageOrder is the order the stages run in: a topological order of the
// cross-stage edges, ties broken by defaultStageOrder.
func deployStageOrder(e *KCLEntities) ([]deployStage, error) {
	edges, err := deployStageEdges(e)
	if err != nil {
		return nil, err
	}
	after := map[deployStage]map[deployStage]stageEdge{} // to -> from -> a witness
	for _, ed := range edges {
		if ed.From == ed.To {
			continue // declaredPlacementEdges returns none; kept as a guard
		}
		if after[ed.To] == nil {
			after[ed.To] = map[deployStage]stageEdge{}
		}
		if _, seen := after[ed.To][ed.From]; !seen {
			after[ed.To][ed.From] = ed
		}
	}
	done := map[deployStage]bool{}
	var order []deployStage
	for len(order) < len(defaultStageOrder) {
		progressed := false
		for _, s := range defaultStageOrder {
			if done[s] {
				continue
			}
			ready := true
			for from := range after[s] {
				if !done[from] {
					ready = false
					break
				}
			}
			if ready {
				done[s], order, progressed = true, append(order, s), true
				break
			}
		}
		if !progressed {
			return nil, stageCycleError(after, done)
		}
	}
	return order, nil
}

// stageCycleError names one edge of the cycle each way.
func stageCycleError(after map[deployStage]map[deployStage]stageEdge, done map[deployStage]bool) error {
	var parts []string
	for _, to := range defaultStageOrder {
		if done[to] {
			continue
		}
		for _, from := range defaultStageOrder {
			if ed, ok := after[to][from]; ok && !done[from] {
				parts = append(parts, fmt.Sprintf("%s (%s) needs %s (%s)", ed.Dependent, ed.To, ed.Dependency, ed.From))
			}
		}
	}
	return fmt.Errorf("unsupported placement stage interleaving (the item dependency graph is acyclic): %s.\n"+
		"One deploy applies the parts forge applies, then the hosted parts, then the frontends forge ships, in whatever order "+
		"those dependencies give, so a hosted part and a cluster part cannot each wait for the other.\n"+
		"fix: drop one of those dependencies, or move one part to the other's placement",
		strings.Join(parts, "; "))
}
