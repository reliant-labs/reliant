/**
 * UI-facing capability flags for the current deployment.
 *
 * Components import these to decide whether to render cloud-only UI
 * (e.g. "Start cloud daemon", "Use Reliant credits"). Today every flag is
 * derived from `hasControlPlane`; in future this file is the natural place
 * to read a `/capabilities` response from the backend so the frontend
 * reflects what's actually deployed instead of just what's configured.
 */

import { hasControlPlane } from "./config";

export const capabilities = {
  /** Hosted ("cloud_free_trial") workspace daemons are offered. */
  cloudWorkspaces: hasControlPlane,
  /** Reliant-managed model credits are offered. */
  managedCredits: hasControlPlane,
} as const;
