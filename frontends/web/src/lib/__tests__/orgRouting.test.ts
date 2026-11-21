// Copyright (c) 2025 Reliant Labs

/**
 * Which org a URL opens in (control-plane docs/design/unified-shell-and-projects.md
 * §3.2–§3.5): the `/o/$org` layout's resolution and the org-less paths'
 * forwarding, against a fake session and membership list.
 */

import { isRedirect } from "@tanstack/react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { resetShellForTests } from "@/lib/shell";
import type { MyOrg, MyOrgs } from "@/services/controlPlane/orgs";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { setRuntimeConfig } from "@/test/runtimeConfig";

import { entryOrgSegment, forwardIntoOrg, resolveOrgRoute, type OrgRoutingDeps } from "../orgRouting";

const org = (id: string, slug: string, over: Partial<MyOrg> = {}): MyOrg => ({
  id, name: slug.toUpperCase(), slug, role: "owner", isOwner: true, isDefault: false, ...over,
});
const ACME = org("org-a", "acme", { isDefault: true });
const SIDE = org("org-b", "side");
const MINE: MyOrgs = { orgs: [ACME, SIDE], defaultOrgId: "org-a" };

function deps(over: Partial<OrgRoutingDeps> = {}): OrgRoutingDeps {
  return {
    signedInUser: async () => "alice",
    hasOrgs: () => true,
    listOrgs: async () => MINE,
    ...over,
  };
}

const at = (pathname: string, searchStr = "", hash = "") => ({ pathname, searchStr, hash, search: Object.fromEntries(new URLSearchParams(searchStr)) });

/** The href a redirect thrown by `run` names. */
async function redirectOf(run: () => unknown): Promise<string> {
  try {
    await run();
  } catch (thrown) {
    if (isRedirect(thrown)) return (thrown.options as { href: string }).href;
    throw thrown;
  }
  throw new Error("no redirect");
}

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null, segments: {} });
  resetShellForTests("reliant");
});

afterEach(() => resetShellForTests());

describe("resolveOrgRoute", () => {
  it("resolves a member's slug, and writes nothing the page reads (the commit does)", async () => {
    const { orgRoute } = await resolveOrgRoute("side", at("/o/side/inbox"), { deps: deps() });
    expect(orgRoute).toEqual({ kind: "member", org: { id: "org-b", slug: "side" } });
    // A load can be superseded and never commit: the tab's org is not moved here (lib/routeOrg.ts).
    expect(useActiveOrgStore.getState().chosenId).toBeNull();
    expect(sessionStorage.getItem("reliant.activeOrg.alice")).toBeNull();
    expect(useActiveOrgStore.getState().segments).toEqual({ "org-a": "acme", "org-b": "side" });
  });

  it("reads an org id before a slug, as the control plane does (#2938)", async () => {
    const twisted = { orgs: [org("org-a", "org-b"), org("org-b", "side")], defaultOrgId: "org-a" };
    expect(await redirectOf(() => resolveOrgRoute("org-b", at("/o/org-b"), { deps: deps({ listOrgs: async () => twisted }) }))).toBe("/o/side");
  });

  it("redirects an org id to the slug form, keeping the rest of the URL", async () => {
    expect(await redirectOf(() => resolveOrgRoute("org-b", at("/o/org-b/workflows/runs", "?project=p-1", "row"), { deps: deps() }))).toBe(
      "/o/side/workflows/runs?project=p-1#row",
    );
  });

  it("redirects a slug spelled in another case to its own spelling", async () => {
    expect(await redirectOf(() => resolveOrgRoute("Side", at("/o/Side"), { deps: deps() }))).toBe("/o/side");
  });

  it("keeps an org with no slug at its id", async () => {
    const bare = org("org-c", "");
    const { orgRoute } = await resolveOrgRoute("org-c", at("/o/org-c"), { deps: deps({ listOrgs: async () => ({ orgs: [bare], defaultOrgId: "org-c" }) }) });
    expect(orgRoute).toEqual({ kind: "member", org: { id: "org-c", slug: "" } });
  });

  it("answers an org that is not the person's, and one that does not exist, the same way", async () => {
    const foreign = await resolveOrgRoute("someone-elses", at("/o/someone-elses"), { deps: deps() });
    const missing = await resolveOrgRoute("org-zzz", at("/o/org-zzz/inbox"), { deps: deps() });
    expect(foreign).toEqual(missing);
    expect(foreign.orgRoute).toEqual({
      kind: "not-member",
      orgs: [
        { id: "org-a", slug: "acme", name: "ACME" },
        { id: "org-b", slug: "side", name: "SIDE" },
      ],
    });
    expect(useActiveOrgStore.getState().chosenId).toBeNull();
  });

  it("waits for sign-in rather than guessing", async () => {
    const list = vi.fn(async () => MINE);
    const { orgRoute } = await resolveOrgRoute("acme", at("/o/acme"), { deps: deps({ signedInUser: async () => null, listOrgs: list }) });
    expect(orgRoute).toEqual({ kind: "signed-out" });
    expect(list).not.toHaveBeenCalled();
  });

  it("reports a membership list it could not read", async () => {
    const { orgRoute } = await resolveOrgRoute("acme", at("/o/acme"), { deps: deps({ listOrgs: async () => Promise.reject(new Error("offline")) }) });
    expect(orgRoute).toMatchObject({ kind: "error", error: { message: "offline" } });
  });

  it("opens the org-less page where there are no orgs to name", async () => {
    expect(await redirectOf(() => resolveOrgRoute("acme", at("/o/acme/apps/shop/releases", "?env=prod"), { deps: deps({ hasOrgs: () => false }) }))).toBe(
      "/forge/p/shop/releases?env=prod",
    );
    expect(await redirectOf(() => resolveOrgRoute("acme", at("/o/acme"), { deps: deps({ signedInUser: async () => "apikey-user" }) }))).toBe("/");
  });
});

describe("entryOrgSegment (what a bare / opens in)", () => {
  it("is this tab's last org first", async () => {
    sessionStorage.setItem("reliant.activeOrg.alice", "org-b");
    localStorage.setItem("reliant.activeOrg.alice", "org-a");
    expect(await entryOrgSegment(deps())).toBe("side");
  });

  it("then this browser's last org", async () => {
    localStorage.setItem("reliant.activeOrg.alice", "org-b");
    expect(await entryOrgSegment(deps())).toBe("side");
  });

  it("then the person's default org", async () => {
    expect(await entryOrgSegment(deps())).toBe("acme");
  });

  it("skips a remembered org the person has left", async () => {
    sessionStorage.setItem("reliant.activeOrg.alice", "org-gone");
    expect(await entryOrgSegment(deps())).toBe("acme");
  });

  it("is none for nobody signed in, a deployment without orgs, or a person with none", async () => {
    expect(await entryOrgSegment(deps({ signedInUser: async () => null }))).toBeNull();
    expect(await entryOrgSegment(deps({ hasOrgs: () => false }))).toBeNull();
    expect(await entryOrgSegment(deps({ listOrgs: async () => ({ orgs: [], defaultOrgId: "" }) }))).toBeNull();
  });
});

describe("forwardIntoOrg", () => {
  it("does nothing with the flag off", () => {
    const list = vi.fn(async () => MINE);
    expect(forwardIntoOrg(at("/inbox"), deps({ listOrgs: list }))).toBeUndefined();
    expect(list).not.toHaveBeenCalled();
  });

  describe("with the flag on", () => {
    beforeEach(() => setRuntimeConfig({ ORG_ROUTES: "true" }));

    it("forwards an org-scoped page into the tab's org, keeping the query and hash", async () => {
      sessionStorage.setItem("reliant.activeOrg.alice", "org-b");
      expect(await redirectOf(() => forwardIntoOrg(at("/workflows/library", "?show=on", "top"), deps()))).toBe("/o/side/workflows/library?show=on#top");
      expect(await redirectOf(() => forwardIntoOrg(at("/"), deps()))).toBe("/o/side");
      expect(await redirectOf(() => forwardIntoOrg(at("/forge/observability/logs"), deps()))).toBe("/o/side/observability/logs");
    });

    it("leaves a path that is not forwarded alone, synchronously", () => {
      expect(forwardIntoOrg(at("/settings/members"), deps())).toBeUndefined();
      expect(forwardIntoOrg(at("/project/p-1"), deps())).toBeUndefined();
      expect(forwardIntoOrg(at("/o/acme/inbox"), deps())).toBeUndefined();
    });

    it("opens the page as it is when there is no org to name", async () => {
      await expect(forwardIntoOrg(at("/inbox"), deps({ signedInUser: async () => null }))).resolves.toBeUndefined();
    });

    it.each([
      ["/apps", "/o/side/apps"],
      ["/sites", "/o/side/sites"],
      ["/usage", "/o/side/usage"],
    ])("forwards on the forge host too, which mounts the same tree (U6): %s → %s", async (from, to) => {
      resetShellForTests("forge");
      sessionStorage.setItem("reliant.activeOrg.alice", "org-b");
      expect(await redirectOf(() => forwardIntoOrg(at(from), deps()))).toBe(to);
    });

    it.each([
      ["/sites", "/o/side/sites"],
      ["/usage", "/o/side/usage"],
    ])("forwards Sites and Usage, which every host now serves: %s → %s", async (from, to) => {
      sessionStorage.setItem("reliant.activeOrg.alice", "org-b");
      expect(await redirectOf(() => forwardIntoOrg(at(from), deps()))).toBe(to);
    });
  });
});
