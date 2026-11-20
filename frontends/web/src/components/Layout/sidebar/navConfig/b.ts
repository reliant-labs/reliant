// Copyright (c) 2026 Reliant Labs

/**
 * B — VERCEL DRILL-IN (recommended, the default). A short top level; the big
 * areas drill in and REPLACE the nav under a "‹ Area" header, on any of their
 * routes (so Back returns to the top level).
 *
 *   Overview · Agents › · Services · Storage · Data · Dashboards · Observability › · Analytics › · Releases ·
 *   Configure: Environments · Secrets · Domains · Sign-in users · Usage · Activity
 *   footer: Inbox · Usage & billing · Settings ›
 */

import {
  activity,
  agentsArea,
  analyticsArea,
  dashboards,
  data,
  domains,
  environments,
  footer,
  heading,
  observabilityArea,
  overview,
  projectUsage,
  releases,
  secrets,
  services,
  signInUsers,
  storage,
} from "./items";
import type { NavConfig } from "./types";

export const navConfigB: NavConfig = {
  variant: "b",
  title: "Drill-in (Vercel)",
  items: [
    overview,
    agentsArea,
    services,
    storage,
    data,
    dashboards,
    observabilityArea,
    analyticsArea,
    releases,
    { ...heading("configure-heading", "Configure"), available: (ctx) => !!ctx.app },
    environments,
    secrets,
    domains,
    signInUsers,
    projectUsage,
    activity,
  ],
  footer,
};
