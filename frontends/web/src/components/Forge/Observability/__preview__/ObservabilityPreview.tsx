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
 * The panel as the console mounts it: one view per page (the console nav
 * picks it; here the view picker does), every environment of the project in
 * scope. The URL state the pages keep in search params is held in component
 * state, seeded from this page's own query string, so a screenshot of any
 * state is a link: `?view=traces&groupBy=deployment.environment.name`.
 */

import { useMemo, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { ObservabilityBillingApiContext } from "@/hooks/observability-billing-queries";
const legacyOnly = async () => { throw new ConnectError("Legacy usage fixture", Code.Unimplemented); };
const legacyBillingApi = { getObservabilityUsage: legacyOnly, listObservabilityUsage: legacyOnly, getObservabilityStorage: legacyOnly, getObservabilityLimits: legacyOnly, updateObservabilityLimits: legacyOnly };
import { QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query";

import PageHeader from "@/components/forge-ui/page_header";
import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { ConsolePreviewShell } from "../../Console/ConsolePreviewShell";
import { isObsNavView, OBS_NAV_LABELS, type ObsNavView } from "../../Console/paths";
import ObservabilityPanel, { type ObsPlace } from "../ObservabilityPanel";
import type { ObsNavigate, ObsSearch } from "../obsSearch";
import { FIXTURE_DECLARED } from "../__fixtures__/fixtureAlerting";
import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT, type FixtureOptions } from "../__fixtures__/fixtureApi";
import { FIXTURE_SIGNALS } from "../__fixtures__/fixtureSignals";

/** The fixture environments with what each declares (rules, routes, observability policy). */
const ENVIRONMENTS = FIXTURE_ENVS.map((env) => ({ ...env, declared: FIXTURE_DECLARED[env.id] ?? null }));

const SCHEMES = [
  "professional-blue",
  "refined-neutral",
  "modern-teal",
  "slate",
  "purple-classic",
  "pure-black",
] as const;

const CASES: { id: string; label: string; options: FixtureOptions }[] = [
  { id: "data", label: "telemetry flowing", options: {} },
  { id: "not-set-up", label: "org not set up (FailedPrecondition)", options: { failWith: Code.FailedPrecondition } },
  { id: "nothing-yet", label: "set up, nothing arrived this week (getting started)", options: { empty: true, signals: { services: [], truncated: false } } },
  { id: "empty", label: "no data in range", options: { empty: true } },
  { id: "graph-unknown", label: "graph: coverage/catalog unavailable", options: { graphUnknown: true } },
  { id: "graph-retained-denied", label: "graph: catalog revoked on refetch", options: { graphReview: "retained-denied" } },
  { id: "graph-retained-unavailable", label: "graph: catalog unavailable on refetch", options: { graphReview: "retained-unavailable" } },
  { id: "graph-rpc-201", label: "graph: exact method beyond enumeration", options: { graphReview: "rpc-201" } },
  { id: "graph-artifacts-21", label: "graph: 21st artifact disagrees", options: { graphReview: "artifacts-21" } },
  { id: "graph-unsupported", label: "graph: older unsupported server", options: { graphReview: "unsupported" } },
  { id: "window-empty", label: "no data in range, data this week", options: { empty: true, signals: FIXTURE_SIGNALS } },
  { id: "denied", label: "no permission", options: { failWith: Code.PermissionDenied } },
  { id: "unavailable", label: "service unavailable", options: { failWith: Code.Unavailable } },
  { id: "quota", label: "query quota hit", options: { failWith: Code.ResourceExhausted } },
  // Alerting states (view=alerts).
  { id: "alerting-not-configured", label: "alerting: not configured", options: { alerting: "not-configured" } },
  { id: "alerting-viewer", label: "alerting: member, not admin", options: { alerting: "viewer" } },
  { id: "alerting-quiet", label: "alerting: nothing firing", options: { alerting: "quiet" } },
  // Release markers: the ledger's answers. (DORA lives on the Releases page: /forge-releases-preview.)
  { id: "delivery-empty", label: "releases: nothing promoted", options: { delivery: "empty" } },
  { id: "delivery-not-member", label: "releases: not a member", options: { delivery: "not-member" } },
  { id: "delivery-env-denied", label: "releases: env from another org", options: { delivery: "env-denied" } },
  { id: "delivery-truncated", label: "releases: truncated (limit 5)", options: { releaseLimit: 5 } },
  // Errors (view=errors, &issue=<id> for one): the issue inbox's states.
  { id: "issues-empty", label: "errors: no issues", options: { issues: "empty" } },
  { id: "issues-not-configured", label: "errors: older control plane (Unimplemented)", options: { issues: "not-configured" } },
  { id: "issues-conflict", label: "errors: first triage loses a race (Aborted)", options: { issueConflict: true } },
  // Discussion on an issue (&issue=iss-orders-deadline has a thread, an assignee, a chat and a related error; iss-worker-conns is unassigned).
  { id: "issues-assign-conflict", label: "errors: first assignment loses a race (Aborted)", options: { assignConflict: true } },
  { id: "issues-merged", label: "errors: iss-checkout-total merged into iss-orders-deadline (its entries labelled; writes refused)", options: { mergedIssue: true } },
  { id: "issues-member", label: "errors: a plain member (edits nothing of others', deletes nothing)", options: { issueReader: "grace" } },
  { id: "issues-no-person", label: "errors: a credential that acts for no person (cannot comment)", options: { issueReader: "none" } },
  { id: "issues-roster-denied", label: "errors: member list closed (needs token:read)", options: { rosterDenied: true } },
  // Sessions (view=sessions, &session=<id> for one; sess-1a8c2f1d91e is the failed checkout, sess-live01 is still recording).
  { id: "sessions-empty", label: "sessions: none in range", options: { sessions: "empty" } },
  { id: "sessions-not-configured", label: "sessions: older control plane (Unimplemented)", options: { sessions: "not-configured" } },
  { id: "replay-partial", label: "replay: stops before the session did", options: { replay: "partial" } },
  { id: "replay-no-snapshot", label: "replay: full snapshot lost (unplayable)", options: { replay: "no-snapshot" } },
  { id: "replay-unreadable", label: "replay: two events unreadable", options: { replay: "unreadable" } },
  { id: "replay-none", label: "replay: recorded, nothing left", options: { replay: "none" } },
  // Release health (a comparison open: &compare=<envId>~<ms>~<promotionId>, or Compare from the release strip).
  // The "data" case judges prod's v1.8.3 a FAIL; these force the other answers.
  { id: "health-pass", label: "release health: PASS", options: { health: "pass" } },
  { id: "health-inconclusive", label: "release health: INCONCLUSIVE (window open)", options: { health: "inconclusive" } },
  { id: "health-not-evaluated", label: "release health: PASS, alerts and issues not checked", options: { health: "not-evaluated" } },
  { id: "health-unimplemented", label: "release health: older control plane (Unimplemented)", options: { health: "unimplemented" } },
];

/** The views the panel hosts (Metrics and Dashboards are console pages of their own). */
const PANEL_VIEWS: ObsNavView[] = ["graph", "rpc", "errors", "sessions", "logs", "traces", "profiles", "alerts"];

const LIST_KEYS = new Set(["envs", "f", "groupBy", "cursor"]);

/** The preview's query string as ObsSearch: list keys repeat (`?f=a&f=b`), `edit` is JSON. */
function searchFromUrl(params: URLSearchParams): ObsSearch {
  const out: Record<string, unknown> = {};
  for (const key of new Set(params.keys())) {
    if (key === "case") continue;
    if (LIST_KEYS.has(key)) out[key] = params.getAll(key);
    else if (key === "edit") {
      try {
        out.edit = JSON.parse(params.get(key) ?? "");
      } catch {
        // A malformed edit is dropped, as the real route's schema drops it.
      }
    } else if (key === "live" || key === "errors" || key === "replay" || key === "declared" || key === "internal") out[key] = params.get(key) === "true";
    else if (key === "t") out[key] = Number(params.get(key)) || undefined;
    else out[key] = params.get(key);
  }
  return out as ObsSearch;
}

function CatalogRefetchProbe() {
  const client = useQueryClient();
  return <button className="text-sm text-primary underline" onClick={() => void client.invalidateQueries({ predicate: (q) => q.queryKey.includes("graph-catalog") || q.queryKey.includes("graph-catalog-exact") })}>Preview: refetch retained catalog</button>;
}

export default function ObservabilityPreview() {
  const initial = new URLSearchParams(window.location.search);
  const [scheme, setScheme] = useState(() => document.documentElement.getAttribute("data-color-scheme") ?? "professional-blue");
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark"));
  const [caseId, setCaseId] = useState(() => initial.get("case") ?? CASES[0].id);
  const [search, setSearch] = useState<ObsSearch>(() => ({ view: "errors", ...searchFromUrl(initial) }));

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

  const place: ObsPlace = { kind: "project", project: FIXTURE_PROJECT, environments: ENVIRONMENTS };
  const view = (isObsNavView(search.view) ? search.view : "errors") as ObsNavView;

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
      page={{ kind: "obs-view", view }}
      controls={
        <>
          <select
            data-testid="preview-view"
            value={view}
            onChange={(e) => navigate({ view: e.target.value as ObsSearch["view"] })}
            className="rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink"
          >
            {PANEL_VIEWS.map((id) => (
              <option key={id} value={id}>
                {OBS_NAV_LABELS[id]}
              </option>
            ))}
          </select>
          <select
            data-testid="preview-case"
            value={caseId}
            onChange={(e) => setCaseId(e.target.value)}
            className="rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink"
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
            className="rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink"
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
            className="rounded-md border border-border bg-surface px-3 py-1 text-sm text-ink"
          >
            {dark ? "Dark" : "Light"}
          </button>
        </>
      }
    >
      <div className="space-y-6">
        <PageHeader title={OBS_NAV_LABELS[view]} meta={<span>All environments</span>} className="mb-0" />
        <QueryClientProvider client={queryClient}>
          <ObservabilityApiProvider api={api}>
            <ObservabilityBillingApiContext.Provider value={legacyBillingApi}>
              {active.options.graphReview?.startsWith("retained-") && <CatalogRefetchProbe />}
              <ObservabilityPanel key={caseId} place={place} search={search} navigate={navigate} />
            </ObservabilityBillingApiContext.Provider>
          </ObservabilityApiProvider>
        </QueryClientProvider>
      </div>
    </ConsolePreviewShell>
  );
}
