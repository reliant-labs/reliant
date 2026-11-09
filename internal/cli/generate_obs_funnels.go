package cli

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/reliant-labs/forge/internal/obsdash"
)

// declaredFunnels is the product funnels the project's frontends declare
// (forge.Frontend.telemetry.funnels), as obsdash draws them, in id order.
//
// The dashboard is the project's, not an environment's, while a frontend is
// declared once and bound per env. Each env therefore contributes its
// frontends' funnels, and a funnel id declared by several is one funnel: the
// same definition collapses, and two definitions under one id are refused,
// because the panel would silently show whichever env rendered first.
func declaredFunnels(rendered []renderedEnvFrontend) ([]obsdash.FrontendFunnel, error) {
	type declared struct {
		funnel obsdash.FrontendFunnel
		where  string
	}
	byID := map[string]declared{}
	for _, r := range rendered {
		if r.Frontend.Telemetry == nil {
			continue
		}
		for _, f := range r.Frontend.Telemetry.Funnels {
			fn := obsdash.FrontendFunnel{
				ID:    f.ID,
				Title: f.Title,
				Funnel: obsdash.Funnel{
					Steps:  f.Steps,
					Window: f.Window,
					By:     obsdash.FunnelBy(f.By),
				},
			}
			where := fmt.Sprintf("frontend %q in env %q", r.Frontend.Name, r.Env)
			if prev, dup := byID[f.ID]; dup {
				if !reflect.DeepEqual(prev.funnel, fn) {
					return nil, fmt.Errorf("funnel %q is declared twice with different definitions (%s and %s): the dashboard has one panel per funnel id, so give them different ids or make them identical", f.ID, prev.where, where)
				}
				continue
			}
			byID[f.ID] = declared{funnel: fn, where: where}
		}
	}
	out := make([]obsdash.FrontendFunnel, 0, len(byID))
	for _, d := range byID {
		out = append(out, d.funnel)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
