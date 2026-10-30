// Copyright (c) 2025 Reliant Labs

/**
 * Every ad-hoc choice in the Observability area lives in the URL (owner
 * decision 2), so a changed view is a link. These pin the round trip: what a
 * view writes, through tanstack-router's own search serialization and the
 * route schema, comes back as what it wrote — and a hand-edited or stale link
 * narrows to what still makes sense instead of failing the route.
 */

import { describe, expect, it } from "vitest";
import { defaultParseSearch, defaultStringifySearch } from "@tanstack/react-router";

import {
  forgeConsoleDashboardSearchSchema,
  forgeConsoleDashboardsSearchSchema,
  forgeOrgObsSearchSchema,
} from "@/routeSchemas";

import { MAX_FILTERS, MAX_GROUP_BY, MAX_IN_VALUES } from "../attributes";
import { FIXTURE_ENVS, FIXTURE_PROJECT } from "../__fixtures__/fixtureApi";
import { envIdsOf, type ObsPlace } from "../obsPlace";
import {
  filterParam,
  filterTokensOf,
  formatFilterToken,
  groupByOf,
  groupLabel,
  parseFilterToken,
  scopeFiltersOf,
  type FilterToken,
} from "../obsFilters";
import { OBS_SEARCH_KEYS, type ObsSearch } from "../obsSearch";

const PROJECT_PLACE: ObsPlace = { kind: "project", project: FIXTURE_PROJECT, environments: [...FIXTURE_ENVS] };

/** What the router does to a search object on its way into the address bar and back. */
function throughTheUrl(search: Record<string, unknown>, schema = forgeOrgObsSearchSchema) {
  const url = defaultStringifySearch(search);
  return { url, parsed: schema.parse(defaultParseSearch(url)) };
}

describe("filter tokens", () => {
  it.each<[string, FilterToken]>([
    ["service.version:v1.8.2", { key: "service.version", op: "eq", value: "v1.8.2" }],
    ["status!:error", { key: "status", op: "neq", value: "error" }],
    ["http.route~orders", { key: "http.route", op: "contains", value: "orders" }],
    ["k8s.container.name:*", { key: "k8s.container.name", op: "exists", value: "" }],
    // Everything after the operator is the value: colons and spaces included.
    ["server.address:db:5432", { key: "server.address", op: "eq", value: "db:5432" }],
    ["http.route~/orders/{id} list", { key: "http.route", op: "contains", value: "/orders/{id} list" }],
  ])("reads %s and writes it back unchanged", (raw, token) => {
    expect(parseFilterToken(raw)).toEqual(token);
    expect(formatFilterToken(token)).toBe(raw);
  });

  it.each(["", "no-operator", ":value", "service.name:", "bad key:v", `k:${"x".repeat(1025)}`])("drops %j instead of failing", (raw) => {
    expect(parseFilterToken(raw)).toBeNull();
  });

  it("keeps URL order, drops duplicates and junk", () => {
    expect(filterTokensOf(["a:1", "junk", "b!:2", "a:1"]).map(formatFilterToken)).toEqual(["a:1", "b!:2"]);
    expect(filterParam([])).toBeUndefined();
  });
});

describe("tokens → QueryScope.filters", () => {
  it("merges two `is` on one key into `in` (how two versions are compared), at the first one's position", () => {
    const tokens = filterTokensOf(["service.version:v1", "status!:error", "service.version:v2", "service.version:v1"]);
    expect(scopeFiltersOf(tokens)).toEqual([
      { key: "service.version", op: "in", values: ["v1", "v2"] },
      { key: "status", op: "neq", value: "error" },
    ]);
  });

  it("passes contains and exists through, and a single `is` stays `eq`", () => {
    expect(scopeFiltersOf(filterTokensOf(["http.route~orders", "k8s.pod.name:*", "service.name:api"]))).toEqual([
      { key: "http.route", op: "contains", value: "orders" },
      { key: "k8s.pod.name", op: "exists" },
      { key: "service.name", op: "eq", value: "api" },
    ]);
  });

  it("caps at the server's limits rather than sending a request it will refuse", () => {
    const many = Array.from({ length: MAX_FILTERS + 5 }, (_, i): FilterToken => ({ key: `k${i}`, op: "neq", value: "x" }));
    expect(scopeFiltersOf(many)).toHaveLength(MAX_FILTERS);
    const values = Array.from({ length: MAX_IN_VALUES + 5 }, (_, i): FilterToken => ({ key: "service.version", op: "eq", value: `v${i}` }));
    expect(scopeFiltersOf(values)[0].values).toHaveLength(MAX_IN_VALUES);
  });
});

describe("group-by", () => {
  it("keeps shaped keys, once each, at most three", () => {
    expect(groupByOf(["deployment.environment.name", "x y", "service.version", "deployment.environment.name", "status", "rpc.method"])).toEqual([
      "deployment.environment.name",
      "service.version",
      "status",
    ]);
    expect(groupByOf(undefined)).toEqual([]);
    expect(MAX_GROUP_BY).toBe(3);
  });

  it("labels a group in group-by order, naming an empty value", () => {
    expect(groupLabel({ "service.version": "v2", "deployment.environment.name": "prod" }, ["deployment.environment.name", "service.version"])).toBe(
      "prod · v2"
    );
    expect(groupLabel({ status: "" }, ["status"])).toBe("(none)");
  });
});

describe("environment is a filter", () => {
  it("resolves picked names to ids on the project area, empty meaning every environment", () => {
    expect(envIdsOf(PROJECT_PLACE, undefined)).toEqual([]);
    expect(envIdsOf(PROJECT_PLACE, ["prod", "staging"])).toEqual(["env-prod", "env-staging"]);
    // A name from an old link that the project no longer has is dropped, not sent.
    expect(envIdsOf(PROJECT_PLACE, ["prod", "qa"])).toEqual(["env-prod"]);
  });

  it("pins the env page to its own environment whatever the URL says", () => {
    expect(envIdsOf({ kind: "env", env: { id: "env-prod", name: "prod" } }, ["staging"])).toEqual(["env-prod"]);
  });
});

describe("the URL round trip", () => {
  it("brings every ad-hoc choice back as it was written: envs, filters, group-by", () => {
    const search: ObsSearch = {
      view: "traces",
      range: "6h",
      envs: ["prod", "staging"],
      f: ["service.version:v1.8.3", "http.route~orders", "k8s.container.name:*", "server.address:db:5432"],
      groupBy: ["deployment.environment.name", "service.version"],
    };
    const { parsed } = throughTheUrl({ project: "p-1", ...search });
    expect(parsed).toEqual({ project: "p-1", ...search });
    expect(filterTokensOf(parsed.f)).toHaveLength(4);
  });

  it("survives values that look like JSON (a numeric service, a boolean-looking version)", () => {
    const { parsed } = throughTheUrl({ service: "123", f: ["service.version:true", "http.response.status_code:500"] });
    expect(parsed).toMatchObject({ service: "123", f: ["service.version:true", "http.response.status_code:500"] });
  });

  it("drops a bad value instead of failing the route", () => {
    const parsed = forgeOrgObsSearchSchema.parse({
      view: "heatmaps",
      range: "90d",
      groupBy: "not-a-list",
      envs: ["prod"],
    });
    expect(parsed).toEqual({ envs: ["prod"] });
  });

  it("lists every key the area owns, so leaving the tab clears them all", () => {
    expect([...OBS_SEARCH_KEYS].sort()).toEqual(
      [
        "alerts", "apps", "cmpWindow", "compare", "envs", "errors", "f", "groupBy",
        "live", "minDuration", "profileType", "q", "range", "rule", "service", "severity", "span", "trace", "view",
        "issue", "istatus", "platform", "isort", "assignee",
        "session", "t", "replay", "user", "cursor",
        "declared", "internal", "rpcId", "rpcService",
      ].sort()
    );
  });

  it("carries the Errors view's assignee filter: me or a user id", () => {
    for (const assignee of ["me", "user-ada"]) expect(throughTheUrl({ view: "errors", assignee }).parsed.assignee).toBe(assignee);
  });

  it("carries a release comparison and its window, and drops one out of bounds", () => {
    const { parsed } = throughTheUrl({ view: "overview", compare: "env-prod~1760000000000~promo-031", cmpWindow: "1h" });
    expect(parsed).toMatchObject({ view: "overview", compare: "env-prod~1760000000000~promo-031", cmpWindow: "1h" });
    expect(throughTheUrl({ cmpWindow: "2y" }).parsed.cmpWindow).toBeUndefined();
    // 24h is past the server's 6-hour maximum: dropped, so the server's default applies.
    expect(throughTheUrl({ cmpWindow: "24h" }).parsed.cmpWindow).toBeUndefined();
    // Delivery is no view any more (its figures are the Releases page's), and
    // neither are Dashboards (console pages of their own): an old link opens the overview.
    expect(throughTheUrl({ view: "delivery" }).parsed.view).toBeUndefined();
    expect(throughTheUrl({ view: "dashboards" }).parsed.view).toBeUndefined();
  });

  it("carries the Errors view's issue and filters, and drops a platform or sort it does not know", () => {
    const { parsed } = throughTheUrl({ view: "errors", issue: "iss-1", istatus: "resolved,ignored", platform: "browser", isort: "events" });
    expect(parsed).toMatchObject({ view: "errors", issue: "iss-1", istatus: "resolved,ignored", platform: "browser", isort: "events" });
    expect(throughTheUrl({ platform: "mobile", isort: "random" }).parsed).toMatchObject({ platform: undefined, isort: undefined });
  });

  it("carries the alerts tab and an open rule, and drops a tab it does not know", () => {
    const { parsed } = throughTheUrl({ view: "alerts", alerts: "rules", rule: "prod/CheckoutP95High" });
    expect(parsed).toMatchObject({ view: "alerts", alerts: "rules", rule: "prod/CheckoutP95High" });
    expect(throughTheUrl({ view: "alerts", alerts: "nonsense" }).parsed.alerts).toBeUndefined();
  });
});

describe("a dashboard's link (Share)", () => {
  it("carries the window, the service, the comparison, the refresh and full screen as they were written", () => {
    const search = { env: "prod", range: "6h", service: "api", compare: "release", refresh: "30s", tv: true };
    expect(throughTheUrl(search, forgeConsoleDashboardSearchSchema as never).parsed).toEqual(search);
  });

  it("round-trips independent viewer controls including false and array selections", () => {
    const search = { env: "prod", service: "worker", rpcService: "orders.v1.OrdersService", rpcMethod: "CreateOrder", rpcProcedure: "/orders.v1.OrdersService/CreateOrder", variables: { client: "web" }, breakdown: "none", hiddenSections: ["resources"], hiddenPanels: ["p95", "errors"], releases: false, alerts: false };
    expect(throughTheUrl(search, forgeConsoleDashboardSearchSchema as never).parsed).toEqual(search);
    expect(forgeConsoleDashboardSearchSchema.parse({ hiddenSections: "resources", alerts: "false" })).toEqual({});
  });

  it("opens an older version or the release it shipped in", () => {
    expect(throughTheUrl({ version: 3 }, forgeConsoleDashboardSearchSchema as never).parsed).toEqual({ version: 3 });
    expect(throughTheUrl({ release: "v1.8.2" }, forgeConsoleDashboardSearchSchema as never).parsed).toEqual({ release: "v1.8.2" });
  });

  it("drops what it does not know instead of failing the route", () => {
    const parsed = forgeConsoleDashboardSearchSchema.parse({ range: "90d", compare: "yesterday", refresh: "1s", version: -2, tv: "yes" });
    expect(parsed).toEqual({});
  });

  it("keeps the index's search and filters", () => {
    const search = { env: "all", view: "all", q: "checkout", group: "services", source: "workspace", owner: "sean" };
    expect(throughTheUrl(search, forgeConsoleDashboardsSearchSchema as never).parsed).toEqual(search);
    expect(forgeConsoleDashboardsSearchSchema.parse({ source: "template", view: "grid", group: "misc" })).toEqual({});
  });
});
