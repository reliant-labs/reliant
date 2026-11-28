// Copyright (c) 2026 Reliant Labs

/**
 * lib/changesFeature.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). The deployment flag says whether this
 * deployment's control plane runs Forge Changes. The demo's "control plane"
 * is its transport, and the shop pack answers every ChangesService read
 * (fixtures/shop/handlers/changes.ts), so the demo lists Experiments in its
 * nav whatever the site's runtime config says. The signed-in app keeps its
 * CHANGES_UI flag.
 */

import type * as Real from "./changesFeature";

export const changesUIEnabled: typeof Real.changesUIEnabled = () => true;
