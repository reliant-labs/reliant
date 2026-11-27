// Copyright (c) 2026 Reliant Labs

/**
 * Every GTM page's frame: the gate, the area chrome, and the org the page
 * reads in.
 *
 * The page asks the gate again (useGtmAccess) rather than trusting the nav:
 * a typed or bookmarked /gtm URL reaches here with no nav in between. Only a
 * yes renders the page. While the answer loads nothing is drawn, and a no, an
 * error or a tab with no org gets what any unknown route gets
 * (GtmNotFound), so the page does not reveal that GTM exists.
 *
 * Inside, `useGtmOrgId()` is the org the gate said yes for: every GtmService
 * request a page makes names it (`org_id` is required), so a page never asks
 * in an org it was not let into. Each read still says so in its own
 * `enabled` (`!!orgId && …`): that is what the org-scoped read guard
 * (src/__tests__/orgScopedQueries.guard.test.ts) checks for.
 */

import { Navigate } from "@tanstack/react-router";
import { createContext, useContext, type ReactNode } from "react";

import { gtmPath, homePath, type GtmPage } from "@/lib/paths";

import { AreaShell } from "../../Layout/AreaShell";
import { useGtmAccess } from "./access";

export const GTM_PAGE_LABELS: Record<GtmPage, string> = {
  strategy: "Strategy",
  ideas: "Ideas",
  campaigns: "Campaigns",
  calendar: "Calendar",
  channels: "Channels",
  positioning: "Positioning & facts",
};

const GtmOrgContext = createContext<string | null>(null);

/** The org a GTM subtree reads in: GtmShell's, or a test's. */
export const GtmOrgProvider = GtmOrgContext.Provider;

/** The org this GTM page reads and writes in. Only inside GtmShell, which renders pages only once the gate said yes. */
export function useGtmOrgId(): string {
  const orgId = useContext(GtmOrgContext);
  if (!orgId) throw new Error("useGtmOrgId: outside GtmShell");
  return orgId;
}

/**
 * The app's not-found page, exactly as routes.tsx's root `notFoundComponent`
 * draws it (the home), so a refused /gtm URL is indistinguishable from a
 * mistyped one.
 */
export function GtmNotFound() {
  return <Navigate to={homePath()} search={{}} />;
}

export function GtmShell({ page, children }: { page: GtmPage; children: ReactNode }) {
  const access = useGtmAccess();
  if (access.loading) return null;
  if (!access.enabled || !access.orgId) return <GtmNotFound />;
  return (
    <GtmOrgProvider value={access.orgId}>
      <AreaShell areaPath={gtmPath(page)} areaLabel={GTM_PAGE_LABELS[page]} areaNoun="GTM" wide>
        <div className="space-y-6" data-testid={`gtm-${page}`}>
          {children}
        </div>
      </AreaShell>
    </GtmOrgProvider>
  );
}
