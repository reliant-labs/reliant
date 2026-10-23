// Package graphkit is the runtime under a project's generated composition
// graph (internal/app/graph_gen.go): memoized construction of each node, the
// root's Build — force the closure, then seal — and the lifecycle nodes of
// what was built, in the order it was built, for lifecyclekit.Supervise.
//
// The graph itself is generated Go. Every edge is a value expression in the
// project's internal/app/compose.go, and every accessor is a typed method on
// the project's *app.Graph that calls Get. Nothing here resolves a dependency,
// looks a value up by name, or inspects a type: which node a Deps field gets
// was decided at generate time, and which lifecycle methods a node has was
// read off its type by the generator and handed to Get as a Node literal.
//
//	func (g *Graph) Billing() *billing.Service {
//		return graphkit.Get(&g.b, &g.m.Billing, "billing", g.newBilling, nil)
//	}
//
// Construction is eager within a root and fails loud at boot: Build forces
// the root's whole closure before anything runs, a construction error
// unwinds with the path that reached it ("build server: api_listener →
// user_routes → billing: …"), and once the graph is sealed an accessor for a
// node the root never reached panics instead of constructing it on a first
// request.
//
// While it builds, a Builder reports each node's construction to a Progress
// (ReportTo) — in a generated graph, the boot probes, so /readyz names the
// node whose constructor is blocking boot.
//
// A Builder is used from one goroutine while it builds. After Build, reading
// an already-built node is safe from any goroutine.
package graphkit

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/lifecyclekit"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

// Memo holds one node's value once it is constructed. The zero value is an
// unbuilt node.
type Memo[T any] struct {
	v    T
	done bool
}

// Builder is one graph's construction state. The zero value is ready to use.
type Builder struct {
	path     []string // the nodes being constructed, outermost first
	sealed   bool
	root     string
	built    []builtNode // completion order: every node after its dependencies
	progress Progress
}

// Progress is told about each node's construction as it happens: Constructing
// as a node's constructor starts, and the done it returns as that constructor
// ends — with its error, nil once the node is built. Nodes nest: a node's
// dependencies start and end inside it, so the latest started and not yet
// done is the constructor actually running. A dependency's failure ends every
// node that reached it, each with that error. Construction is sequential: one
// goroutine, one node's constructor at a time.
//
// serverkit's boot probes satisfy it, so a root's /readyz names the node
// boot is waiting on.
type Progress interface {
	Constructing(node string) (done func(err error))
}

// ReportTo sets where construction progress goes from now on; nil reports
// nowhere. Call it before Build.
func (b *Builder) ReportTo(p Progress) { b.progress = p }

type builtNode struct {
	name string
	node lifecyclekit.Node
}

// buildError is a construction failure on its way up to Build.
type buildError struct {
	path []string
	err  error
}

func (e *buildError) Error() string {
	return strings.Join(e.path, " → ") + ": " + e.err.Error()
}

func (e *buildError) Unwrap() error { return e.err }

// ErrSealed is the cause of the panic an accessor raises for a node the
// root's Build did not construct.
var ErrSealed = errors.New("graphkit: the graph is sealed")

// Get returns the node m memoizes, constructing it with ctor the first time.
// name labels it in errors and in the supervisor's logs. node, when non-nil,
// is the node's lifecycle — its Run, Drain, Close and Ready methods, read off
// its type at generate time; a Node with none of them is not supervised.
//
// A construction error does not return: it panics, carrying the path of
// nodes that reached it, and Build recovers it. Calling an accessor outside
// Build (a hand-written main using the graph unsealed) gets the same panic;
// Catch turns it into an error.
func Get[T any](b *Builder, m *Memo[T], name string, ctor func() (T, error), node func(T) lifecyclekit.Node) T {
	if m.done {
		return m.v
	}
	if b.sealed {
		panic(fmt.Errorf("%w: %s was not constructed by Build%s, so nothing may construct it now — "+
			"reach it from the root's fields (or a node they reach), or build it before Build seals the graph",
			ErrSealed, name, rootSuffix(b.root)))
	}
	for i, n := range b.path {
		if n == name {
			cycle := append(append([]string(nil), b.path[i:]...), name)
			panic(&buildError{path: append([]string(nil), b.path...), err: fmt.Errorf(
				"dependency cycle %s: compose.go's value expressions reach %s from itself — break one edge with a setter after both exist",
				strings.Join(cycle, " → "), name)})
		}
	}
	v, err := construct(b, name, ctor)
	if err != nil {
		panic(&buildError{path: append(append([]string(nil), b.path...), name), err: err})
	}
	m.v, m.done = v, true
	bn := builtNode{name: name}
	if node != nil {
		bn.node = node(v)
		bn.node.Name = name
	}
	b.built = append(b.built, bn)
	return v
}

// construct runs ctor, turning a plain panic inside it into an error on this
// node so its path is reported; a panic that already carries a path (a
// dependency's failure) passes through untouched.
func construct[T any](b *Builder, name string, ctor func() (T, error)) (v T, err error) {
	b.path = append(b.path, name)
	var done func(error)
	if b.progress != nil {
		done = b.progress.Constructing(name)
	}
	defer func() {
		// Popped however ctor ends, so a failure caught by Catch leaves no
		// stale path behind to read as a cycle on the next accessor.
		b.path = b.path[:len(b.path)-1]
		if r := recover(); r != nil {
			if be, ok := r.(*buildError); ok {
				report(done, be)
				panic(be)
			}
			if e, ok := r.(error); ok && errors.Is(e, ErrSealed) {
				report(done, e)
				panic(r)
			}
			err = fmt.Errorf("panic: %v", r)
		}
		report(done, err)
	}()
	return ctor()
}

// report ends a node's construction on its Progress, if one was told.
func report(done func(error), err error) {
	if done != nil {
		done(err)
	}
}

func rootSuffix(root string) string {
	if root == "" {
		return ""
	}
	return " (root " + root + ")"
}

// Build constructs a root: force calls the root's field accessors, so every
// node the root reaches is constructed once, dependencies first. Build then
// seals the graph. A construction error comes back as "build <root>: a → b →
// c: <cause>". Building a second root on a sealed graph is an error: one
// process runs one root.
func (b *Builder) Build(root string, force func()) error {
	if b.sealed {
		return fmt.Errorf("build %s: the graph was already built for root %s; build each root on a graph of its own", root, b.root)
	}
	if err := Catch(force); err != nil {
		return fmt.Errorf("build %s: %w", root, err)
	}
	b.sealed, b.root = true, root
	return nil
}

// Catch runs f and returns the construction error a node's accessor panicked
// with, for a caller that uses the graph's accessors outside Build. Any other
// panic is re-raised.
func Catch(f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if be, ok := r.(*buildError); ok {
				err = be
				return
			}
			panic(r)
		}
	}()
	f()
	return nil
}

// Sealed reports whether Build has run.
func (b *Builder) Sealed() bool { return b.sealed }

// Built is the name of every constructed node, in construction-completion
// order: each after everything it depends on.
func (b *Builder) Built() []string {
	out := make([]string, len(b.built))
	for i, n := range b.built {
		out[i] = n.name
	}
	return out
}

// Nodes are the lifecycle nodes of every constructed node that has a Run,
// Drain, Close or Ready, in construction order — the order Supervise starts
// them in, so a node starts after everything it depends on and stops before.
func (b *Builder) Nodes() []lifecyclekit.Node {
	var out []lifecyclekit.Node
	for _, n := range b.built {
		if n.node.Run == nil && n.node.Drain == nil && n.node.Close == nil && n.node.Ready == nil {
			continue
		}
		out = append(out, n.node)
	}
	return out
}

// Supervise runs the built nodes under lifecyclekit.Supervise until ctx ends.
// Every node that reports Ready joins probes' /readyz, and probes' Drainer is
// the policy's readiness gate and shutdown budget when the policy names none.
// probes may be nil — a root with no probe route. Call it after Build.
//
// first are nodes the graph did not build — a root's probe listener — that
// start before every built node and stop after all of them, so they answer
// for the whole of boot and drain.
func (b *Builder) Supervise(ctx context.Context, policy lifecyclekit.Policy, probes *serverkit.Probes, first ...lifecyclekit.Node) error {
	if !b.sealed {
		return errors.New("graphkit: Supervise before Build: build the root first, so nothing is constructed while it runs")
	}
	nodes := append(append([]lifecyclekit.Node(nil), first...), b.Nodes()...)
	if probes != nil {
		probes.AddReadyCheck(lifecyclekit.ReadyChecks(nodes...)...)
		if policy.Drainer == nil {
			policy.Drainer = probes.Drainer
		}
	}
	return lifecyclekit.Supervise(ctx, policy, nodes...)
}
