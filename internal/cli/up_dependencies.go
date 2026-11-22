package cli

import (
	"context"
	"fmt"
	"os"
	"time"
)

// startupNode is deliberately just the local launch order, not a second runtime
// scheduler. Infrastructure and one-shot jobs still belong to upHostPhase.
type startupNode struct {
	name     string
	after    []string
	frontend bool
}

func hostStartupPlan(e *KCLEntities, targets []string) ([]startupNode, error) {
	// Old declarations are not subjected to a new namespace restriction.
	enabled := false
	for _, w := range e.WorkloadsOn(RuntimeHost) {
		if w.Runtime.Host != nil && len(w.Runtime.Host.StartupAfter) > 0 {
			enabled = true
		}
	}
	if !enabled {
		return nil, nil
	}
	nodes := map[string]startupNode{}
	var order []string
	add := func(n startupNode) error {
		if _, exists := nodes[n.name]; exists {
			return fmt.Errorf("startup_after: ambiguous name %q", n.name)
		}
		nodes[n.name] = n
		order = append(order, n.name)
		return nil
	}
	for _, w := range e.WorkloadsOn(RuntimeHost) {
		if !w.LongRunning() {
			if w.Runtime.Host != nil && len(w.Runtime.Host.StartupAfter) > 0 {
				return nil, fmt.Errorf("startup_after: %s is not a long-running host workload", w.Name)
			}
			continue
		}
		var after []string
		if w.Runtime.Host != nil {
			after = w.Runtime.Host.StartupAfter
		}
		if err := add(startupNode{name: w.Name, after: after}); err != nil {
			return nil, err
		}
	}
	for _, fe := range e.Frontends {
		if err := add(startupNode{name: fe.Name, frontend: true}); err != nil {
			return nil, err
		}
	}
	// Validate the whole declared graph, including cycles outside this selection.
	state := map[string]int{}
	var visit func(string) error
	visit = func(name string) error {
		if state[name] == 1 {
			return fmt.Errorf("startup_after: cycle at %q", name)
		}
		if state[name] == 2 {
			return nil
		}
		state[name] = 1
		for _, dep := range nodes[name].after {
			if _, ok := nodes[dep]; !ok {
				return fmt.Errorf("startup_after: %q requires missing host workload or frontend %q", name, dep)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[name] = 2
		return nil
	}
	for _, name := range order {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	var selected []startupNode
	for _, name := range order {
		if !inTargetSet(targets, name) {
			continue
		}
		n := nodes[name]
		for _, dep := range n.after {
			if !inTargetSet(targets, dep) {
				return nil, fmt.Errorf("startup_after: %q requires %q excluded by --target; select both targets", name, dep)
			}
		}
		selected = append(selected, n)
	}
	return selected, nil
}

// launchStartupPlan keeps declaration order except where an explicit edge
// requires moving a dependency forward. Each dependency is checked again just
// before its consumer starts, so an exit after an earlier ready probe fails.
func launchStartupPlan(ctx context.Context, nodes []startupNode, start func(startupNode) error, ready func(startupNode) error, finalReady ...func([]startupNode) error) error {
	byName := map[string]startupNode{}
	started := map[string]bool{}
	for _, n := range nodes {
		byName[n.name] = n
	}
	var launch func(startupNode) error
	launch = func(n startupNode) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if started[n.name] {
			return nil
		}
		for _, dep := range n.after {
			d := byName[dep]
			if err := launch(d); err != nil {
				return err
			}
			if err := ready(d); err != nil {
				return fmt.Errorf("startup_after: %s waiting for %s: %w", n.name, dep, err)
			}
		}
		// Earlier dependencies may exit or lose their listener while later ones
		// compile. Require a fresh aggregate snapshot at the launch boundary.
		var deps []startupNode
		for _, dep := range n.after {
			deps = append(deps, byName[dep])
		}
		for _, check := range finalReady {
			if err := check(deps); err != nil {
				return fmt.Errorf("startup_after: %s: %w", n.name, err)
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := start(n); err != nil {
			return err
		}
		started[n.name] = true
		return nil
	}
	for _, n := range nodes {
		if err := launch(n); err != nil {
			return err
		}
	}
	return nil
}

// checkStartupDependencies is a single aggregate, fail-closed snapshot, not
// another wait budget. A ready dependency that regressed must not launch a
// consumer against stale evidence.
func checkStartupDependencies(deps []startupNode, e *KCLEntities, projectID, env string, procs *procRegistry) error {
	facts := newOSProcFacts()
	return checkStartupDependencySnapshot(deps, e, projectID, env, procs, facts, func(name string, port int) hostReadyResult {
		return probeFrontendStartup(name, port, procs, portInUse, portListenerPID, facts)
	})
}

func checkStartupDependencySnapshot(deps []startupNode, e *KCLEntities, projectID, env string, procs *procRegistry, facts procFacts, frontendProbe func(string, int) hostReadyResult) error {
	var rows []hostReadyResult
	for _, dep := range deps {
		name := dep.name
		if dep.frontend {
			for _, fe := range e.Frontends {
				if fe.Name == dep.name {
					rows = append(rows, frontendProbe(dep.name, fe.Port))
				}
			}
		} else {
			rs := evalHostReadiness(e, projectID, env, []string{name}, portInUse, portListenerPID, facts)
			if len(rs) == 0 {
				state := portReadyNobody
				if procs.hasRunning(name) {
					state = portReadyOurs
				}
				rs = append(rs, hostReadyResult{name: name, state: state})
			}
			for i := range rs {
				rs[i].exited = procs.exitReason(name)
			}
			rows = append(rows, rs...)
		}
	}
	if unready := hostReadyUnready(rows); len(unready) > 0 {
		return hostReadyError(env, unready, e)
	}
	return nil
}

func waitStartupDependency(ctx context.Context, n startupNode, e *KCLEntities, projectID, env string, timeout time.Duration, procs *procRegistry) error {
	if !n.frontend {
		return waitHostServicesReady(ctx, e, projectID, env, []string{n.name}, timeout, hostReadyPoll, procs)
	}
	return waitFrontendStartup(ctx, n.name, e, env, timeout, hostReadyPoll, func(name string, port int) hostReadyResult {
		return probeFrontendStartup(name, port, procs, portInUse, portListenerPID, newOSProcFacts())
	})
}

// probeFrontendStartup requires this invocation's runner, not merely a reusable
// project/env marker. npm/pnpm may spawn the listener several descendants deep.
// Unknown ancestry stays unready; an unrelated known listener fails closed.
func probeFrontendStartup(name string, port int, procs *procRegistry, listening func(int) bool, listenerPID func(int) int, facts procFacts) hostReadyResult {
	r := hostReadyResult{name: "frontend:" + name, port: port, state: portReadyNobody}
	r.exited = procs.exitReason(r.name)
	runner := procs.runningPID(r.name)
	if runner <= 0 || r.exited != "" || port <= 0 || !listening(port) {
		return r
	}
	pid := listenerPID(port)
	if pid <= 0 {
		return r
	}
	if pid == runner || (facts != nil && insideTree(pid, map[int]bool{runner: true}, facts)) {
		r.state = portReadyOurs
	} else {
		r.state = portReadyForeign
		r.holderPID = pid
	}
	// Wait may have observed an exit while the OS probes were in flight.
	r.exited = procs.exitReason(r.name)
	return r
}

func waitFrontendStartup(ctx context.Context, name string, e *KCLEntities, env string, timeout, poll time.Duration, probe func(string, int) hostReadyResult) error {
	port := 0
	for _, fe := range e.Frontends {
		if fe.Name == name {
			port = fe.Port
			break
		}
	}
	if port <= 0 {
		return fmt.Errorf("frontend %q has no resolved dev-server port", name)
	}
	return waitHostReadiness(ctx, env, e, timeout, poll, os.Stdout, func() []hostReadyResult {
		return []hostReadyResult{probe(name, port)}
	})
}
