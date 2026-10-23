package graphkit

// plan.go — a root's start and stop order, as data.
//
// Build constructs a root's closure and Supervise stops it in reverse, but
// both need the real nodes: a database, a broker, a listener's port. The
// order itself is fixed when the graph is generated — it is compose.go's
// edges walked from the root's fields — so graph_gen.go also emits it as a
// value, Plan<Root>(), that a unit test can pin without constructing
// anything:
//
//	func TestGatewayStopsTheHandOverFirst(t *testing.T) {
//		if got := app.PlanGateway().Stop(); got[0] != "daemon_listener" {
//			t.Fatalf("stop order %v", got)
//		}
//	}

// PlanNode is one node of a root's plan: its name — the name Built and the
// supervisor's "drain:" lines use — and which lifecycle methods its type has,
// read off the type when graph_gen.go was generated.
type PlanNode struct {
	Name  string
	Run   bool
	Drain bool
	Close bool
	Ready bool
}

// Supervised reports whether Supervise runs the node: it has a Run, Drain,
// Close or Ready. A node without one is constructed and never started or
// stopped.
func (n PlanNode) Supervised() bool { return n.Run || n.Drain || n.Close || n.Ready }

// Plan is a root's nodes in the order its Build constructs them — each after
// everything it depends on, the root's last field first — preceded, for a
// root none of whose nodes serves the probes, by the probe listener Run
// starts before every node and stops after them.
//
// It is the plan, not a record: a node whose constructor returns a nil
// value at run time is not supervised, and Built() names only what a real
// Build constructed (never the probe listener).
type Plan []PlanNode

// Names is every node, in construction order.
func (p Plan) Names() []string {
	out := make([]string, 0, len(p))
	for _, n := range p {
		out = append(out, n.Name)
	}
	return out
}

// Start is the supervised nodes in the order Supervise starts them.
func (p Plan) Start() []string {
	var out []string
	for _, n := range p {
		if n.Supervised() {
			out = append(out, n.Name)
		}
	}
	return out
}

// Stop is the supervised nodes in the order Supervise stops them: Start
// reversed, so the root's first field stops first and what it depends on
// after it.
func (p Plan) Stop() []string {
	start := p.Start()
	out := make([]string, len(start))
	for i, n := range start {
		out[len(start)-1-i] = n
	}
	return out
}
