// Copyright (c) 2025 Reliant Labs

/**
 * `reliant://open?url=` links in the renderer (lib/openRouteLink.ts): checked
 * again on this side of the process boundary, then navigated to.
 */

import { afterEach, describe, expect, it, vi } from "vitest";

import { isOpenableRoute, listenForOpenRouteLinks } from "../openRouteLink";

describe("isOpenableRoute", () => {
  it.each(["/c/c-1", "/c/c-1/t/t-1?view=detailed", "/o/acme/c/c-1", "/new", "/new?project=p-1"])("opens %s", (route) => {
    expect(isOpenableRoute(route)).toBe(true);
  });

  it.each(["", "c/c-1", "//evil.example/c/1", "/\\evil", "https://evil.example/c/1", "javascript:alert(1)", "/settings", "/newish", "/c/1\n"])(
    "refuses %j",
    (route) => {
      expect(isOpenableRoute(route)).toBe(false);
    },
  );
});

describe("listenForOpenRouteLinks", () => {
  afterEach(() => {
    delete (window as { electronAPI?: unknown }).electronAPI;
  });

  it("navigates to an openable route and ignores the rest", () => {
    let deliver: (route: string) => void = () => {};
    (window as { electronAPI?: unknown }).electronAPI = {
      onOpenRoute: (callback: (route: string) => void) => {
        deliver = callback;
        return () => {};
      },
    };
    const navigate = vi.fn();
    listenForOpenRouteLinks({ navigate });
    deliver("/o/acme/c/c-1");
    deliver("//evil.example");
    expect(navigate.mock.calls).toEqual([[{ href: "/o/acme/c/c-1" }]]);
  });

  it("does nothing outside the desktop app, or on one that predates the links", () => {
    expect(() => listenForOpenRouteLinks({ navigate: vi.fn() })()).not.toThrow();
    (window as { electronAPI?: unknown }).electronAPI = {};
    expect(() => listenForOpenRouteLinks({ navigate: vi.fn() })()).not.toThrow();
  });
});
