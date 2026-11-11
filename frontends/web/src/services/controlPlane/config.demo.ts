// Copyright (c) 2025 Reliant Labs

/**
 * services/controlPlane/config.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). The demo's runtime config names no control
 * plane, yet every control-plane read must still run: the demo's transport
 * (lib/connect.demo.ts) answers it with sample data, whatever the URL. So the
 * cloud features are on, and the "control plane" is the site's own origin:
 * services use the URL only to label a store or as a "configured" flag, and
 * the demo's transport never dials it.
 */

import type * as Real from "./config";

export const CONTROL_PLANE_API_URL: typeof Real.CONTROL_PLANE_API_URL = globalThis.location?.origin ?? "https://demo.invalid";

export const hasControlPlane: typeof Real.hasControlPlane = true;
