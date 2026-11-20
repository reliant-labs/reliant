// Copyright (c) 2026 Reliant Labs

/**
 * A — ACCORDION EVERYTHING. One nav; groups expand in place like Workflows ▾.
 * Only Settings drills in (every variant's rule).
 *
 *   Overview · Agents ▾ (New chat, Workflows ▾, Sandboxes, AI providers ↗, Chats) · Services · Storage ·
 *   Dashboards · Observability ▾ · Performance & issues ▾ · Product analytics ▾ · Releases · Data · Configure ▾
 *   footer: Inbox · Usage & billing · Settings ›
 */

import {
  agentsGroup,
  analyticsGroup,
  configureGroup,
  dashboards,
  data,
  footer,
  observabilityGroup,
  overview,
  performanceGroup,
  releases,
  services,
  storage,
} from "./items";
import type { NavConfig } from "./types";

export const navConfigA: NavConfig = {
  variant: "a",
  title: "Accordion everything",
  items: [
    overview,
    agentsGroup,
    services,
    storage,
    dashboards,
    observabilityGroup,
    performanceGroup,
    analyticsGroup,
    releases,
    data,
    configureGroup,
  ],
  footer,
};
