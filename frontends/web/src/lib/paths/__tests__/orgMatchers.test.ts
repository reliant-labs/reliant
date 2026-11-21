// Copyright (c) 2025 Reliant Labs

/**
 * Every module that MATCHES a pathname against the app's routes reads an org
 * page (`/o/<org>/…`, control-plane docs/design/unified-shell-and-projects.md
 * §3) as the org-less page it is (lib/paths/org.ts, orgFreePath): the four the
 * nav-paths guard allowlists as matchers (workflowsArea, mobileRedirect,
 * monacoPreload, onboardingGate) and the other pathname readers U3 taught.
 */

import { describe, expect, it } from "vitest";

import { consolePageOf, orgPageOf } from "@/components/Forge/Console/paths";
import { isForgePath } from "@/components/Forge/QueuedDeployWatch";
import { projectIdOfPath } from "@/components/Layout/ProjectFilterRouteSync";
import { isOnboardingReturnTo, routeRequiresOnboarding } from "@/components/OnboardingFlow/onboardingGate";
import { shortcutScope } from "@/components/Shortcuts/ShortcutHost";
import { mobileHandoffAllowed, mobilePathFor, shouldRedirectToMobile } from "@/lib/mobileRedirect";
import { shouldPreloadMonaco } from "@/lib/monacoPreload";
import { getParentRouteNavigateOptions } from "@/lib/routeParent";
import { isWorkflowBuilderPath, isWorkflowsAreaPath, isWorkflowsDetailPath, workflowsTabForPath } from "@/lib/workflowsArea";

const phone = (pathname: string) => shouldRedirectToMobile({ pathname, search: "", width: 390, coarsePointer: true, optedOutOfMobile: false });

describe("pathname matchers, in the org form", () => {
  it("workflowsArea: the tab, the detail page, the area and the builder", () => {
    expect(workflowsTabForPath("/o/acme/workflows/runs/c-1")).toBe("runs");
    expect(workflowsTabForPath("/o/acme/workflows/library")).toBe("workflows");
    expect(isWorkflowsDetailPath("/o/acme/workflows/runs/c-1")).toBe(true);
    expect(isWorkflowsDetailPath("/o/acme/workflows/runs")).toBe(false);
    expect(isWorkflowsAreaPath("/o/acme/workflows")).toBe(true);
    expect(isWorkflowBuilderPath("/o/acme/workflow/fix-my-pr")).toBe(true);
    expect(isWorkflowsAreaPath("/o/acme/inbox")).toBe(false);
  });

  it("mobileRedirect: an org page on a phone opens its mobile twin; the console stays", () => {
    expect(phone("/o/acme/inbox")).toBe(true);
    expect(phone("/o/acme/apps")).toBe(false);
    expect(phone("/o/acme/apps/shop/releases")).toBe(false);
    expect(phone("/o/acme/observability/logs")).toBe(false);
    expect(mobilePathFor("/o/acme/inbox")).toBe("/m/inbox");
    expect(mobilePathFor("/o/acme/workflows/runs/c-1")).toBe("/m/chats/c-1");
    expect(mobilePathFor("/o/acme/workflows/runs")).toBe("/m/runs");
    expect(mobilePathFor("/o/acme")).toBe("/m/chats");
  });

  it("monacoPreload: an org page preloads like the app page it is", () => {
    expect(shouldPreloadMonaco("/o/acme")).toBe(true);
    expect(shouldPreloadMonaco("/o/acme/workflow/fix-my-pr")).toBe(true);
  });

  it("onboardingGate: exemptions and the return path, in either form", () => {
    expect(routeRequiresOnboarding("/o/acme/apps")).toBe(false);
    expect(routeRequiresOnboarding("/o/acme/inbox")).toBe(true);
    expect(isOnboardingReturnTo("/o/acme")).toBe(false);
    expect(isOnboardingReturnTo("/o/acme/inbox?x=1")).toBe(true);
  });

  it("routeParent: an org page's parent is its org-less parent's", () => {
    expect(getParentRouteNavigateOptions("/o/acme/workflows/runs/c-1")).toEqual({ to: "/workflows/runs" });
    expect(getParentRouteNavigateOptions("/o/acme/workflow/new")).toEqual({ to: "/workflows/library" });
    expect(getParentRouteNavigateOptions("/o/acme/apps/shop")).toEqual({ to: "/", search: {} });
  });

  it("the console: which page, which app", () => {
    expect(consolePageOf("/o/acme/apps/shop/releases")).toEqual({ project: "shop", page: { kind: "releases" } });
    expect(consolePageOf("/o/acme/apps")).toBeNull();
    expect(orgPageOf("/o/acme/observability/logs")).toEqual({ kind: "obs-view", view: "logs" });
    expect(orgPageOf("/o/acme/observability")).toEqual({ kind: "observability" });
    expect(isForgePath("/o/acme/apps/shop")).toBe(true);
    expect(isForgePath("/o/acme/inbox")).toBe(false);
  });

  it("the chat shell: the project filter, and the workspace's shortcuts", () => {
    expect(projectIdOfPath("/o/acme/project/p-1")).toBe("p-1");
    expect(projectIdOfPath("/o/acme")).toBeUndefined();
    expect(shortcutScope({ routeIds: ["__root__", "/o/$org", "/o/$org/_app", "/o/$org/_app/"], pathname: "/o/acme", signedIn: true })).toEqual({
      enabled: true,
      workspace: true,
    });
    expect(
      shortcutScope({ routeIds: ["__root__", "/o/$org", "/o/$org/_authenticated", "/o/$org/_authenticated/inbox"], pathname: "/o/acme/inbox", signedIn: true }),
    ).toEqual({ enabled: true, workspace: false });
  });

  it("mobileRedirect: an org page hands off to the phone only once a member org's page has committed", () => {
    const member = { kind: "member" as const, org: { id: "org-a", slug: "acme" } };
    expect(mobileHandoffAllowed({ pathname: "/inbox", committedPathname: null, routeOrgScope: { kind: "legacy" } })).toBe(true);
    expect(mobileHandoffAllowed({ pathname: "/o/acme/inbox", committedPathname: "/o/acme/inbox", routeOrgScope: member })).toBe(true);
    // Still loading, someone else's, another org's commit on screen, or not committed yet.
    expect(mobileHandoffAllowed({ pathname: "/o/acme/inbox", committedPathname: null, routeOrgScope: { kind: "pending" } })).toBe(false);
    expect(mobileHandoffAllowed({ pathname: "/o/acme/inbox", committedPathname: "/o/acme/inbox", routeOrgScope: { kind: "denied", reason: "not-member" } })).toBe(false);
    expect(mobileHandoffAllowed({ pathname: "/o/side/inbox", committedPathname: "/o/acme/inbox", routeOrgScope: member })).toBe(false);
    expect(mobileHandoffAllowed({ pathname: "/o/acme/inbox", committedPathname: "/settings", routeOrgScope: member })).toBe(false);
  });
});
