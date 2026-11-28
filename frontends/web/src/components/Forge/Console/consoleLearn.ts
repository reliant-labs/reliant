// Copyright (c) 2026 Reliant Labs

/**
 * Which guide each console page's Learn link opens (lib/learnLinks.tsx). Keyed
 * by the page's kind as a string, so a kind added later (Security, Auth)
 * resolves as soon as it is named here, and an unknown one simply has no link.
 */

import type { LearnSection } from "@/lib/learnLinks";

import type { ConsolePage } from "./paths";

const BY_KIND: Partial<Record<string, LearnSection>> = {
  overview: "overview",
  workload: "services",
  services: "services",
  service: "services",
  releases: "releases",
  release: "releases",
  activity: "releases",
  "obs-view": "observability",
  dashboard: "dashboards",
  dashboards: "dashboards",
  storage: "storage",
  data: "data",
  users: "auth",
  auth: "auth",
  security: "security",
  tests: "tests",
  "tests-coverage": "tests",
  "tests-runs": "tests",
  "tests-run": "tests",
  "tests-flaky": "tests",
  "tests-flaky-test": "tests",
  environments: "environments",
  environment: "environments",
  secrets: "secrets",
  domains: "domains",
  usage: "usage",
  events: "analytics",
  analytics: "analytics",
  "site-insights": "site-insights",
};

/** The Learn section of a console page, or undefined when it has none. */
export function learnSectionOf(page: ConsolePage): LearnSection | undefined {
  if (page.kind === "obs-view" && page.view === "dashboards") return "dashboards";
  return BY_KIND[page.kind];
}
