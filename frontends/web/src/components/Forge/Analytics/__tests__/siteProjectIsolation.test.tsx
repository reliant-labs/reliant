// Copyright (c) 2026 Reliant Labs

/**
 * Two projects whose browsers report the same service.namespace and the same
 * bare frontend name ("web"): only the gateway's reliant.env.id stamp tells
 * their spans apart. The fake server below does what control-plane does with
 * a sql panel: it resolves QueryScope.project_id to that project's
 * environment ids, binds them as {env_ids}, and runs the SQL as written, so a
 * statement without the mandatory env predicate (or a request without the
 * project) reads the other project's spans, as ClickHouse would.
 */

import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, renderHook, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useSearch: () => ({}),
  useNavigate: () => vi.fn(),
}));

const { SitePicker } = await import("../siteInsightsParts");
const { useSiteSpeedSummary } = await import("../useSiteSpeedSummary");
const { siteServiceNames } = await import("../speedInsightsModel");
const { ObservabilityApiProvider } = await import("@/services/controlPlane/observability/ObservabilityApiContext");
type ObservabilityApi = import("@/services/controlPlane/observability/ObservabilityApiContext").ObservabilityApi;
type QueryPanelRequest = Parameters<ObservabilityApi["queryPanel"]>[0];
type PanelResult = Awaited<ReturnType<ObservabilityApi["queryPanel"]>>;

const ENVS_OF_PROJECT: Record<string, string[]> = { shop: ["shop-prod"], other: ["other-prod"] };
// The other project's browsers report namespace "shop" too: producer-controlled, so it collides.
const SPANS = [
  { envId: "shop-prod", namespace: "shop", service: "web", views: 3 },
  { envId: "shop-prod", namespace: "shop", service: "docs", views: 2 },
  { envId: "other-prod", namespace: "shop", service: "web", views: 99 },
  { envId: "other-prod", namespace: "shop", service: "admin", views: 7 },
];
const ENV_PREDICATE = "has({env_ids:Array(String)}, ResourceAttributes['reliant.env.id'])";

function serve(req: QueryPanelRequest): PanelResult {
  const sql = req.panel.sql ?? "";
  // envscope.Resolve: a named project binds every live environment of it; no project binds none.
  const envIds = req.projectId !== undefined ? (ENVS_OF_PROJECT[req.projectId] ?? []) : [];
  const envEnforced = sql.includes(ENV_PREDICATE) && !sql.includes("empty({env_ids");
  const namespace = /\['service\.namespace'\] = '([^']*)'/.exec(sql)?.[1];
  const spans = SPANS.filter(
    (s) => (!envEnforced || envIds.includes(s.envId)) && (namespace === undefined || s.namespace === namespace) && (req.services.length === 0 || req.services.includes(s.service))
  );
  const views = new Map<string, number>();
  for (const s of spans) views.set(s.service, (views.get(s.service) ?? 0) + s.views);
  const summary = req.panel.id.endsWith("summary");
  return {
    kind: "table",
    columns: ["views"],
    rows: summary ? [{ group: {}, values: [spans.reduce((n, s) => n + s.views, 0)] }] : [...views].map(([service, n]) => ({ group: { service }, values: [n] })),
  } as unknown as PanelResult;
}

function setup() {
  const queryPanel = vi.fn(async (req: QueryPanelRequest) => serve(req));
  const api = { currentOrgId: async () => "org", queryPanel } as unknown as ObservabilityApi;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ObservabilityApiProvider api={api}>{children}</ObservabilityApiProvider>
    </QueryClientProvider>
  );
  return { queryPanel, wrapper };
}

describe("Site insights across projects with colliding service names", () => {
  it("the picker lists only the current project's sites and counts", async () => {
    const { queryPanel, wrapper } = setup();
    render(<SitePicker scope={{ orgId: "org", projectId: "shop", envIds: [], filters: [] }} range="7d" />, { wrapper });
    const picker = await screen.findByRole("combobox");
    const labels = Array.from(picker.querySelectorAll("option"), (o) => o.textContent);
    expect(labels).toEqual(["All sites", "web · 3", "docs · 2"]);
    expect(queryPanel.mock.calls[0]![0]).toMatchObject({ projectId: "shop", services: [] });
  });

  it("a selected site's panels read only the current project's spans of that name", async () => {
    const { queryPanel, wrapper } = setup();
    const { result } = renderHook(() => useSiteSpeedSummary({ project: "shop", services: siteServiceNames("shop", "web") }), { wrapper });
    // The scope (and so the read) waits for the org; views is 99 + 3 if the other project's "web" leaks in.
    await waitFor(() => expect(result.current.summary).toBeDefined());
    expect(result.current.views).toBe(3);
    expect(queryPanel.mock.calls[0]![0]).toMatchObject({ projectId: "shop", services: ["shop-web", "web"] });
  });
});
