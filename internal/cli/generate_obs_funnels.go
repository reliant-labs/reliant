package cli

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// declaredFunnels is the product funnels the project's frontends declare
// (forge.Frontend.telemetry.funnels), as obsdash draws them, in id order.
//
// The dashboard is the project's, not an environment's, while a frontend is
// declared once and bound per env. Each env therefore contributes its
// frontends' funnels, and a funnel id declared by several is one funnel: the
// same definition collapses, and two definitions under one id are refused,
// because the panel would silently show whichever env rendered first.
func declaredFunnels(rendered []renderedEnvFrontend, analytics ...renderedEnvAnalytics) ([]obsdash.FrontendFunnel, error) {
	type declared struct {
		funnel obsdash.FrontendFunnel
		where  string
	}
	byID := map[string]declared{}
	add := func(f FunnelEntity, where string) error {
		fn := funnelFromEntity(f)
		if prev, dup := byID[f.ID]; dup {
			if !reflect.DeepEqual(prev.funnel, fn) {
				return fmt.Errorf("funnel %q is declared twice with different definitions (%s and %s): the dashboard has one panel per funnel id, so give them different ids or make them identical", f.ID, prev.where, where)
			}
			return nil
		}
		byID[f.ID] = declared{funnel: fn, where: where}
		return nil
	}
	for _, r := range rendered {
		if r.Frontend.Telemetry == nil {
			continue
		}
		for _, f := range r.Frontend.Telemetry.Funnels {
			if err := add(f, fmt.Sprintf("frontend %q in env %q", r.Frontend.Name, r.Env)); err != nil {
				return nil, err
			}
		}
	}
	for _, a := range analytics {
		for _, f := range a.Analytics.Funnels {
			if err := add(f, fmt.Sprintf("observability.analytics in env %q", a.Env)); err != nil {
				return nil, err
			}
		}
	}
	out := make([]obsdash.FrontendFunnel, 0, len(byID))
	for _, d := range byID {
		out = append(out, d.funnel)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// funnelFromEntity is a rendered forge.Funnel as the dashboards read it.
func funnelFromEntity(f FunnelEntity) obsdash.FrontendFunnel {
	fn := obsdash.FrontendFunnel{
		ID:    f.ID,
		Title: f.Title,
		Funnel: obsdash.Funnel{
			Steps:  f.Steps,
			Window: f.Window,
			By:     obsdash.FunnelBy(f.By),
		},
	}
	for _, s := range f.StepFilters {
		fn.StepFilters = append(fn.StepFilters, obsdash.FunnelStep{Where: s.Where, Within: s.Within})
	}
	return fn
}

// declaredCounts is every env's Observability.analytics counts, one per id:
// an equal declaration collapses, two different ones are refused.
func declaredCounts(analytics []renderedEnvAnalytics) ([]obsdash.EventCount, error) {
	byID := map[string]obsdash.EventCount{}
	where := map[string]string{}
	for _, a := range analytics {
		for _, c := range a.Analytics.Counts {
			ec := obsdash.EventCount{ID: c.ID, Title: c.Title, Event: c.Event, GroupBy: c.GroupBy}
			if prev, dup := byID[c.ID]; dup {
				if !reflect.DeepEqual(prev, ec) {
					return nil, fmt.Errorf("count %q is declared twice with different definitions (%s and env %q): the Product dashboard has one panel per count id", c.ID, where[c.ID], a.Env)
				}
				continue
			}
			byID[c.ID], where[c.ID] = ec, fmt.Sprintf("env %q", a.Env)
		}
	}
	out := make([]obsdash.EventCount, 0, len(byID))
	for _, c := range byID {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// declaredActivity is the project's user-activity rules: the union of every
// env's Observability.analytics not_activity, exclude_users_with and
// exclude_user_ids, so an event or user any environment declares is a rule
// for the project's dashboards.
func declaredActivity(analytics []renderedEnvAnalytics) (obsquery.Activity, error) {
	var out obsquery.Activity
	for _, a := range analytics {
		out = out.Merge(obsquery.Activity{NotActivity: a.Analytics.NotActivity, ExcludeUsersWith: a.Analytics.ExcludeUsersWith,
			ExcludeUserIDs: a.Analytics.ExcludeUserIDs})
	}
	if err := out.Validate(); err != nil {
		return obsquery.Activity{}, fmt.Errorf("observability.analytics: %w", err)
	}
	return out, nil
}
