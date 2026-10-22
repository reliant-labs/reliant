// Copyright (c) 2025 Reliant Labs

/**
 * Whether an integration can be connected on THIS deployment, from its
 * connection methods' status (ConnectionService.ListIntegrations, or a catalog
 * entry's `connection.methods`). One rule for every surface that asks, so the
 * Add-step palette, the step's connection picker and the connect dialog never
 * disagree about it.
 *
 * Unavailable means every way to connect is switched off here — an OAuth app
 * this deployment has no client for, a delegated authority it does not run —
 * so a Connect button could only open "nothing is set up here".
 */

import type { AuthMethod, Integration } from "../api/connection-grpc";

/** Every method that takes a credential is unavailable here (and there is at least one). */
export function methodsUnavailableHere(methods: readonly Pick<AuthMethod, "kind" | "available">[]): boolean {
  const connectable = methods.filter((method) => method.kind !== "none");
  return connectable.length > 0 && connectable.every((method) => !method.available);
}

/** The ids of the integrations nobody can connect on this deployment. */
export function unavailableIntegrationIds(integrations: readonly Integration[]): Set<string> {
  return new Set(integrations.filter((integration) => methodsUnavailableHere(integration.methods)).map((integration) => integration.id));
}
