// Copyright (c) 2026 Reliant Labs

/**
 * C — HYBRID. Drill-in for the big areas (Agents with the chat list full
 * height, Observability, Settings); accordions for the small groups
 * (Workflows ▾ inside Agents, Product analytics ▾, Configure ▾).
 *
 *   Overview · Agents › · Services · Storage · Data · Dashboards · Observability › · Product analytics ▾ ·
 *   Releases · Configure ▾
 *   footer: Inbox · Usage & billing · Settings ›
 */

import {
  agentsArea,
  analyticsGroup,
  configureGroup,
  dashboards,
  data,
  footer,
  observabilityArea,
  overview,
  releases,
  services,
  storage,
} from "./items";
import type { NavConfig } from "./types";

export const navConfigC: NavConfig = {
  variant: "c",
  title: "Hybrid",
  items: [overview, agentsArea, services, storage, data, dashboards, observabilityArea, analyticsGroup, releases, configureGroup],
  footer,
};
