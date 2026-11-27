// Copyright (c) 2026 Reliant Labs

/**
 * SITE INSIGHTS (it was Speed insights): /forge/p/:app/site-insights[/:view],
 * how a site does for the people and the crawlers that visit it, one view a
 * tab:
 *
 *   speed           the default: web vitals per page for real visitors
 *                   (SpeedInsightsPage.tsx, as Speed insights was)
 *   seo             per-route head tags, social cards, robots and sitemap,
 *                   broken links (Site insights R4)
 *   accessibility   per-route score and issues (Site insights R5)
 *
 * The view is a path segment, so Back, refresh and a shared link keep it; the
 * old /speed-insights redirects to the Speed view. Every view reads the site
 * the URL names (`?site=`, siteInsightsParts.tsx), so a project with a static
 * site and an app frontend reads them apart.
 *
 * SEO and Accessibility say "coming soon" until their audit data ships:
 * SITE_AUDIT_VIEWS_READY is the flag their cards flip.
 */

import type { ReactNode } from "react";
import { Accessibility, SearchCheck } from "lucide-react";

import Card from "@/components/forge-ui/card";

import { useForgeConsole } from "../Console/consoleContext";
import { Page } from "../Console/consolePageFrame";
import type { ConsolePage, SiteInsightsView } from "../Console/paths";
import { SiteInsightsHeader } from "./siteInsightsParts";
import { SpeedInsightsView } from "./SpeedInsightsPage";

/** The audit views, beside Speed. */
export type SiteAuditView = Exclude<SiteInsightsView, "speed">;

/**
 * Whether each audit view has its page yet. False: its tab is there and says
 * what is coming. The SEO (R4) and Accessibility (R5) cards flip theirs when
 * the page they render ships.
 */
export const SITE_AUDIT_VIEWS_READY: Readonly<Record<SiteAuditView, boolean>> = { seo: false, accessibility: false };

/** The view a Site insights page shows: its path's, or Speed. */
export function siteInsightsViewOf(page: ConsolePage): SiteInsightsView {
  return page.kind === "site-insights" ? (page.view ?? "speed") : "speed";
}

export function SiteInsightsPage() {
  const { page } = useForgeConsole();
  const view = siteInsightsViewOf(page);
  return (
    <Page testId="console-site-insights">
      {view === "speed" ? <SpeedInsightsView /> : SITE_AUDIT_VIEWS_READY[view] ? null : <SiteAuditComingSoon view={view} />}
    </Page>
  );
}

const COMING: Record<SiteAuditView, { icon: ReactNode; title: string; body: string }> = {
  seo: {
    icon: <SearchCheck aria-hidden="true" />,
    title: "SEO checks are coming",
    body: "After every release, each page of your site will be checked the way a search engine and a link preview read it: its title and description, canonical URL, social card, robots.txt and sitemap, structured data and broken links, with a score per page and the fix for each finding.",
  },
  accessibility: {
    icon: <Accessibility aria-hidden="true" />,
    title: "Accessibility checks are coming",
    body: "After every release, each page of your site will be checked for accessibility issues: names and labels, contrast, ARIA, navigation, tables and lists, and more, with a score per page, how serious each issue is, and the element to fix.",
  },
};

/** An audit view before its data ships: the tab, and what it will show. */
export function SiteAuditComingSoon({ view }: { view: SiteAuditView }) {
  const copy = COMING[view];
  return (
    <>
      <SiteInsightsHeader view={view} />
      <Card className="flex max-w-[var(--form-max)] items-start gap-3" data-testid={`site-insights-${view}-coming-soon`}>
        <span className="mt-0.5 text-ink-muted [&>svg]:size-5">{copy.icon}</span>
        <div className="space-y-1">
          <h2 className="text-section text-ink">{copy.title}</h2>
          <p className="text-ui text-ink-muted">{copy.body}</p>
        </div>
      </Card>
    </>
  );
}
