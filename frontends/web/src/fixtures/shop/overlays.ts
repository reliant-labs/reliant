// Copyright (c) 2025 Reliant Labs

/**
 * The app pack's NAMED overlays: variants of the shop world a surface asks
 * for by name, because a name is all a URL (`?fixtures=no-daemons`) or a
 * harness init script can carry. Each is a pack layered on top of the app
 * pack (shopAppPack), so it overrides only the keys it answers.
 *
 * Only these names are honoured: install.ts allow-lists `?fixtures=` and
 * `window.__forgeFixtures.overlays` against this table and ignores the rest.
 * The screenshot harness's overlays (phase 2 plan §1.3) join this table.
 */
import { DaemonRegistryService } from "@/gen/reliant/v1/daemon_registry_pb";

import { defineFixturePack, type FixturePack } from "../transport";
import { serve } from "./handlers/serve";

/** An overlay, built when it is selected (after the pack clock is pinned). */
export type ShopOverlay = () => FixturePack;

export const SHOP_OVERLAYS: Readonly<Record<string, ShopOverlay>> = {
  /** Alex has no machine: the daemonless home and the no-sandbox agents page. */
  "no-daemons": () => defineFixturePack({ name: "no-daemons", scenario: serve(DaemonRegistryService, { listDaemons: () => ({ daemons: [] }) }) }),
};

/**
 * Console preview overlays, keyed by the preview's `?case=` id and layered
 * with FixturePreview. Kept separate from the URL-selectable app overlays.
 */
export { browserOverlays } from "./overlays/browsers";
export { consoleOverlays } from "./overlays/console";
export { deliveryOverlays } from "./overlays/delivery";
export { observabilityOverlays, readCap, retainedCatalog } from "./overlays/observability";
