// Copyright (c) 2026 Reliant Labs

/**
 * THE NAV (U6d): the owner's hybrid, settled 2026-11-20 (u6d-hybrid-nav-brief).
 *
 *   Find · switcher (the shell draws them above)
 *   Overview · Inbox (count) · Agents › · Services · Data · Storage · Auth ·
 *   Dashboards ▾ · Observability › · Analytics ▾ · Security ▾ · Releases · Configure ▾
 *   footer: Usage & billing · Settings
 *
 * Only Agents and Observability drill in (their rows replace the nav, under a
 * "‹ Area" header, on their routes). Everything else is an accordion or a
 * link. Settings is a plain link; on its pages its sections take the column.
 */

import {
  agentsArea,
  analyticsGroup,
  auth,
  configureGroup,
  dashboards,
  data,
  footer,
  inbox,
  observabilityArea,
  overview,
  releases,
  securityGroup,
  services,
  settingsSections,
  storage,
} from "./items";
import type { NavConfig } from "./types";

export const hybridNav: NavConfig = {
  items: [
    overview,
    inbox,
    agentsArea,
    services,
    data,
    storage,
    auth,
    dashboards,
    observabilityArea,
    analyticsGroup,
    securityGroup,
    releases,
    configureGroup,
  ],
  footer,
  sectionPages: [settingsSections],
};
