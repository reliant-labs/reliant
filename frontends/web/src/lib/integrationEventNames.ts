// Copyright (c) 2025 Reliant Labs

/**
 * Integration events in words, from the catalog. A run started by an
 * integration event records the integration id and the provider's event type
 * ("github", "issues.opened"), and a declared trigger names them the same way;
 * read raw, a run header said "on github: issues.opened". The catalog's
 * trigger types carry both display names — the integration's ("GitHub") and
 * the trigger's ("Issue opened") — and the provider events each fires on, so
 * one trigger search names every event a run or trigger can mention.
 *
 * Anything the catalog does not know (an integration since removed, a
 * wildcard like "issues.*") reads as recorded rather than guessed.
 */

import type { CatalogEntrySummary } from "../api/catalog-search-grpc";

export interface IntegrationEventNaming {
  /** "github" → "GitHub". */
  integration: (integrationId: string) => string;
  /** ("github", "issues.opened") → "Issue opened". */
  event: (integrationId: string, eventType: string) => string;
}

/** Names exactly what was recorded: the default before the catalog loads. */
export const RAW_EVENT_NAMING: IntegrationEventNaming = {
  integration: (integrationId) => integrationId,
  event: (_integrationId, eventType) => eventType,
};

/** The naming the catalog's trigger types give. */
export function integrationEventNaming(triggers: readonly Pick<CatalogEntrySummary, "displayName" | "events" | "integration">[]): IntegrationEventNaming {
  const integrations = new Map<string, string>();
  const events = new Map<string, string>();
  for (const trigger of triggers) {
    const integrationId = trigger.integration.id;
    if (trigger.integration.displayName) integrations.set(integrationId, trigger.integration.displayName);
    for (const eventType of trigger.events) {
      // The first trigger to claim an event names it; manifests give each
      // event to one trigger type.
      const key = `${integrationId}\u0000${eventType}`;
      if (!events.has(key)) events.set(key, trigger.displayName);
    }
  }
  return {
    integration: (integrationId) => integrations.get(integrationId) ?? integrationId,
    event: (integrationId, eventType) => events.get(`${integrationId}\u0000${eventType}`) ?? eventType,
  };
}

/**
 * An integration source in words: "GitHub: Issue opened, Issue closed +1".
 * Shared by declared triggers (the canvas rail, the detail page, inactive
 * triggers) and stored automations, so a trigger reads the same everywhere.
 */
export function describeIntegrationEvents(
  source: { integration: string; events: readonly string[] },
  naming: IntegrationEventNaming = RAW_EVENT_NAMING,
): string {
  const names = source.events.map((eventType) => naming.event(source.integration, eventType));
  const events = names.length > 2 ? `${names.slice(0, 2).join(", ")} +${names.length - 2}` : names.join(", ");
  return `${(source.integration && naming.integration(source.integration)) || "Integration"}${events ? `: ${events}` : ""}`;
}
