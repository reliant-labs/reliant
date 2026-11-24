// Copyright (c) 2025 Reliant Labs

/**
 * Named overlays over the shop pack: the states a dev preview needs that
 * shop's story does not have (an outage, an org that never set observability
 * up, an empty ledger, a read that never comes back). Each is a fixture pack
 * layer (../overlay.ts answering / failing / emptying / stalling), put over
 * the pack with `<FixturePreview pack={…} overlay={…}>` (../preview.tsx).
 * Keyed by the preview's `?case=` id, one module per console area.
 *
 * Harness policy, never the public demo's: the demo bundle imports
 * ./index.ts, never this file.
 */

export { browserOverlays } from "./overlays/browsers";
export { consoleOverlays } from "./overlays/console";
export { deliveryOverlays } from "./overlays/delivery";
