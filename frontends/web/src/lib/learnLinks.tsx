// Copyright (c) 2026 Reliant Labs

/**
 * Learn links (U6d): every section of the shell points at the guide that
 * explains it, on docs.reliantlabs.io. One table, so a section's page header,
 * its empty state and its nav row all name the same page, and a doc that
 * moves is one edit here.
 *
 * The slugs are a contract with the docs (reliant `docs/`, Mintlify): each
 * names a page in docs/docs.json. Pages marked NEW in the U6d briefing are
 * written there alongside this table.
 */

import { BookOpen } from "lucide-react";

import { cn } from "@/lib/utils";

export const DOCS_ORIGIN = "https://docs.reliantlabs.io";

/** Every section with a Learn link, and the docs page it opens. */
export const LEARN_SLUGS = {
  overview: "forge/overview",
  "agents/chats": "features/chats",
  workflows: "workflows/overview",
  sandboxes: "machines/overview",
  connectors: "integrations/connectors",
  "ai-providers": "settings/api-keys",
  services: "forge/services",
  data: "forge/data",
  storage: "forge/storage",
  auth: "forge/sign-in",
  dashboards: "forge/observability/dashboards-and-alerts",
  observability: "forge/observability",
  "speed-insights": "forge/observability/speed-insights",
  analytics: "forge/observability/product-analytics",
  security: "forge/security/vulnerabilities",
  releases: "forge/deploy/releases-and-promotion",
  environments: "forge/deploy/environments",
  secrets: "forge/secrets",
  domains: "forge/custom-domains",
  usage: "account/project-usage",
  "usage-billing": "account/billing",
  migrate: "forge/migrate-an-existing-app",
} as const;

export type LearnSection = keyof typeof LEARN_SLUGS;

/** The docs page for a section. */
export function learnUrl(section: LearnSection): string {
  return `${DOCS_ORIGIN}/${LEARN_SLUGS[section]}`;
}

/** What a section is called in a Learn link's accessible name ("Learn about Speed insights"). */
const SECTION_NAMES: Record<LearnSection, string> = {
  overview: "forge projects",
  "agents/chats": "chats",
  workflows: "workflows",
  sandboxes: "agent sandboxes",
  connectors: "connectors",
  "ai-providers": "AI providers",
  services: "services",
  data: "data",
  storage: "storage",
  auth: "sign-in",
  dashboards: "dashboards",
  observability: "observability",
  "speed-insights": "speed insights",
  analytics: "analytics",
  security: "security",
  releases: "releases",
  environments: "environments",
  secrets: "secrets",
  domains: "domains",
  usage: "usage",
  "usage-billing": "usage and billing",
  migrate: "migrating an app to forge",
};

/**
 * "Learn" beside a section's title: opens its guide in a new tab. `label`
 * replaces the word (an empty state's "Learn how to migrate").
 */
export function LearnLink({ section, label = "Learn", className }: { section: LearnSection; label?: string; className?: string }) {
  return (
    <a
      href={learnUrl(section)}
      target="_blank"
      rel="noopener noreferrer"
      aria-label={label === "Learn" ? `Learn about ${SECTION_NAMES[section]} (opens the docs)` : undefined}
      className={cn("no-color inline-flex shrink-0 items-center gap-1 text-meta text-ink-muted hover:text-ink hover:underline", className)}
      data-testid={`learn-link-${section}`}
    >
      <BookOpen className="size-3.5" aria-hidden="true" />
      {label}
    </a>
  );
}
