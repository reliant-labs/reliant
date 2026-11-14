package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/obsdash"
)

// declaredAnalytics is the product analytics the project's frontends declare
// (forge.Frontend.telemetry.analytics), merged into the one declaration the
// project's built-in product dashboards read. Nil when no frontend declares
// any, which the dashboards read as every default.
//
// The dashboards are the project's while a frontend is declared once and
// bound per env, so every env's frontends contribute, by the same rule as
// declaredFunnels: an equal declaration collapses, and two different ones
// under one key are refused, because the dashboard would otherwise show
// whichever env rendered first.
//
//   - goals: one per id, sorted by id;
//   - activation and activation_window: each frontend that sets one must set
//     the same value; one left unset defers to the others;
//   - channels: concatenated in render order (env, then frontend), an equal
//     rule kept once; the same conditions credited to two channels are
//     refused;
//   - dashboards: any declaration of False drops them, because an opt-out is
//     the author's explicit word.
//
// `mode` is not merged: it is per frontend and per env, and reaches the
// browser as ANALYTICS_MODE without passing through the dashboards.
func declaredAnalytics(rendered []renderedEnvFrontend) (*obsdash.ProductAnalytics, error) {
	type origin struct{ value, where string }
	type declaredGoal struct {
		goal  obsdash.Goal
		where string
	}
	var (
		out        obsdash.ProductAnalytics
		declared   bool
		goals      = map[string]declaredGoal{}
		activation origin
		window     origin
		channels   = map[obsdash.ChannelRule]bool{}
		conditions = map[string]origin{}
	)
	for _, r := range rendered {
		if r.Frontend.Telemetry == nil || r.Frontend.Telemetry.Analytics == nil {
			continue
		}
		a := r.Frontend.Telemetry.Analytics
		declared = true
		where := fmt.Sprintf("frontend %q in env %q", r.Frontend.Name, r.Env)
		for _, g := range a.Goals {
			goal := obsdash.Goal{ID: g.ID, Title: g.Title, Event: g.Event, Route: g.Route}
			if prev, dup := goals[g.ID]; dup {
				if prev.goal != goal {
					return nil, fmt.Errorf("analytics goal %q is declared twice with different definitions (%s and %s): the Product dashboard has one row per goal id, so give them different ids or make them identical", g.ID, prev.where, where)
				}
				continue
			}
			goals[g.ID] = declaredGoal{goal, where}
		}
		for _, f := range []struct {
			name  string
			value string
			into  *origin
		}{{"activation", a.Activation, &activation}, {"activation_window", a.ActivationWindow, &window}} {
			if f.value == "" {
				continue
			}
			if f.into.value != "" && f.into.value != f.value {
				return nil, fmt.Errorf("analytics %s is %q in %s but %q in %s: the project's dashboards have one, so declare the same value or leave it unset in all but one", f.name, f.into.value, f.into.where, f.value, where)
			}
			if f.into.value == "" {
				*f.into = origin{f.value, where}
			}
		}
		for _, c := range a.Channels {
			rule := obsdash.ChannelRule{Host: c.Host, Source: c.Source, Medium: c.Medium, Channel: c.Channel}
			if channels[rule] {
				continue
			}
			key := c.Host + "|" + c.Source + "|" + c.Medium
			if prev, clash := conditions[key]; clash {
				return nil, fmt.Errorf("analytics channel rule %s credits %q in %s but %q in %s: rules are tried in order, so the second could never match; give them the same channel or different conditions", describeChannelConditions(rule), prev.value, prev.where, c.Channel, where)
			}
			conditions[key] = origin{c.Channel, where}
			channels[rule] = true
			out.Channels = append(out.Channels, rule)
		}
		if !a.Dashboards {
			out.NoDashboards = true
		}
	}
	if !declared {
		return nil, nil
	}
	for _, g := range goals {
		out.Goals = append(out.Goals, g.goal)
	}
	sort.Slice(out.Goals, func(i, j int) bool { return out.Goals[i].ID < out.Goals[j].ID })
	out.Activation, out.ActivationWindow = activation.value, window.value
	return &out, nil
}

// describeChannelConditions names a rule's conditions for an error message.
func describeChannelConditions(r obsdash.ChannelRule) string {
	var parts []string
	for _, c := range []struct{ k, v string }{{"host", r.Host}, {"source", r.Source}, {"medium", r.Medium}} {
		if c.v != "" {
			parts = append(parts, fmt.Sprintf("%s=%q", c.k, c.v))
		}
	}
	return strings.Join(parts, " ")
}
