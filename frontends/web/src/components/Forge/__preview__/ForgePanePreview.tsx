// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY: the real forge console — the same route tree (forgeConsoleRoutes),
 * layout, nav and pages the product mounts — over the shop fixture pack's
 * app layer (src/fixtures/shop/app.ts, <FixturePreview>), with no login.
 *
 * WHY THE FAKE IS THE TRANSPORT, NOT THE QUERY CACHE. Seeding React Query
 * would bypass the code most worth looking at: the clients, the
 * classification of forge's answers, the routing of each read to the backend
 * or the daemon. So every hook, service and component runs as it does in the
 * product, and only the Connect transport under them answers from the pack:
 * the same world (app `shop`, prod on v1.8.3 and failing its health gate) the
 * public demo, the screenshot harness and the other dev previews show.
 *
 * A scenario is a named overlay over the pack (src/fixtures/shop/overlays.ts
 * consoleOverlays): `unregistered` has no rows in the control plane;
 * `daemon-offline` fails every daemon read; `queued` and `queued-member`
 * put prod's newest deploy in the queue on billing, as the viewer who can set
 * it up and as one who cannot.
 *
 *   /forge-pane-preview?scenario=registered|unregistered|daemon-offline|queued|queued-member&path=/forge/p/shop/environments/prod
 *
 * `path` is any console URL (or a retired one, to watch it redirect).
 */

import { consoleOverlays } from "@/fixtures/shop/overlays";

import { ConsoleFixturePreview } from "./ConsoleFixturePreview";

export type Scenario = "registered" | keyof typeof consoleOverlays;

export function ForgePanePreview() {
  // Keyed on the query string: the app router treats a change of `?scenario`
  // as the same route, so without a remount the console would carry the
  // previous case's state.
  return <ForgePanePreviewCase key={window.location.search} />;
}

function ForgePanePreviewCase() {
  const params = new URLSearchParams(window.location.search);
  const scenario = (params.get("scenario") as Scenario | null) ?? "registered";
  const path = params.get("path") || "/forge/p/shop";
  return <ConsoleFixturePreview path={path} overlay={scenario === "registered" ? null : consoleOverlays[scenario]} />;
}
