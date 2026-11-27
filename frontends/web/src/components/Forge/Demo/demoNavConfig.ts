// Copyright (c) 2026 Reliant Labs

/**
 * The public demo's nav: the app's own nav (Layout/sidebar/navConfig/hybrid.ts),
 * item for item and in its order, drawn from the SAME item objects, minus a
 * short named list of account-only items (DEMO_EXCLUDED_IDS), plus the demo's
 * own items (DEMO_OWN_ITEMS):
 *
 *   Home (the demo's) · Overview · Inbox · Agents › · Dashboards ▾ ·
 *   Observability › · Analytics ▾ · Deployments ▾ · Tests · Data · Storage ·
 *   Auth · Security ▾ · Configure ▾
 *   footer: the phone's actions (the demo's) · How it was built (the demo's)
 *
 * The app's footer (Usage & billing, Help, Settings) is the exclusion list:
 * nobody signs in to the demo, so there is no bill and no account to set, and
 * Help's menu reaches the app's Setup Guide (Onboarding/OnboardingChecklist)
 * and Settings › Keyboard shortcuts, neither of which the demo has. The pages
 * the sample shop does not serve (DEMO_HIDDEN_PAGES) are pruned as rows.
 *
 * demoNavConfig.test pins it: demoNavItems is hybridNav with exactly the
 * excluded items taken out and the demo's own put in, so an item the app adds,
 * moves or renames arrives in the demo too, or the test fails.
 *
 * Home is the story-led page a visitor lands on and "How it was built" the
 * recorded forge run (both `?section=` pages of /forge/demo, DemoConsole); on
 * a phone the drawer ends with the tour, Install Forge and the telemetry
 * notice (the `host-actions` slot, DemoNav). The live slots (the chat list,
 * New chat, the Inbox count, the Workflows pill) are the app's own, drawn by
 * the same code (Layout/ChatShellNav.tsx).
 *
 * It imports the items, never hybrid.ts: the app's config carries Settings'
 * sections (`sectionPages`), which must stay out of the demo bundle
 * (vite-plugin-demo-bundle.ts).
 */

import { createElement } from "react";
import { Hammer, Home } from "lucide-react";

import {
  agentsArea,
  analyticsGroup,
  auth,
  configureGroup,
  dashboards,
  data,
  deploymentsGroup,
  footer,
  inbox,
  observabilityArea,
  overview,
  securityGroup,
  slot,
  storage,
} from "@/components/Layout/sidebar/navConfig/items";
import { withoutPages } from "@/components/Layout/sidebar/navConfig/prune";
import { testsGroup } from "@/components/Layout/sidebar/navConfig/tests";
import type { NavConfig, NavCtx, NavItem, NavRow } from "@/components/Layout/sidebar/navConfig/types";

import { DEMO_HIDDEN_PAGES, DEMO_SECTION_BUILT, DEMO_SECTION_HOME, demoSectionHref, FORGE_DEMO_PATH, type DemoSection } from "./demoPath";

const sectionRow = (id: string, label: string, section: DemoSection, Icon: typeof Home): NavRow => ({
  kind: "row",
  id,
  label,
  icon: createElement(Icon, { className: "size-4", "aria-hidden": true }),
  href: () => demoSectionHref(section),
  match: (ctx: NavCtx) => ctx.pathname === FORGE_DEMO_PATH && ctx.search.section === section,
});

/** Home: the story-led page a visitor lands on (sections/DemoHome). */
export const demoHomeRow = sectionRow("demo-home", "Home", DEMO_SECTION_HOME, Home);
/** "How it was built": the recorded forge run that built the sample shop (howBuilt/). */
export const demoBuiltRow = sectionRow("demo-built", "How it was built", DEMO_SECTION_BUILT, Hammer);
/** On a phone, the demo's actions above the footer's rows (DemoNav fills it; elsewhere it draws nothing). */
export const demoActionsSlot = slot("host-actions");

/** The items that are the demo's own, not the app's. */
export const DEMO_OWN_ITEMS: readonly NavItem[] = [demoHomeRow, demoBuiltRow, demoActionsSlot];

/**
 * The app's items the demo leaves out, by id: account-only, every one.
 *  - usage-billing: the org's bill (Billing/); nobody pays for the demo.
 *  - slot-help: Help's menu revives the app's Setup Guide and opens Settings ›
 *    Keyboard shortcuts; the demo has its own tour in the page's action row.
 *  - settings: the signed-in account's settings.
 */
export const DEMO_EXCLUDED_IDS: readonly string[] = ["usage-billing", "slot-help", "settings"];

const excluded = new Set(DEMO_EXCLUDED_IDS);

/** The nav before the demo's pruning: hybridNav's items, the excluded ones out and the demo's own in. */
export const demoNavItems: NavConfig = {
  items: [
    demoHomeRow,
    overview,
    inbox,
    agentsArea,
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
  footer: [demoActionsSlot, ...footer.filter((item) => !excluded.has(item.id)), demoBuiltRow],
};

export const demoNav: NavConfig = withoutPages(demoNavItems, DEMO_HIDDEN_PAGES);
