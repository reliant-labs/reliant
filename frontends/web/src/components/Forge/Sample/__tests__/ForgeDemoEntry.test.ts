// Copyright (c) 2026 Reliant Labs

/**
 * `/forge/demo` in the app's tree: signed in, the page an old demo link names
 * on the sample app in the shell; signed out, the demo host.
 */

import { isRedirect } from "@tanstack/react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

const openExternalLink = vi.hoisted(() => vi.fn(async () => undefined));
vi.mock("@/lib/open-link", () => ({ openExternalLink }));

import { useAuthStore } from "@/store/authStore";

import { enterForgeDemo, sampleHrefForDemo } from "../ForgeDemoEntry";

describe("sampleHrefForDemo", () => {
  it("opens the sample app's page for the panel's view, keeping the open trace and the tour", () => {
    expect(sampleHrefForDemo({})).toBe("/forge/p/sample_shop");
    expect(sampleHrefForDemo({ view: "overview" })).toBe("/forge/p/sample_shop");
    expect(sampleHrefForDemo({ view: "traces", trace: "abc", tour: "forge:fg-hops" })).toBe(
      "/forge/p/sample_shop/observability/traces?trace=abc&tour=forge%3Afg-hops",
    );
  });

  it("drops the panel's own keys, which name nothing on the console", () => {
    expect(sampleHrefForDemo({ section: "built", browser: "data", view: "errors", issue: undefined })).toBe("/forge/p/sample_shop/observability/errors");
  });
});

/** Where enterForgeDemo sends `/forge/demo<searchStr>`: the redirect's options. */
async function entered(search: Record<string, unknown>, searchStr: string) {
  try {
    await enterForgeDemo({ location: { search, searchStr } });
  } catch (err) {
    if (isRedirect(err)) return err.options as { to?: string; href?: string; search?: Record<string, unknown> };
    throw err;
  }
  throw new Error("enterForgeDemo returned without redirecting");
}

describe("enterForgeDemo", () => {
  const initial = useAuthStore.getState();
  afterEach(() => {
    useAuthStore.setState(initial, true);
    delete (window as { electronAPI?: unknown }).electronAPI;
    openExternalLink.mockClear();
  });

  it("signed in, opens the page the link names on the sample app, the tour kept", async () => {
    useAuthStore.setState({ initialized: true, user: { id: "u1" } as never });
    const to = await entered({ view: "traces", tour: "forge:fg-trace" }, "?view=traces&tour=forge:fg-trace");
    expect(to.to).toBe("/forge/p/sample_shop/observability/traces");
    expect(to.search).toEqual({ tour: "forge:fg-trace" });
    expect(to.href).toBeUndefined();
  });

  it("signed in on the desktop app, too: the sample is in its own shell, never the browser", async () => {
    (window as { electronAPI?: unknown }).electronAPI = {};
    useAuthStore.setState({ initialized: true, user: { id: "u1" } as never });
    expect((await entered({}, "")).to).toBe("/forge/p/sample_shop");
    expect(openExternalLink).not.toHaveBeenCalled();
  });

  it("signed out, leaves for the demo host: there is no shell to walk", async () => {
    useAuthStore.setState({ initialized: true, user: null });
    expect((await entered({ view: "traces" }, "?view=traces")).href).toBe("https://demo.reliantlabs.io/forge/demo?view=traces");
  });

  it("waits for the session before it decides, so a signed-in person is never sent off first", async () => {
    const initialize = vi.fn(async () => useAuthStore.setState({ initialized: true, user: { id: "u1" } as never }));
    useAuthStore.setState({ initialized: false, user: null, initialize });
    expect((await entered({}, "")).to).toBe("/forge/p/sample_shop");
    expect(initialize).toHaveBeenCalledOnce();
  });
});
