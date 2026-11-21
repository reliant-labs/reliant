// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"
	"errors"
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
)

// poolSite is one literal `daemon: {pool: …}`: the pool it names and where.
type poolSite struct {
	pool string
	path []string
}

// checkMachinePools adds a validation error for every literal `daemon:
// {pool: …}` in the checked definition (the workflow's own and every
// node's, inline bodies and the on_failure/finally handlers included) that
// names a pool the owner does not have: the run would fail placing it. A CEL
// selector names nothing until the run resolves it, so it is not checked.
//
// lookup answers one pool name: nil when it exists, an error wrapping
// core.ErrMachinePoolNotFound when it does not. Any other error is a
// warning: whether the pool exists could not be read, and a read failure
// must not block a save the run itself would recheck.
func checkMachinePools(c workflowCheck, lookup func(name string) error) {
	if c.parseErr != nil || c.result == nil || c.workflow == nil {
		return
	}
	known := map[string]error{}
	for _, site := range machinePoolSites(c.workflow) {
		err, seen := known[site.pool]
		if !seen {
			err = lookup(site.pool)
			known[site.pool] = err
		}
		switch {
		case err == nil:
		case errors.Is(err, core.ErrMachinePoolNotFound):
			c.result.AddError(validation.CategoryStructure, site.path, "daemon.pool", machinepool.PoolNotFoundMessage(site.pool))
		default:
			c.result.AddWarning(validation.CategoryStructure, site.path, "daemon.pool",
				fmt.Sprintf("could not check that machine pool %q exists: %v", site.pool, err))
		}
	}
}

// machinePoolSites lists wf's literal pool selectors in document order.
func machinePoolSites(wf *reliantv1.Workflow) []poolSite {
	var sites []poolSite
	add := func(sel *reliantv1.CelDaemonSelector, path []string) {
		if pool := sel.GetLiteral().GetPool(); pool != "" {
			sites = append(sites, poolSite{pool: pool, path: path})
		}
	}
	add(wf.GetDaemon(), []string{wf.GetName()})
	var walk func(w *reliantv1.Workflow, base []string)
	walk = func(w *reliantv1.Workflow, base []string) {
		for i, node := range w.GetNodes() {
			if node == nil {
				continue
			}
			nodePath := appendPath(base, "nodes", poolNodeSegment(node, i))
			add(node.GetDaemon(), nodePath)
			if inline := node.GetWorkflow().GetInline(); inline != nil {
				walk(inline, appendPath(nodePath, "inline"))
			}
			if inline := node.GetLoop().GetInline(); inline != nil {
				walk(inline, appendPath(nodePath, "inline"))
			}
		}
	}
	walk(wf, []string{wf.GetName()})
	if h := wf.GetOnFailure(); h != nil {
		walk(h, []string{wf.GetName(), "on_failure"})
	}
	if h := wf.GetFinally(); h != nil {
		walk(h, []string{wf.GetName(), "finally"})
	}
	return sites
}

// poolNodeSegment names a node in a path the way the validator does: its id,
// or its index when it has none.
func poolNodeSegment(node *reliantv1.Node, i int) string {
	if id := node.GetId(); id != "" {
		return id
	}
	return fmt.Sprintf("[%d]", i)
}

// appendPath is a fresh path (a stored finding keeps its path by reference).
func appendPath(base []string, elems ...string) []string {
	out := make([]string, 0, len(base)+len(elems))
	return append(append(out, base...), elems...)
}

// poolLookup answers checkMachinePools for userID through the repository.
func (s *Service) poolLookup(ctx context.Context, userID string) func(name string) error {
	return func(name string) error {
		_, err := s.deps.Repo.GetMachinePoolByName(ctx, userID, name)
		return err
	}
}
