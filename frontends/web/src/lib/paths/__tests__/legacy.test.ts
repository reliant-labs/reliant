// Copyright (c) 2025 Reliant Labs

/**
 * The org-less paths that forward into the tab's org with the flag
 * `web.org_routes` on (control-plane docs/design/unified-shell-and-projects.md
 * §3.4, §12): one row per legacy path, with where it lands in the org `acme`,
 * or that it stays where it is and why.
 */

import { describe, expect, it } from "vitest";

import { legacyOrgForward } from "../legacy";
import { inOrg } from "../org";

const FORWARDED: [path: string, landsAt: string][] = [
  ["/", "/o/acme"],
  ["/inbox", "/o/acme/inbox"],
  ["/inbox/", "/o/acme/inbox"],
  ["/apps", "/o/acme/apps"],
  ["/workflows", "/o/acme/workflows"],
  ["/workflows/library", "/o/acme/workflows/library"],
  ["/workflows/library/builtin%3A%2F%2Fget-it-right", "/o/acme/workflows/library/builtin%3A%2F%2Fget-it-right"],
  ["/workflows/runs", "/o/acme/workflows/runs"],
  ["/workflows/automations", "/o/acme/workflows/automations"],
  ["/workflow/new", "/o/acme/workflow/new"],
  ["/forge/observability", "/o/acme/observability"],
  ["/forge/observability/logs", "/o/acme/observability/logs"],
];

const STAYS: [path: string, why: string, search?: Record<string, unknown>][] = [
  ["/sites", "the forge host's page: this host does not serve it, so forwarding would land nowhere (R5)"],
  ["/usage", "the forge host's page (R5)"],
  ["/workflows/library/fix-my-pr", "a saved workflow by name: its own org decides (U4), never the tab's (R7)"],
  ["/workflows/library/builtin-lookalike", "a saved workflow whose name only looks built-in (R7)"],
  ["/workflow/fix-my-pr", "the builder by name: a saved workflow's, or a built-in's bare name, indistinguishable (R7)"],
  ["/project/p-1", "a project by id: the resolver (U4) finds its org"],
  ["/workflows/runs/c-1", "a run by id: the resolver (U4)"],
  ["/workflows/automations/t-1", "an automation by id: the resolver (U4)"],
  ["/forge/p/shop", "an app by name: ResolveAppOrgs (U4)"],
  ["/forge/p/shop/releases", "an app by name: ResolveAppOrgs (U4)"],
  ["/forge", "the console's entry: it redirects to the Apps list (an org path) or resolves ?project= (U4)"],
  ["/forge/env/prod", "a retired console path naming an app: U4"],
  ["/forge/observability", "an app in the query: U4", { project: "shop" }],
  ["/forge/observability/logs", "an app in the query: U4", { forgeProject: "shop" }],
  ["/settings", "Settings: the split is U8"],
  ["/settings/machines/d-1", "machines: U8"],
  ["/onboarding", "setup: R11"],
  ["/m/chats", "the mobile tree: U15"],
  ["/m/chats/c-1", "the mobile tree: U15"],
  ["/workflow", "retired: redirects to a current path first"],
  ["/runs/c-1", "retired: redirects to a current path first"],
  ["/automations", "retired: redirects to a current path first"],
  ["/auth", "never org-scoped"],
  ["/oauth/cli", "never org-scoped"],
  ["/claim", "never org-scoped"],
  ["/accept-invite", "never org-scoped"],
  ["/o/acme/inbox", "already in an org"],
];

describe("legacyOrgForward", () => {
  it.each(FORWARDED)("%s forwards to %s", (path, landsAt) => {
    const forwarded = legacyOrgForward(path);
    expect(forwarded).not.toBeNull();
    expect(inOrg("acme", forwarded!)).toBe(landsAt);
  });

  it.each(STAYS)("%s stays (%s)", (path, _why, search) => {
    expect(legacyOrgForward(path, search)).toBeNull();
  });
});
