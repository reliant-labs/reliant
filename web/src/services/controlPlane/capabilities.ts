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
  /** Hosted ("cloud_free_trial") cloud daemons are offered. */
  cloudDaemons: hasControlPlane,
  /** Reliant-managed model credits are offered. */
  managedCredits: hasControlPlane,
  /** Cloud-managed git credential storage & repo cloning. */
  gitConnections: hasControlPlane,
  /**
   * Reliant can charge for something in this deployment.
   *
   * True exactly when there is a product of ours to sell — hosted machines or
   * Reliant's models — which today means a control plane is configured. It is
   * the deployment half of "only bill when Reliant AI or Reliant compute is
   * chosen": the plan half asks WHAT the user picked, this asks whether we are
   * the one billing for it.
   *
   * Without it, `requiresPayment` had no way to know that the billing RPCs it
   * routes on cannot answer here. Its facts read pessimistic by design
   * (unknown ⇒ "you owe"), so a build with no control plane derived the user
   * to a checkout step that could not mint a Stripe session and could not list
   * a plan — a dead end whose only visible symptom was "No plans are available
   * in this setup".
   *
   * Deliberately a BUILD CONSTANT rather than a server read: it cannot flap,
   * so suppressing the bill on it introduces no race, unlike the entitlement
   * facts beside it which must stay pessimistic while in flight.
   */
  billing: hasControlPlane,
} as const;