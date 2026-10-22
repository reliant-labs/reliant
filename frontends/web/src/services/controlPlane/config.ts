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

export const CONTROL_PLANE_API_URL =
  import.meta.env.VITE_CONTROL_PLANE_API_URL || "";

export const hasControlPlane = Boolean(CONTROL_PLANE_API_URL);
