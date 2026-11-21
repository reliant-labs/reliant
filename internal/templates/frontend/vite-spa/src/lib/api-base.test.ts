// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Pins where the browser sends API traffic (src/lib/api-base.ts): this page's
// own origin while the dev server proxies the API, the runtime config's
// API_URL in every build.

import { afterEach, describe, expect, it, vi } from "vitest";

import { chooseApiBase, chooseApiBaseFor } from "./api-base";

const preview = "https://5173-abc123.workspaces.reliantapi.com";

describe("chooseApiBase", () => {
  it("sends dev traffic to the page's own origin, whatever the config names", () => {
    expect(
      chooseApiBase({
        devProxyPath: "/_api",
        origin: preview,
        basePath: "",
        configured: "http://localhost:8080",
      }),
    ).toBe(`${preview}/_api`);
  });

  it("uses the configured API URL when nothing proxies it", () => {
    expect(
      chooseApiBase({
        devProxyPath: "",
        origin: preview,
        basePath: "",
        configured: "https://api.example.com/",
      }),
    ).toBe("https://api.example.com");
  });

  // Reliant hosting's config.js: the API on this page's own origin, on every
  // host the site answers on, so it must stay a path.
  it("keeps the platform's same-origin /_api path as a path", () => {
    for (const configured of ["/_api", "/_api/"]) {
      expect(chooseApiBase({ devProxyPath: "", origin: preview, basePath: "", configured })).toBe(
        "/_api",
      );
    }
  });

  it("leaves an empty configured URL empty, which means same-origin", () => {
    expect(
      chooseApiBase({
        devProxyPath: "",
        origin: preview,
        basePath: "",
        configured: "",
      }),
    ).toBe("");
  });
});

describe("apiBase", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
    vi.resetModules();
  });

  // vite.config.ts defines FORGE_DEV_API_PROXY for the dev server only;
  // stubbing the env and re-importing is the closest a test gets to each bundle.
  async function apiBaseUnder(dev: boolean, proxy: string) {
    vi.stubEnv("DEV", dev);
    vi.stubEnv("FORGE_DEV_API_PROXY", proxy);
    vi.resetModules();
    return (await import("./api-base")).apiBase;
  }

  it("goes through this origin under the dev server", async () => {
    const apiBase = await apiBaseUnder(true, "/_api");
    expect(apiBase("http://localhost:8080")).toBe(`${window.location.origin}/_api`);
  });

  it("reads the configured URL in a build, even if the variable leaked", async () => {
    const apiBase = await apiBaseUnder(false, "/_api");
    expect(apiBase("https://api.example.com")).toBe("https://api.example.com");
  });

  it("reads the configured URL when the dev proxy is off", async () => {
    const apiBase = await apiBaseUnder(true, "");
    expect(apiBase("http://localhost:8080")).toBe("http://localhost:8080");
  });
});

// Named same-origin APIs (Frontend.apis): /_api/@<name> beside /_api.
describe("chooseApiBaseFor", () => {
  it("proxies a named API under the dev proxy path, after an @", () => {
    expect(
      chooseApiBaseFor("billing", {
        devProxyPath: "/_api",
        origin: preview,
        basePath: "",
        configured: "http://localhost:8090",
      }),
    ).toBe(`${preview}/_api/@billing`);
  });

  it("keeps the platform's same-origin path as a path in a build", () => {
    expect(
      chooseApiBaseFor("billing", {
        devProxyPath: "",
        origin: preview,
        basePath: "",
        configured: "/_api/@billing/",
      }),
    ).toBe("/_api/@billing");
  });
});

describe("apiBaseFor", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
    vi.resetModules();
  });

  async function apiBaseForUnder(dev: boolean, proxy: string, apis?: Record<string, string>) {
    vi.stubEnv("DEV", dev);
    vi.stubEnv("FORGE_DEV_API_PROXY", proxy);
    vi.stubGlobal("__FORGE_CONFIG__", apis ? { apis } : {});
    vi.resetModules();
    return (await import("./api-base")).apiBaseFor;
  }

  it("goes through this origin under the dev server", async () => {
    const apiBaseFor = await apiBaseForUnder(true, "/_api");
    expect(apiBaseFor("billing")).toBe(`${window.location.origin}/_api/@billing`);
  });

  it("reads config.js's apis in a build", async () => {
    const apiBaseFor = await apiBaseForUnder(false, "", { billing: "/_api/@billing" });
    expect(apiBaseFor("billing")).toBe("/_api/@billing");
  });

  it("refuses a name the environment does not declare", async () => {
    const apiBaseFor = await apiBaseForUnder(false, "", { billing: "/_api/@billing" });
    expect(() => apiBaseFor("biling")).toThrow(/no API named "biling"/);
  });
});
