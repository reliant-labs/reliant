// Copyright (c) 2025 Reliant Labs

import { orgOfPath, orgSwitchPath } from "@/lib/paths/org";
import { orgSegment, type RouteOrg } from "@/lib/routeOrg";
import { chooseActiveOrg } from "@/store/activeOrgStore";

interface RouterLike {
  state: { location: { pathname: string } };
  navigate: (options: { href: string }) => unknown;
}

/**
 * Make `org` the org this tab acts in (control-plane
 * docs/design/unified-shell-and-projects.md §3.5).
 *
 * On an org page (`/o/<org>/…`) the URL names the org, so the switch is a
 * navigation: the same page in `org`, minus what belonged to the old one
 * (lib/paths/org.ts, orgSwitchPath). The route then writes the tab's org
 * (lib/orgRouting.ts). On a page outside the org tree (Settings, until U8; the
 * mobile tree, until U15) the org is chosen in place, as before.
 *
 * Reads the router from the global registry (routes.tsx), like
 * store/projectStore: the switcher and the org services call it outside any
 * route component.
 */
export function switchTabOrg(org: RouteOrg): void {
  const router = (globalThis as { __RELIANT_ROUTER?: RouterLike }).__RELIANT_ROUTER;
  const pathname = router?.state.location.pathname ?? "";
  if (router && orgOfPath(pathname) !== null) {
    void router.navigate({ href: orgSwitchPath(pathname, orgSegment(org)) });
    return;
  }
  chooseActiveOrg(org.id);
}
