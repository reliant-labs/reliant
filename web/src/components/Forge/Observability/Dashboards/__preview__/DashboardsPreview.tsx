// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY proving harness for the Observability home, the Dashboards index
 * and a dashboard. NOT a product surface.
 *
 * The real ObservabilityHome, DashboardsList and DashboardView inside the
 * real console chrome (ConsolePreviewShell), against the fixture app
 * (createFixtureApi: telemetry, the release ledger, alerting and an in-memory
 * DashboardService) — unauthenticated, no backend — so every state can be
 * looked at in every colour scheme and screenshotted. All state is in the URL:
 *
 *   /forge-dashboards-preview                                  the Observability home
 *   /forge-dashboards-preview?page=list                        Dashboards
 *   /forge-dashboards-preview?page=dashboard&dash=workspace/checkout-latency
 *   …&dash=release/frontend-web   (funnel, issues, sessions)    …&dash=release/overview
 *   …&compare=period|release  &range=24h  &service=api  &tv=1  &version=2
 *   …&env=all|staging|dev
 *   …&case=empty|read-only|error|loading|not-set-up|no-telemetry
 *   …&dark=1&scheme=pure-black
 */

import { useEffect, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code } from "@connectrpc/connect";

import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";

import { ConsolePreviewShell } from "../../../Console/ConsolePreviewShell";
import { withSearch, type ConsolePage } from "../../../Console/paths";
import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT, type FixtureOptions } from "../../__fixtures__/fixtureApi";
import { ObservabilityHome } from "../../Home/ObservabilityHome";
import type { ObsSearch } from "../../obsSearch";
import { compareParam, isDashboardPageKind, rangeParam, refreshParam, type DashboardSearch } from "../dashboardModel";
import { DashboardsList, type DashboardsSearch } from "../DashboardsList";
import { DashboardView } from "../DashboardView";

const BASE = "/forge-dashboards-preview";

const SCHEMES = [
  "professional-blue",
  "refined-neutral",
  "modern-teal",
  "slate",
  "forest",
  "vibrant-pink",
  "energetic-orange",
  "bold-red",
  "purple-classic",
  "pure-black",
] as const;

const CASES: { id: string; label: string; options: FixtureOptions & { loading?: boolean } }[] = [
  { id: "data", label: "the fixture app", options: {} },
  { id: "empty", label: "no dashboards yet", options: { dashboardsEmpty: true } },
  { id: "read-only", label: "member, not admin", options: { dashboardsReadOnly: true } },
  { id: "error", label: "dashboards unavailable", options: { dashboardsFailWith: Code.Unavailable } },
  { id: "loading", label: "loading", options: { loading: true } },
  { id: "not-set-up", label: "observability not set up", options: { failWith: Code.FailedPrecondition } },
  { id: "no-telemetry", label: "nothing sent yet", options: { empty: true } },
];

type PreviewSearch = DashboardsSearch &
  Omit<DashboardSearch, "range" | "compare" | "refresh" | "version"> & {
    page?: string;
    dash?: string;
    env?: string;
    case?: string;
    dark?: boolean | string | number;
    scheme?: string;
    range?: string;
    compare?: string;
    refresh?: string;
    version?: number;
  };

const CONTROL = "rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink";

/** An API whose every call never answers: the loading state, held. */
function pending(api: ObservabilityApi): ObservabilityApi {
  return Object.fromEntries(Object.keys(api).map((key) => [key, () => new Promise(() => undefined)])) as unknown as ObservabilityApi;
}

export default function DashboardsPreview() {
  const search = useSearch({ strict: false }) as PreviewSearch;
  const navigate = useNavigate();
  const env = search.env ?? "prod";
  const caseId = search.case ?? "data";
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0]!;
  // The router parses `dark=1` as JSON, so it arrives as a number.
  const dark = !!search.dark && String(search.dark) !== "0" && String(search.dark) !== "false";
  const scheme = search.scheme ?? "professional-blue";
  const page = search.page ?? "home";
  const [dashKind, ...dashKey] = (search.dash ?? "workspace/checkout-latency").split("/");
  const dashRef: DashboardRef | null = isDashboardPageKind(dashKind) ? { kind: dashKind, projectId: FIXTURE_PROJECT, key: dashKey.join("/") } : null;

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.setAttribute("data-color-scheme", scheme);
  }, [dark, scheme]);

  // A fresh client and fixture per case, so one case's answers (and stars) never leak into the next.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const queryClient = useMemo(() => new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } }), [caseId]);
  const api = useMemo(() => {
    const fixture = createFixtureApi(active.options);
    return active.options.loading ? pending(fixture) : fixture;
  }, [active]);

  const go = (next: Partial<PreviewSearch>, replace = true) =>
    void navigate({ to: BASE, search: (prev: Record<string, unknown>) => ({ ...prev, ...next }), replace });
  const keep = { env, case: search.case, dark: search.dark, scheme: search.scheme };
  const previewHref = (extra: Record<string, unknown>) => withSearch(BASE, { ...keep, ...extra });
  const dashboardHref = (ref: DashboardRef, extra: DashboardSearch = {}) => previewHref({ page: "dashboard", dash: `${ref.kind}/${ref.key}`, ...extra });
  const explore = (view: string) => (s: ObsSearch = {}) => withSearch("/forge-observability-preview", { view, ...s });
  const navigateTo = (href: string) => {
    if (href.startsWith(BASE)) void navigate({ href });
    else window.open(href, "_blank");
  };

  const consolePage: ConsolePage =
    page === "list" ? { kind: "obs-view", view: "dashboards" } : page === "dashboard" && dashRef ? { kind: "dashboard", dashKind: dashRef.kind as "release" | "workspace", key: dashRef.key } : { kind: "observability" };

  const hrefs = {
    logs: explore("logs"),
    traces: explore("traces"),
    profiles: explore("profiles"),
    alerts: withSearch("/forge-observability-preview", { view: "alerts" }),
    dashboards: previewHref({ page: "list" }),
    list: previewHref({ page: "list" }),
    releases: "/forge-releases-preview",
    release: (version: string) => withSearch("/forge-releases-preview", { version }),
    trace: (traceId: string) => withSearch("/forge-observability-preview", { view: "traces", trace: traceId }),
    workload: (name: string) => withSearch("/forge-workloads-preview", { workload: name }),
    dashboard: dashboardHref,
  };

  return (
    <ConsolePreviewShell
      page={consolePage}
      project={FIXTURE_PROJECT}
      envs={FIXTURE_ENVS.map((e) => ({ name: e.name, preview: false }))}
      env={env}
      onEnvChange={(next) => go({ env: next })}
      starred={caseId === "data" ? [{ title: "Checkout latency", dashKind: "workspace", key: "checkout-latency" }, { title: "shop: overview", dashKind: "release", key: "overview" }, { title: "Frontend · web", dashKind: "release", key: "frontend-web" }] : []}
      dashboardTitle={page === "dashboard" ? dashRef?.key : undefined}
      controls={
        <>
          <select data-testid="preview-page" value={page} onChange={(e) => go({ page: e.target.value === "home" ? undefined : e.target.value })} className={CONTROL}>
            <option value="home">Observability home</option>
            <option value="list">Dashboards</option>
            <option value="dashboard">A dashboard</option>
          </select>
          <select data-testid="preview-case" value={caseId} onChange={(e) => go({ case: e.target.value === "data" ? undefined : e.target.value })} className={CONTROL}>
            {CASES.map((c) => (
              <option key={c.id} value={c.id}>
                {c.label}
              </option>
            ))}
          </select>
          <select data-testid="preview-scheme" value={scheme} onChange={(e) => go({ scheme: e.target.value })} className={CONTROL}>
            {SCHEMES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
          <button type="button" data-testid="preview-dark" onClick={() => go({ dark: dark ? undefined : "1" })} className={CONTROL}>
            {dark ? "Dark" : "Light"}
          </button>
        </>
      }
    >
      <QueryClientProvider client={queryClient}>
        <ObservabilityApiProvider api={api}>
          {page === "list" ? (
            <DashboardsList
              key={caseId}
              project={FIXTURE_PROJECT}
              env={env}
              environments={FIXTURE_ENVS}
              search={{ q: search.q, source: search.source, owner: search.owner }}
              onSearch={(next) => go(next)}
              hrefs={hrefs}
              navigateTo={navigateTo}
            />
          ) : page === "dashboard" && dashRef ? (
            <DashboardView
              key={`${caseId}-${search.dash}`}
              project={FIXTURE_PROJECT}
              dashRef={dashRef}
              env={env}
              environments={FIXTURE_ENVS}
              search={{
                range: rangeParam(search.range),
                service: search.service,
                compare: search.compare ? compareParam(search.compare) : undefined,
                refresh: search.refresh ? refreshParam(search.refresh) : undefined,
                tv: !!search.tv || undefined,
                version: typeof search.version === "number" ? search.version : undefined,
                release: search.release,
              }}
              onSearch={(next) => go(next as Partial<PreviewSearch>)}
              onEnvChange={(next) => go({ env: next })}
              hrefs={hrefs}
              navigateTo={navigateTo}
            />
          ) : (
            <ObservabilityHome
              key={caseId}
              project={FIXTURE_PROJECT}
              env={env}
              environments={FIXTURE_ENVS}
              range={rangeParam(search.range) ?? "1h"}
              onRange={(range) => go({ range })}
              hrefs={hrefs}
              navigateTo={navigateTo}
            />
          )}
        </ObservabilityApiProvider>
      </QueryClientProvider>
    </ConsolePreviewShell>
  );
}
