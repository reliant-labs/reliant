// Copyright (c) 2025 Reliant Labs

/**
 * Proving harness for the Observability area. NOT a product surface.
 *
 * The real console's Observability views (ObservabilityViewPage, one view per
 * page), in the real console, on their real clients over the shop fixture
 * pack (ConsoleFixturePreview) — unauthenticated, no backend — so every view
 * and state can be looked at in every colour scheme, screenshotted, and
 * driven by the Playwright smoke (e2e/observability.spec.ts). Dev builds only:
 * the route sits behind DevOnlyRoute in routes.tsx. A case is a named overlay
 * over the pack (src/fixtures/shop/overlays.ts observabilityOverlays).
 *
 * The harness's URL says where the console opens: `view` picks the page, and
 * every other key is the view's own search, so a screenshot of any state is
 * a link: `?view=traces&groupBy=deployment.environment.name`,
 * `?view=errors&issue=iss-orders-deadline`, `&case=not-set-up`.
 */

import { useMemo } from "react";
import { Code } from "@connectrpc/connect";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";

import { DEMO_PROJECT } from "@/fixtures/shop/handlers/scope";
import { observabilityOverlays, retainedCatalog } from "@/fixtures/shop/overlays";
import type { FixturePack } from "@/fixtures/transport";

import { consolePath, isObsNavView, OBS_NAV_LABELS, type ObsNavView } from "../../Console/paths";
import { ConsoleFixturePreview, isDark, PreviewControls, usePageSearch, withPageSearch } from "../../__preview__/ConsoleFixturePreview";

const BASE = "/forge-observability-preview";

const o = observabilityOverlays;

type Overlay = FixturePack | readonly FixturePack[] | (() => FixturePack);

const CASES: { id: string; label: string; overlay?: Overlay }[] = [
  { id: "data", label: "telemetry flowing" },
  { id: "not-set-up", label: "org not set up (FailedPrecondition)", overlay: o.telemetry["not-set-up"] },
  { id: "nothing-yet", label: "set up, nothing arrived this week (getting started)", overlay: o.telemetry["nothing-yet"] },
  { id: "empty", label: "no data in range", overlay: o.telemetry.empty },
  { id: "graph-unknown", label: "graph: coverage/catalog unavailable", overlay: o.graph.unknown },
  { id: "graph-retained-denied", label: "graph: catalog revoked on refetch", overlay: () => retainedCatalog(Code.PermissionDenied) },
  { id: "graph-retained-unavailable", label: "graph: catalog unavailable on refetch", overlay: () => retainedCatalog(Code.Unavailable) },
  { id: "graph-unsupported", label: "graph: older unsupported server", overlay: o.graph.unsupported },
  { id: "window-empty", label: "no data in range, data this week", overlay: o.telemetry["window-empty"] },
  { id: "denied", label: "no permission", overlay: o.telemetry.denied },
  { id: "unavailable", label: "service unavailable", overlay: o.telemetry.unavailable },
  { id: "quota", label: "query quota hit", overlay: o.telemetry.quota },
  // Alerting states (view=alerts).
  { id: "alerting-not-configured", label: "alerting: not configured", overlay: o.alerting["not-configured"] },
  { id: "alerting-viewer", label: "alerting: member, not admin", overlay: o.alerting.viewer },
  { id: "alerting-quiet", label: "alerting: nothing firing", overlay: o.alerting.quiet },
  // Release markers: the ledger's answers. (DORA lives on the Releases page: /forge-releases-preview.)
  { id: "delivery-empty", label: "releases: nothing promoted", overlay: o.delivery.empty },
  { id: "delivery-not-member", label: "releases: not a member", overlay: o.delivery["not-member"] },
  { id: "delivery-env-denied", label: "releases: env from another org", overlay: o.delivery["env-denied"] },
  { id: "delivery-truncated", label: "releases: truncated (5)", overlay: o.delivery.truncated },
  // Errors (view=errors, &issue=<id> for one): the issue inbox's states.
  { id: "issues-empty", label: "errors: no issues", overlay: o.issues.empty },
  { id: "issues-not-configured", label: "errors: older control plane (Unimplemented)", overlay: o.issues["not-configured"] },
  { id: "issues-conflict", label: "errors: triage loses a race (Aborted)", overlay: o.issues.conflict },
  { id: "issues-assign-conflict", label: "errors: assignment loses a race (Aborted)", overlay: o.issues["assign-conflict"] },
  { id: "issues-merged", label: "errors: iss-inventory-lock merged into iss-orders-deadline", overlay: o.issues.merged },
  { id: "issues-roster-denied", label: "errors: member list closed (needs token:read)", overlay: o.issues["roster-denied"] },
  // Sessions (view=sessions, &session=<id> for one).
  { id: "sessions-empty", label: "sessions: none in range", overlay: o.sessions.empty },
  { id: "sessions-not-configured", label: "sessions: older control plane (Unimplemented)", overlay: o.sessions["not-configured"] },
  { id: "replay-partial", label: "replay: stops before the session did", overlay: o.replay.partial },
  { id: "replay-no-snapshot", label: "replay: full snapshot lost (unplayable)", overlay: o.replay["no-snapshot"] },
  { id: "replay-unreadable", label: "replay: two events unreadable", overlay: o.replay.unreadable },
  { id: "replay-none", label: "replay: recorded, nothing left", overlay: o.replay.none },
  // Release health (a comparison open: Compare from the release strip). The story judges prod's v1.8.3 a FAIL; these force the other answers.
  { id: "health-pass", label: "release health: PASS", overlay: o.health.pass },
  { id: "health-inconclusive", label: "release health: INCONCLUSIVE (window open)", overlay: o.health.inconclusive },
  { id: "health-not-evaluated", label: "release health: PASS, alerts and issues not checked", overlay: o.health["not-evaluated"] },
  { id: "health-unimplemented", label: "release health: older control plane (Unimplemented)", overlay: o.health.unimplemented },
];

/** The views the panel hosts (Metrics and Dashboards are console pages of their own: /forge-metrics-preview, /forge-dashboards-preview). */
const PANEL_VIEWS: ObsNavView[] = ["graph", "rpc", "errors", "sessions", "logs", "traces", "profiles", "alerts"];

const CONTROL = "rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink";

function CatalogRefetchProbe() {
  const client = useQueryClient();
  return (
    <button className="fixed left-3 bottom-3 z-50 text-sm text-primary-ink underline" onClick={() => void client.invalidateQueries({ predicate: (q) => q.queryKey.includes("graph-catalog") || q.queryKey.includes("graph-catalog-exact") })}>
      Preview: refetch retained catalog
    </button>
  );
}

export default function ObservabilityPreview() {
  const params = usePageSearch();
  const caseId = params.get("case") ?? "data";
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0];
  const requested = params.get("view");
  const view: ObsNavView = requested && isObsNavView(requested) ? requested : "errors";
  const path = withPageSearch(consolePath(DEMO_PROJECT, { kind: "obs-view", view }), ["view"], params.toString());
  return <ObservabilityPreviewCase key={`${active.id}|${path}`} active={active} view={view} path={path} scheme={params.get("scheme") ?? "professional-blue"} dark={isDark(params.get("dark"))} />;
}

function ObservabilityPreviewCase({ active, view, path, scheme, dark }: { active: (typeof CASES)[number]; view: ObsNavView; path: string; scheme: string; dark: boolean }) {
  const navigate = useNavigate();
  // A factory overlay counts per mount: one fresh pack per case.
  const overlay = useMemo(() => (typeof active.overlay === "function" ? active.overlay() : active.overlay), [active]);
  const retained = active.id.startsWith("graph-retained-");
  return (
    <ConsoleFixturePreview
      path={path}
      overlay={overlay}
      wrap={(console) => (
        <>
          {console}
          {retained && <CatalogRefetchProbe />}
        </>
      )}
      controls={
        <PreviewControls base={BASE} cases={CASES} caseId={active.id} defaultCase="data" scheme={scheme} dark={dark}>
          <select data-testid="preview-view" value={view} onChange={(e) => void navigate({ to: BASE, search: { view: e.target.value }, replace: true } as never)} className={CONTROL}>
            {PANEL_VIEWS.map((id) => (
              <option key={id} value={id}>
                {OBS_NAV_LABELS[id]}
              </option>
            ))}
          </select>
        </PreviewControls>
      }
    />
  );
}
