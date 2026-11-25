// Copyright (c) 2026 Reliant Labs

/**
 * The public demo's nav: the app's own nav (Layout/sidebar/navConfig/hybrid.ts)
 * drawn from the SAME item objects, so the demo's information architecture is
 * the signed-in app's, row for row, in its order. demoNav.test pins that: with
 * the demo's own rows taken out, demoNav is hybridNav with rows removed, never
 * reordered or relabelled.
 *
 * What the demo leaves out is the app's own: the Inbox, Agents (chats,
 * workflows, sandboxes), and the footer's Usage & billing, Help and Settings.
 * What it adds is its own: Home above Overview and "How it was built" in the
 * footer (`?section=` pages of /forge/demo, DemoConsole), and on a phone the
 * tour, Install Forge and the telemetry notice (the `host-actions` slot,
 * DemoNav). The pages the sample shop does not serve (DEMO_HIDDEN_PAGES) have
 * no row.
 *
 * It imports the items, never hybrid.ts: the app's config carries Settings'
 * sections, which must stay out of the demo bundle (vite-plugin-demo-bundle.ts).
 */

import { createElement } from "react";
import { Hammer, Home } from "lucide-react";

import {
  analyticsGroup,
  auth,
  configureGroup,
  dashboards,
  data,
  deploymentsGroup,
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

/** The items that are the demo's own, not the app's: what demoNav ⊂ hybridNav leaves out. */
export const DEMO_OWN_ITEMS: readonly NavItem[] = [demoHomeRow, demoBuiltRow, demoActionsSlot];

export const demoNav: NavConfig = withoutPages(
  {
    items: [
      demoHomeRow,
      overview,
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
    footer: [demoActionsSlot, demoBuiltRow],
  },
  DEMO_HIDDEN_PAGES,
);
