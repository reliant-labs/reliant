/**
 * Shared config for the control-plane service modules.
 *
 * `hasControlPlane` is the ONLY place in the frontend that decides whether
 * cloud features are wired up. Each `services/controlPlane/<domain>/index.ts`
 * reads it once to pick a cloud or local implementation.
 *
 * Components, hooks, and tests must NOT import `hasControlPlane` directly.
 * They consume the chosen implementation through the service module's index.
 */

import { loadConfig } from "@/lib/config_gen";

// Module scope is right here: config.js is loaded before the bundle runs, and
// one page load reads one environment's values for its whole lifetime.
export const CONTROL_PLANE_API_URL = loadConfig().CONTROL_PLANE_API_URL;

export const hasControlPlane = Boolean(CONTROL_PLANE_API_URL);
