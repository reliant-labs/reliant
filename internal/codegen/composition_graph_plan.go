// File: internal/codegen/composition_graph_plan.go
//
// Each root's plan as data (roots plan F7): graph_gen.go's Plan<Root>()
// returns the nodes Build<Root> constructs, in order, with the lifecycle
// methods each one's type has — the start and stop order Run<Root>
// supervises, which a unit test can then pin without a database or a broker
// (graphkit.Plan's Start and Stop).

package codegen

// graphPlanNodeView is one node of a root's plan.
type graphPlanNodeView struct {
	Name                     string
	Run, Drain, Close, Ready bool
}

// planNodes is v's closure in construction order (v.Reach), each node under
// the name its accessor hands graphkit.Get.
func planNodes(v graphRootView, byName map[string]*graphNode) []graphPlanNodeView {
	out := make([]graphPlanNodeView, 0, len(v.Reach))
	for _, name := range v.Reach {
		pn := graphPlanNodeView{Name: logName(name)}
		if n := byName[name]; n != nil {
			if n.LogName != "" {
				pn.Name = n.LogName
			}
			l := n.Lifecycle
			pn.Run, pn.Drain, pn.Close, pn.Ready = l.Run != "", l.Drain != "", l.Close != "", l.Ready != ""
		}
		out = append(out, pn)
	}
	return out
}
