/**
 * Chats and threads are URLs (U5; control-plane
 * docs/design/unified-shell-and-projects.md §3.1, §3.4, §9 R1–R3), through the
 * PRODUCTION route trees (test/orgRouteHarness.ts): where a chat's URL lands
 * with the flag `web.org_routes` off and on, what it resolves to (the chat,
 * not found, an error), how a link into the wrong org is answered, and that a
 * refresh, Back and Forward stay on the chat the URL names.
 *
 * Headless: what the chat shell then does with the answer (open the chat,
 * show the not-found page) is hooks/useChatRoute.ts, tested beside it.
 */

import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
// Pages are not rendered (the harness is headless): every lazy page is a stub.
vi.mock("@/lib/lazyRoute", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/lazyRoute")>()),
  lazyRoute: () => () => null,
}));

import { ConnectError, Code } from "@connectrpc/connect";

import { chatShellRouteOf } from "@/lib/chatRoute";
import { takeMovedChatNotice, type ChatRouteData } from "@/lib/chatRouteResolve";
import { setTransport } from "@/lib/connect";
import { resetShellForTests } from "@/lib/shell";
import { routeTree } from "@/routes";
import { createOrgRouteHarness, type OrgRouteWorld } from "@/test/orgRouteHarness";

const ACME = { id: "org-a", slug: "acme", name: "Acme" };
const SIDE = { id: "org-b", slug: "side", name: "Side" };
const FLAGS = [false, true] as const;

const CHATS = [
  { id: "a-1", orgId: "org-a" },
  { id: "a-2", orgId: "org-a" },
  { id: "b-1", orgId: "org-b" },
  // Another org's chat the person can read (shared with them) but whose org is not theirs.
  { id: "x-1", orgId: "org-x" },
  // From before orgs: its owner's default org's (acme, the first).
  { id: "old-1", orgId: "" },
];

function world(flag: boolean, over: Partial<OrgRouteWorld> = {}): OrgRouteWorld {
  return { host: "reliant", routeTree, flag, orgs: [ACME, SIDE], chats: CHATS, ...over };
}

function data(h: ReturnType<typeof createOrgRouteHarness>): ChatRouteData {
  return h.leafData() as ChatRouteData;
}

const chatId = (h: ReturnType<typeof createOrgRouteHarness>) => {
  const d = data(h);
  return d.kind === "chat" ? d.chat.id : d.kind;
};

afterEach(() => {
  setTransport(null);
  resetShellForTests();
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

describe.each(FLAGS)("web.org_routes %s", (flag) => {
  it("opens a chat's org URL on its chat, in its org", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/acme/c/a-1");
    expect(h.pathname()).toBe("/o/acme/c/a-1");
    expect(h.leafRouteId()).toBe("/o/$org/_app/c/$chatId");
    expect(h.orgRoute()).toEqual({ kind: "member", org: { id: "org-a", slug: "acme" } });
    expect(chatId(h)).toBe("a-1");
    expect(h.notFound()).toBe(false);
  });

  it("opens a thread's URL on its chat, naming the thread", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/acme/c/a-1/t/thread-7");
    expect(h.pathname()).toBe("/o/acme/c/a-1/t/thread-7");
    expect(h.leafRouteId()).toBe("/o/$org/_app/c/$chatId/t/$threadId");
    expect(chatId(h)).toBe("a-1");
    expect(chatShellRouteOf(h.pathname())).toEqual({ kind: "chat", chatId: "a-1", threadId: "thread-7" });
  });

  it("stays on a chat across a refresh", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/acme/c/a-1?view=detailed&panel=changes");
    await h.refresh();
    expect(h.pathname()).toBe("/o/acme/c/a-1");
    expect(h.search()).toMatchObject({ view: "detailed", panel: "changes" });
    expect(chatId(h)).toBe("a-1");
  });

  it("moves between two chats with Back and Forward", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/acme/c/a-1");
    await h.navigate("/o/acme/c/a-2");
    expect(chatId(h)).toBe("a-2");
    await h.back();
    expect(h.pathname()).toBe("/o/acme/c/a-1");
    expect(chatId(h)).toBe("a-1");
    await h.forward();
    expect(h.pathname()).toBe("/o/acme/c/a-2");
    expect(chatId(h)).toBe("a-2");
  });

  it("closes a thread with Back", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/acme/c/a-1");
    await h.navigate("/o/acme/c/a-1/t/thread-7");
    await h.back();
    expect(h.pathname()).toBe("/o/acme/c/a-1");
    expect(chatShellRouteOf(h.pathname())).toEqual({ kind: "chat", chatId: "a-1", threadId: null });
  });

  it("sends a link into the wrong org to the chat's own, with a notice", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/acme/c/b-1/t/thread-7?view=detailed");
    expect(h.pathname()).toBe("/o/side/c/b-1/t/thread-7");
    expect(h.search()).toMatchObject({ view: "detailed" });
    expect(h.orgRoute()).toEqual({ kind: "member", org: { id: "org-b", slug: "side" } });
    expect(chatId(h)).toBe("b-1");
    expect(takeMovedChatNotice("b-1")).toBe("This chat is in Side, so it opened there.");
    // Once.
    expect(takeMovedChatNotice("b-1")).toBeNull();
  });

  it("puts a chat from before orgs in its owner's default org", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/side/c/old-1");
    expect(h.pathname()).toBe("/o/acme/c/old-1");
    expect(chatId(h)).toBe("old-1");
  });

  it("never shows a chat under an org it is not in: one in an org that is not the person's is not found", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/acme/c/x-1");
    expect(h.pathname()).toBe("/o/acme/c/x-1");
    expect(data(h)).toEqual({ kind: "missing", chatId: "x-1" });
  });

  it.each(["/o/acme/c/nope", "/o/acme/c/nope/t/t-1"])("shows an unknown chat (%s) as not found, never the home page", async (url) => {
    const h = createOrgRouteHarness(world(flag));
    await h.open(url);
    expect(h.pathname()).toBe(url);
    expect(data(h)).toEqual({ kind: "missing", chatId: "nope" });
    // The chat shell's own page says so (ChatRouteNotice), not the router's not-found.
    expect(h.notFound()).toBe(false);
  });

  it("shows a chat that could not be read as an error, not as not found", async () => {
    const h = createOrgRouteHarness(world(flag, { chatError: new ConnectError("upstream", Code.Unavailable) }));
    await h.open("/o/acme/c/a-1");
    expect(h.pathname()).toBe("/o/acme/c/a-1");
    expect(data(h).kind).toBe("error");
  });

  it("asks nothing about a chat under an org the person is not in", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/stranger/c/a-1");
    expect(h.pathname()).toBe("/o/stranger/c/a-1");
    expect(h.orgRoute()?.kind).toBe("not-member");
    expect(h.chatReads).toEqual([]);
  });

  it("keeps a chat URL in place for a visitor who is not signed in", async () => {
    const h = createOrgRouteHarness(world(flag, { user: null }));
    await h.open("/c/a-1");
    expect(h.pathname()).toBe("/c/a-1");
    expect(data(h)).toEqual({ kind: "unresolved" });
    expect(h.chatReads).toEqual([]);
  });

  it("opens the new-chat view with its draft context", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/acme/new?project=p-1&machine=d-1&ws=w-1");
    expect(h.pathname()).toBe("/o/acme/new");
    expect(h.leafRouteId()).toBe("/o/$org/_app/new");
    expect(h.search()).toMatchObject({ project: "p-1", machine: "d-1", ws: "w-1" });
  });

  it("drops a view value it does not know rather than failing the route", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/acme/c/a-1?panel=nope&view=fancy");
    expect(h.pathname()).toBe("/o/acme/c/a-1");
    expect(h.search().panel).toBeUndefined();
    expect(h.search().view).toBeUndefined();
    expect(chatId(h)).toBe("a-1");
  });
});

describe("the org-less chat URL", () => {
  it.each(["/c/a-1", "/c/a-1/t/thread-7"])("opens %s where it is with the flag off", async (url) => {
    const h = createOrgRouteHarness(world(false, { tabOrg: "org-b" }));
    await h.open(url);
    expect(h.pathname()).toBe(url);
    expect(h.leafRouteId()).toMatch(/^\/_app\/c\/\$chatId/);
    expect(chatId(h)).toBe("a-1");
  });

  it.each([
    ["/c/a-1", "/o/acme/c/a-1"],
    ["/c/a-1/t/thread-7", "/o/acme/c/a-1/t/thread-7"],
    ["/c/b-1?panel=files", "/o/side/c/b-1"],
    ["/c/old-1", "/o/acme/c/old-1"],
  ])("redirects %s into the chat's org, not the tab's, with the flag on", async (from, to) => {
    // The tab is in side: a chat of acme still opens in acme.
    const h = createOrgRouteHarness(world(true, { tabOrg: "org-b" }));
    await h.open(from);
    expect(h.pathname()).toBe(to);
    expect(h.notFound()).toBe(false);
  });

  it("keeps the search across the redirect", async () => {
    const h = createOrgRouteHarness(world(true));
    await h.open("/c/b-1?panel=files&view=detailed");
    expect(h.search()).toMatchObject({ panel: "files", view: "detailed" });
  });

  it("shows an unknown chat as not found where it is, with the flag on too", async () => {
    const h = createOrgRouteHarness(world(true));
    await h.open("/c/nope");
    expect(h.pathname()).toBe("/c/nope");
    expect(data(h)).toEqual({ kind: "missing", chatId: "nope" });
  });

  it("forwards the new-chat view into the tab's org with the flag on, keeping its draft context", async () => {
    const h = createOrgRouteHarness(world(true, { tabOrg: "org-b" }));
    await h.open("/new?project=p-1");
    expect(h.pathname()).toBe("/o/side/new");
    expect(h.search()).toMatchObject({ project: "p-1" });
  });

  it("leaves the new-chat view where it is with the flag off", async () => {
    const h = createOrgRouteHarness(world(false, { tabOrg: "org-b" }));
    await h.open("/new");
    expect(h.pathname()).toBe("/new");
    expect(h.leafRouteId()).toBe("/_app/new");
  });
});

// U6: the forge host mounts the same route tree (routes.tsx), so it serves a
// chat's URL itself, as the Reliant host does — no hand-off.
describe.each(FLAGS)("the forge host (flag %s)", (flag) => {
  it("opens a chat's org URL on its chat, in its org", async () => {
    const h = createOrgRouteHarness(world(flag, { host: "forge" }));
    await h.open("/o/acme/c/a-1");
    expect(h.pathname()).toBe("/o/acme/c/a-1");
    expect(h.leafRouteId()).toBe("/o/$org/_app/c/$chatId");
    expect(chatId(h)).toBe("a-1");
  });
});
