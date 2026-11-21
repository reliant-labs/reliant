// Copyright (c) 2025 Reliant Labs

/**
 * The chat shell's paths read back (lib/chatRoute.ts), and the URL moves that
 * open a chat or the new-chat view: a push for the person's own move, a
 * replace for the app's, nothing when the URL already names it.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useActiveOrgStore } from "@/store/activeOrgStore";
import { setRuntimeConfig } from "@/test/runtimeConfig";

import {
  CHAT_SHELL_PREFIXES,
  chatShellRouteOf,
  navigateToChatUrl,
  navigateToNewChatUrl,
  patchNewChatUrl,
  pathOwnsChat,
  routeOwnsChat,
} from "../chatRoute";

describe("chatShellRouteOf", () => {
  it.each([
    ["/", { kind: "home" }],
    ["/project/p-1", { kind: "home" }],
    ["/o/acme", { kind: "home" }],
    ["/o/acme/project/p-1", { kind: "home" }],
    ["/new", { kind: "new" }],
    ["/o/acme/new", { kind: "new" }],
    ["/c/c-1", { kind: "chat", chatId: "c-1", threadId: null }],
    ["/c/c-1/", { kind: "chat", chatId: "c-1", threadId: null }],
    ["/o/acme/c/c-1/t/t-1", { kind: "chat", chatId: "c-1", threadId: "t-1" }],
    ["/c/a%2Fb/t/x%20y", { kind: "chat", chatId: "a/b", threadId: "x y" }],
  ])("reads %s", (path, route) => {
    expect(chatShellRouteOf(path)).toEqual(route);
  });

  it.each(["/inbox", "/o/acme/inbox", "/workflows/runs/c-1", "/m/chats/c-1", "/c", "/c/c-1/x", "/new/thing", "/settings"])(
    "is no chat-shell page: %s",
    (path) => {
      expect(chatShellRouteOf(path)).toBeNull();
    },
  );

  it("owns the open chat on a chat's page and the new-chat view only", () => {
    expect(pathOwnsChat("/c/c-1")).toBe(true);
    expect(pathOwnsChat("/o/acme/new")).toBe(true);
    expect(pathOwnsChat("/")).toBe(false);
    expect(pathOwnsChat("/project/p-1")).toBe(false);
    expect(CHAT_SHELL_PREFIXES).toEqual(["/c/", "/new"]);
  });
});

describe("moving the URL", () => {
  let location: { pathname: string; search: Record<string, unknown> };
  const navigate = vi.fn();

  function at(pathname: string, search: Record<string, unknown> = {}) {
    location = { pathname, search };
    (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER = {
      get latestLocation() {
        return location;
      },
      state: { location, status: "idle" },
      navigate,
    };
  }

  beforeEach(() => {
    navigate.mockReset();
    setRuntimeConfig({});
  });

  afterEach(() => {
    delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
    useActiveOrgStore.setState({ effectiveId: null, segments: {} });
  });

  it("pushes a chat, a thread, or replaces when asked", () => {
    at("/c/a");
    navigateToChatUrl("b");
    navigateToChatUrl("a", { threadId: "t 1" });
    navigateToChatUrl("c", { replace: true });
    expect(navigate.mock.calls.map(([options]) => options)).toEqual([
      { href: "/c/b", replace: false },
      { href: "/c/a/t/t%201", replace: false },
      { href: "/c/c", replace: true },
    ]);
  });

  it("keeps the chat's valid view and panel when only its thread changes", () => {
    at("/c/a", { view: "simplified", panel: "changes", tour: "x" });
    navigateToChatUrl("a", { threadId: "t-1" });
    at("/c/a/t/t-1", { view: "simplified", panel: "changes" });
    navigateToChatUrl("a");
    at("/c/a", { view: "bogus", panel: "files" });
    navigateToChatUrl("a", { threadId: "t-1" });
    // Another chat has its own.
    navigateToChatUrl("b");
    expect(navigate.mock.calls.map(([options]) => options)).toEqual([
      { href: "/c/a/t/t-1?view=simplified&panel=changes", replace: false },
      { href: "/c/a?view=simplified&panel=changes", replace: false },
      { href: "/c/a/t/t-1?panel=files", replace: false },
      { href: "/c/b", replace: false },
    ]);
  });

  it("does nothing when the URL already names that chat and thread", () => {
    at("/c/a/t/t-1");
    navigateToChatUrl("a", { threadId: "t-1" });
    expect(navigate).not.toHaveBeenCalled();
  });

  it("goes to the chat's own org, not the page's, with the flag on", () => {
    setRuntimeConfig({ ORG_ROUTES: "true" });
    useActiveOrgStore.setState({ effectiveId: "org-a", segments: { "org-a": "acme", "org-b": "side" } });
    at("/o/acme/c/a");
    navigateToChatUrl("b", { orgId: "org-b" });
    expect(navigate).toHaveBeenCalledWith({ href: "/o/side/c/b", replace: false });
  });

  it("opens the new-chat view with its context: a push from a chat, a replace on the view itself", () => {
    at("/c/a");
    navigateToNewChatUrl({ project: "p-1", machine: undefined, ws: "" });
    at("/new", { project: "p-1" });
    navigateToNewChatUrl({ project: "p-2" });
    expect(navigate.mock.calls.map(([options]) => options)).toEqual([
      { to: "/new", search: { project: "p-1" }, replace: false },
      { to: "/new", search: { project: "p-2" }, replace: true },
    ]);
  });

  it("records a choice in the new-chat view's URL, and only there", () => {
    at("/new", { project: "p-1", tour: "x" });
    patchNewChatUrl({ machine: "d-1" });
    expect(navigate).toHaveBeenCalledWith({ to: "/new", search: { project: "p-1", tour: "x", machine: "d-1" }, replace: true });
    navigate.mockReset();
    // Already there: nothing.
    at("/new", { project: "p-1", machine: "d-1" });
    patchNewChatUrl({ machine: "d-1" });
    // Cleared: the key goes.
    patchNewChatUrl({ machine: undefined });
    expect(navigate.mock.calls.map(([options]) => options)).toEqual([{ to: "/new", search: { project: "p-1" }, replace: true }]);
    navigate.mockReset();
    at("/", {});
    patchNewChatUrl({ machine: "d-1" });
    expect(navigate).not.toHaveBeenCalled();
  });

  it("knows when the URL owns the open chat (a restore must not open another)", () => {
    at("/c/a");
    expect(routeOwnsChat()).toBe(true);
    at("/new");
    expect(routeOwnsChat()).toBe(true);
    at("/");
    expect(routeOwnsChat()).toBe(false);
    at("/inbox");
    expect(routeOwnsChat()).toBe(false);
  });
});
