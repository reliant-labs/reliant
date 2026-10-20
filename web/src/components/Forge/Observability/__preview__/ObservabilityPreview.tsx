// Copyright (c) 2025 Reliant Labs

/**
 * Proving harness for the Observability area. NOT a product surface.
 *
 * Renders the real ObservabilityPanel, inside the real forge shell, against
 * the in-memory fixture API — unauthenticated and with no backend — so every
 * view and state can be looked at in every colour scheme, screenshotted, and
 * driven by the Playwright smoke (e2e/observability.spec.ts). Dev builds only:
 * the route sits behind DevOnlyRoute in routes.tsx.
 *
 * Both places the panel lives are here: the project's area (every
 * environment, filterable and groupable) and one environment's tab (pinned to
 * prod). The URL state the pages keep in search params is held in component
 * state, seeded from this page's own query string, so a screenshot of any
 * state is a link: `?place=project&view=workloads&groupBy=deployment.environment.name`.
 */

import { useMemo, useState } from "react";
import { Code } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import PageHeader from "@/components/forge-ui/page_header";
import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { ConsolePreviewShell } from "../../Console/ConsolePreviewShell";
import ObservabilityPanel, { type ObsPlace } from "../ObservabilityPanel";
import type { ObsNavigate, ObsSearch } from "../obsSearch";
import { FIXTURE_DECLARED } from "../__fixtures__/fixtureAlerting";
import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT, type FixtureOptions } from "../__fixtures__/fixtureApi";

/** The fixture environments with what each declares (rules, routes, observability policy). */
const ENVIRONMENTS = FIXTURE_ENVS.map((env) => ({ ...env, declared: FIXTURE_DECLARED[env.id] ?? null }));

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

const CASES: { id: string; label: string; options: FixtureOptions }[] = [
  { id: "data", label: "telemetry flowing", options: {} },
  { id: "not-set-up", label: "org not set up (FailedPrecondition)", options: { failWith: Code.FailedPrecondition } },
  { id: "empty", label: "no data in range", options: { empty: true } },
  { id: "denied", label: "no permission", options: { failWith: Code.PermissionDenied } },
  { id: "unavailable", label: "service unavailable", options: { failWith: Code.Unavailable } },
  { id: "quota", label: "query quota hit", options: { failWith: Code.ResourceExhausted } },
  // Usage states (view=usage): the allowance ladder as an org on each rung sees it.
  { id: "usage-near", label: "usage: 85% of allowance", options: { usage: { usedBytes: 8.5 * 2 ** 30 } } },
  { id: "usage-over", label: "usage: over, unfunded (dropping)", options: { usage: { usedBytes: 11 * 2 ** 30, state: "over_100", usageState: "over_100" } } },
  { id: "usage-dropping", label: "usage: over 150% (most dropped)", options: { usage: { usedBytes: 16 * 2 ** 30, state: "over_150", usageState: "over_150" } } },
  { id: "usage-billed", label: "usage: over, funded (billed)", options: { usage: { usedBytes: 14 * 2 ** 30, state: "ok", usageState: "over_100", funded: true, overageCents: 100, projectedOverageCents: 180 } } },
  { id: "usage-cap", label: "usage: spend cap reached", options: { usage: { usedBytes: 230 * 2 ** 30, state: "over_100", usageState: "over_150", funded: true, spendCapReached: true, overageCents: 5000 } } },
  { id: "usage-exempt", label: "usage: exempt org", options: { usage: { exempt: true, usedBytes: 40 * 2 ** 30, usageState: "over_150" } } },
  // Alerting states (view=alerts).
  { id: "alerting-not-configured", label: "alerting: not configured", options: { alerting: "not-configured" } },
  { id: "alerting-viewer", label: "alerting: member, not admin", options: { alerting: "viewer" } },
  { id: "alerting-quiet", label: "alerting: nothing firing", options: { alerting: "quiet" } },
  // Release markers: the ledger's answers. (DORA lives on the Releases page: /forge-releases-preview.)
  { id: "delivery-empty", label: "releases: nothing promoted", options: { delivery: "empty" } },
  { id: "delivery-not-member", label: "releases: not a member", options: { delivery: "not-member" } },
  { id: "delivery-env-denied", label: "releases: env from another org", options: { delivery: "env-denied" } },
  { id: "delivery-truncated", label: "releases: truncated (limit 5)", options: { releaseLimit: 5 } },
];

const LIST_KEYS = new Set(["envs", "f", "groupBy"]);

/** The preview's query string as ObsSearch: list keys repeat (`?f=a&f=b`), `edit` is JSON. */
function searchFromUrl(params: URLSearchParams): ObsSearch {
  const out: Record<string, unknown> = {};
  for (const key of new Set(params.keys())) {
    if (key === "case" || key === "place") continue;
    if (LIST_KEYS.has(key)) out[key] = params.getAll(key);
    else if (key === "edit") {
      try {
        out.edit = JSON.parse(params.get(key) ?? "");
      } catch {
        // A malformed edit is dropped, as the real route's schema drops it.
      }
    } else if (key === "live" || key === "errors") out[key] = params.get(key) === "true";
    else out[key] = params.get(key);
  }
  return out as ObsSearch;
}

export default function ObservabilityPreview() {
  const initial = new URLSearchParams(window.location.search);
  const [scheme, setScheme] = useState(() => document.documentElement.getAttribute("data-color-scheme") ?? "professional-blue");
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark"));
  const [caseId, setCaseId] = useState(() => initial.get("case") ?? CASES[0].id);
  const [placeKind, setPlaceKind] = useState<"project" | "env">(() => (initial.get("place") === "project" ? "project" : "env"));
  const [search, setSearch] = useState<ObsSearch>(() => searchFromUrl(initial));

  const active = CASES.find((c) => c.id === caseId) ?? CASES[0];
  // A fresh client per case, so one case's cached answers never leak into the next.
  const queryClient = useMemo(
    () => new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [caseId]
  );
  const api = useMemo(() => createFixtureApi(active.options), [active]);

  const navigate: ObsNavigate = (next) => {
    setSearch((prev) => {
      const merged: Record<string, unknown> = { ...prev, ...next };
      for (const key of Object.keys(merged)) if (merged[key] === undefined) delete merged[key];
      return merged as ObsSearch;
    });
  };

  const prod = ENVIRONMENTS[2];
  const place: ObsPlace =
    placeKind === "project"
      ? { kind: "project", project: FIXTURE_PROJECT, environments: ENVIRONMENTS }
      : {
          kind: "env",
          env: prod,
          onWiden: (next) => {
            setPlaceKind("project");
            setSearch(next);
          },
        };

  function applyScheme(next: string) {
    setScheme(next);
    document.documentElement.setAttribute("data-color-scheme", next);
  }

  function applyDark(next: boolean) {
    setDark(next);
    document.documentElement.classList.toggle("dark", next);
  }

  return (
    <ConsolePreviewShell
      page={placeKind === "project" ? { kind: "observability" } : { kind: "environment", env: "prod" }}
      controls={
        <>
          <select
            data-testid="preview-place"
            value={placeKind}
            onChange={(e) => setPlaceKind(e.target.value as "project" | "env")}
            className="rounded-md border border-border bg-card px-2 py-1 text-sm text-foreground"
          >
            <option value="env">env page tab (prod)</option>
            <option value="project">project area (all environments)</option>
          </select>
          <select
            data-testid="preview-case"
            value={caseId}
            onChange={(e) => setCaseId(e.target.value)}
            className="rounded-md border border-border bg-card px-2 py-1 text-sm text-foreground"
          >
            {CASES.map((c) => (
              <option key={c.id} value={c.id}>
                {c.label}
              </option>
            ))}
          </select>
          <select
            data-testid="preview-scheme"
            value={scheme}
            onChange={(e) => applyScheme(e.target.value)}
            className="rounded-md border border-border bg-card px-2 py-1 text-sm text-foreground"
          >
            {SCHEMES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
          <button
            type="button"
            data-testid="preview-dark"
            onClick={() => applyDark(!dark)}
            className="rounded-md border border-border bg-card px-3 py-1 text-sm text-foreground"
          >
            {dark ? "Dark" : "Light"}
          </button>
        </>
      }
    >
      <div className="space-y-6">
        {placeKind === "project" && (
          <PageHeader title="Observability" meta={<span>{FIXTURE_PROJECT}</span>} className="mb-0" />
        )}
        <QueryClientProvider client={queryClient}>
          <ObservabilityApiProvider api={api}>
            <ObservabilityPanel key={`${caseId}-${placeKind}`} place={place} search={search} navigate={navigate} />
          </ObservabilityApiProvider>
        </QueryClientProvider>
      </div>
    </ConsolePreviewShell>
  );
}
