package cli

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
)

func liveSet(slugs ...string) map[string]bool {
	out := make(map[string]bool, len(slugs))
	for _, s := range slugs {
		out[s] = true
	}
	return out
}

// The default must stay "every entity": every project that predates
// frontends[].routes has no such key, and a narrowing default would silently
// stop generating pages those projects already depend on.
func TestRouteFilter_EmptyAllowlistGeneratesEverything(t *testing.T) {
	t.Parallel()
	want, unknown := routeFilterFor(config.FrontendConfig{Name: "web"}, liveSet("users", "daemons"))

	for _, slug := range []string{"users", "daemons", "anything-else"} {
		if !want(slug) {
			t.Errorf("want(%q) = false, but an empty allowlist must generate every route", slug)
		}
	}
	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want none for an empty allowlist", unknown)
	}
}

// The narrowing case this flag exists for: an operator console that wants two
// of twenty entities should get exactly two.
func TestRouteFilter_AllowlistNarrowsToNamedRoutes(t *testing.T) {
	t.Parallel()
	fe := config.FrontendConfig{Name: "ops", Routes: []string{"users", "usage-events"}}
	want, unknown := routeFilterFor(fe, liveSet("users", "usage-events", "daemons", "plans"))

	for _, slug := range []string{"users", "usage-events"} {
		if !want(slug) {
			t.Errorf("want(%q) = false, want true — it is in the allowlist", slug)
		}
	}
	for _, slug := range []string{"daemons", "plans"} {
		if want(slug) {
			t.Errorf("want(%q) = true, want false — it is NOT in the allowlist", slug)
		}
	}
	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want none (every declared slug is live)", unknown)
	}
}

// An entity added to the project LATER must not appear in a frontend that
// declared its routes. That is the allowlist's whole point: an internal
// console should not grow a page because someone added a table.
func TestRouteFilter_NewEntityDoesNotJoinAnAllowlistedFrontend(t *testing.T) {
	t.Parallel()
	fe := config.FrontendConfig{Name: "ops", Routes: []string{"users"}}
	// "coupons" is live in the project but was never declared by this frontend.
	want, _ := routeFilterFor(fe, liveSet("users", "coupons"))

	if want("coupons") {
		t.Error("want(\"coupons\") = true — a newly-added entity leaked into an allowlisted frontend")
	}
}

// A typo'd or renamed slug must be REPORTED. Silently ignoring it produces a
// frontend missing the page its author asked for, indistinguishable from a
// generator bug.
func TestRouteFilter_ReportsUnknownSlugs(t *testing.T) {
	t.Parallel()
	fe := config.FrontendConfig{Name: "ops", Routes: []string{"users", "usres", "gone"}}
	want, unknown := routeFilterFor(fe, liveSet("users", "daemons"))

	if !reflect.DeepEqual(unknown, []string{"usres", "gone"}) {
		t.Errorf("unknown = %v, want [usres gone]", unknown)
	}
	// The valid entry still works — one bad slug must not disable the rest.
	if !want("users") {
		t.Error("a typo'd sibling slug disabled a valid route")
	}
}

// Authors copy slugs out of a URL bar, so "/users" and "Users" must behave
// like "users" rather than silently matching nothing.
func TestRouteFilter_TolerantOfCaseAndLeadingSlash(t *testing.T) {
	t.Parallel()
	fe := config.FrontendConfig{Name: "ops", Routes: []string{"/Users", "USAGE-EVENTS"}}
	want, unknown := routeFilterFor(fe, liveSet("users", "usage-events"))

	if !want("users") || !want("usage-events") {
		t.Error("case/slash variants failed to match their live slugs")
	}
	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want none — the variants are valid slugs", unknown)
	}
}

// sortedSlugs backs the unknown-route warning; an unordered map would print
// the same misconfiguration differently on every run.
func TestSortedSlugs_IsDeterministic(t *testing.T) {
	t.Parallel()
	got := sortedSlugs(liveSet("plans", "users", "daemons"))
	if !reflect.DeepEqual(got, []string{"daemons", "plans", "users"}) {
		t.Errorf("sortedSlugs = %v, want sorted [daemons plans users]", got)
	}
}

// `routes: [none]` is the first-class spelling of "this frontend gets no
// generated CRUD pages" — a marketing site, or an app whose screens are all
// hand-written. It used to be read as a slug named "none" and warned about
// on every generate ("routes [none] match no CRUD entity").
func TestRouteFilter_NoneGeneratesNothingAndWarnsNothing(t *testing.T) {
	t.Parallel()
	for _, spelling := range []string{"none", "NONE", " none "} {
		fe := config.FrontendConfig{Name: "web", Routes: []string{spelling}}
		want, unknown := routeFilterFor(fe, liveSet("users", "daemons"))
		for _, slug := range []string{"users", "daemons", "none"} {
			if want(slug) {
				t.Errorf("routes [%q]: want(%q) = true, want false — none means no pages", spelling, slug)
			}
		}
		if len(unknown) != 0 {
			t.Errorf("routes [%q]: unknown = %v, want none — `none` is a value, not a slug", spelling, unknown)
		}
	}
}

// The sidebar must link only routes this frontend actually has. With an
// allowlist (or `none`) the nav used to advertise every entity in the
// project, i.e. links to pages the page generator had deliberately not
// written.
func TestFilterNavPagesForFrontend(t *testing.T) {
	t.Parallel()
	pages := []templates.NavPageData{{Slug: "users"}, {Slug: "daemons"}}
	live := liveSet("users", "daemons")

	if got := filterNavPagesForFrontend(pages, config.FrontendConfig{Name: "web", Routes: []string{"none"}}, live); len(got) != 0 {
		t.Errorf("routes [none]: nav = %v, want empty", got)
	}
	got := filterNavPagesForFrontend(pages, config.FrontendConfig{Name: "ops", Routes: []string{"users"}}, live)
	if len(got) != 1 || got[0].Slug != "users" {
		t.Errorf("allowlist [users]: nav = %v, want [users]", got)
	}
	if got := filterNavPagesForFrontend(pages, config.FrontendConfig{Name: "web"}, live); len(got) != 2 {
		t.Errorf("no allowlist: nav = %v, want every page", got)
	}
}
