// Copyright (c) 2026 Reliant Labs

/**
 * THE NAV (U6e): the owner's hybrid, round 2 (u6e-hybrid-round2-brief).
 *
 *   Find · switcher (the shell draws them above)
 *   Overview · Inbox (count) · Agents › · GTM › (org-gated) ·
 *   Dashboards ▾ · Observability › · Analytics ▾ ·
 *   Deployments ▾ (Services, Releases, Environments, Previews) ·
 *   Data · Storage · Auth ·
 *   Security ▾ · Configure ▾ (Secrets, Domains, Usage, Activity)
 *   footer: Usage & billing · Help · Settings
 *
 * Agents › also lists the Inbox (under New chat), so a chat's drill-in reaches
 * it in one click; /inbox itself is the top level, its row there the one lit.
 *
 * Only Agents and Observability drill in (their rows replace the nav, under a
 * "‹ Area" header, on their routes). Everything else is an accordion or a
 * link. Settings is a plain link; on its pages its sections take the column.
 * The desktop nav, the narrow window's drawer and the phone's drawer all
 * draw this one config.
 */

import {
  agentsArea,
  analyticsGroup,
  auth,
  configureGroup,
  dashboards,
  data,
  deploymentsGroup,
  footer,
  gtmArea,
  inbox,
  observabilityArea,
  overview,
  securityGroup,
  storage,
} from "./items";
// The one importer of Settings' sections: they are the app's (settings.tsx).
import { settingsSections } from "./settings";
import { testsGroup } from "./tests";
import type { NavConfig } from "./types";

export const hybridNav: NavConfig = {
  items: [
    overview,
    inbox,
    agentsArea,
    gtmArea,
    dashboards,
    observabilityArea,
    analyticsGroup,
    deploymentsGroup,
    testsGroup,
    data,
    storage,
    auth,
    securityGroup,
    configureGroup,
  ],
  footer,
  sectionPages: [settingsSections],
};
