// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Pins where the browser sends API traffic (src/lib/api-base.ts): this page's
// own origin while `next dev` proxies the API, the runtime config's API_URL in
// every build.

import { afterEach, describe, expect, it, vi } from "vitest";

import { chooseApiBase } from "./api-base";
import { BASE_PATH } from "./basepath_gen";

const preview = "https://3000-abc123.workspaces.reliantapi.com";
// Any mount prefix will do for the pure function. Deliberately not a real
// one: this project's own base path lives only in basepath_gen.ts, never as a
// hand-written literal.
const mount = "/mount";

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

  it("keeps the frontend's base path in front of the proxy path", () => {
    expect(
      chooseApiBase({ devProxyPath: "/_api", origin: preview, basePath: mount, configured: "" }),
    ).toBe(`${preview}${mount}/_api`);
  });

  it("is relative when there is no window to name an origin", () => {
    expect(chooseApiBase({ devProxyPath: "/_api", origin: "", basePath: "", configured: "" })).toBe(
      "/_api",
    );
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

  it("leaves an empty configured URL empty, which means same-origin", () => {
    expect(chooseApiBase({ devProxyPath: "", origin: preview, basePath: "", configured: "" })).toBe(
      "",
    );
  });
});

describe("apiBase", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
    vi.resetModules();
  });

  // next.config.ts inlines FORGE_DEV_API_PROXY; stubbing the env and
  // re-importing is the closest a test gets to each bundle.
  async function apiBaseUnder(nodeEnv: string, proxy: string) {
    vi.stubEnv("NODE_ENV", nodeEnv);
    vi.stubEnv("FORGE_DEV_API_PROXY", proxy);
    vi.resetModules();
    return (await import("./api-base")).apiBase;
  }

  it("goes through this origin under `next dev`", async () => {
    const apiBase = await apiBaseUnder("development", "/_api");
    expect(apiBase("http://localhost:8080")).toBe(`${window.location.origin}${BASE_PATH}/_api`);
  });

  it("reads the configured URL in a production build, even if the variable leaked", async () => {
    const apiBase = await apiBaseUnder("production", "/_api");
    expect(apiBase("https://api.example.com")).toBe("https://api.example.com");
  });

  it("reads the configured URL when the dev proxy is off", async () => {
    const apiBase = await apiBaseUnder("development", "");
    expect(apiBase("http://localhost:8080")).toBe("http://localhost:8080");
  });
});
