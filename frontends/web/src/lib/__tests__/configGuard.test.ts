/**
 * The runtime config check that runs before the app boots (lib/configGuard.ts,
 * lib/configReady.ts checkWebConfig).
 *
 * The failure it exists for: a managed deployment whose config.js lacks a
 * key it cannot run without used to boot anyway, against config_gen.ts's
 * self-hosted defaults (a local Supabase, no control plane), and fail
 * screens later with nothing naming the cause. It must instead stop at boot
 * and say which key is missing, on the page.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setRuntimeConfig } from "@/test/runtimeConfig";

const host = vi.hoisted(() => ({ packaged: false }));
vi.mock("@/lib/protocol", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/protocol")>()),
  isPackagedRendererOrigin: () => host.packaged,
}));

import { checkWebConfig } from "../configReady";

/** Every key a managed deployment's document carries. */
const CLOUD = {
  AUTH_MODE: "cloud",
  API_URL: "https://api.example.test",
  CONTROL_PLANE_API_URL: "https://admin.example.test",
  SUPABASE_URL: "https://auth.example.test",
  SUPABASE_ANON_KEY: "sb_publishable_example",
};

beforeEach(() => {
  host.packaged = false;
  document.body.innerHTML = '<div id="root"><p id="untouched">app</p></div>';
});

afterEach(() => {
  document.body.innerHTML = "";
});

describe("checkWebConfig", () => {
  it("accepts an empty document: that is the self-hosted app on its defaults", () => {
    setRuntimeConfig({});
    expect(checkWebConfig()).toEqual([]);
  });

  it("accepts a complete managed document", () => {
    setRuntimeConfig(CLOUD);
    expect(checkWebConfig()).toEqual([]);
  });

  it("names every key a managed document is missing", () => {
    setRuntimeConfig({ AUTH_MODE: "cloud", API_URL: "https://api.example.test" });
    const [problem, ...rest] = checkWebConfig();

    expect(rest).toEqual([]);
    expect(problem).toContain("CONTROL_PLANE_API_URL");
    expect(problem).toContain("SUPABASE_ANON_KEY");
    // SUPABASE_URL has a default (a LOCAL Supabase), which is exactly what a
    // managed deployment must not fall back to, so presence is judged on the
    // document itself.
    expect(problem).toContain("SUPABASE_URL");
    expect(problem).not.toMatch(/requires API_URL/);
  });

  it("treats a value equal to the self-hosted default as missing", () => {
    setRuntimeConfig({ ...CLOUD, SUPABASE_URL: "http://127.0.0.1:54321" });
    expect(checkWebConfig().join("\n")).toContain("requires SUPABASE_URL");
  });

  it("treats a blank value as missing", () => {
    setRuntimeConfig({ ...CLOUD, SUPABASE_ANON_KEY: "  " });
    expect(checkWebConfig().join("\n")).toContain("SUPABASE_ANON_KEY");
  });

  it("refuses an AUTH_MODE outside its set instead of silently reading it as self-hosted", () => {
    setRuntimeConfig({ ...CLOUD, AUTH_MODE: "clould" });
    expect(checkWebConfig().join("\n")).toContain('AUTH_MODE is "clould"');
  });

  it("accepts AUTH_MODE \"self-hosted\": the self-hosted app, with its mode stated", () => {
    setRuntimeConfig({ AUTH_MODE: "self-hosted" });
    expect(checkWebConfig()).toEqual([]);
  });

  it("refuses a flag that is not \"true\", \"false\" or unset", () => {
    setRuntimeConfig({ DISABLE_TLS: "yes" });
    expect(checkWebConfig()).toEqual([
      'DISABLE_TLS is "yes"; it must be "true", "false" or unset.',
    ]);
  });

  it("refuses a value of the wrong type with the generated module's own message", () => {
    setRuntimeConfig({ API_URL: 42 } as unknown as Record<string, string>);
    expect(checkWebConfig().join("\n")).toContain("invalid runtime configuration");
  });

  it("requires a document in the packaged desktop app", () => {
    host.packaged = true;
    window.__FORGE_CONFIG__ = undefined;
    expect(checkWebConfig().join("\n")).toContain("config.js did not load");
  });
});

describe("configGuard (imported second by main.tsx)", () => {
  it("shows what is missing on the page and stops the boot", async () => {
    vi.resetModules();
    setRuntimeConfig({ AUTH_MODE: "cloud" });

    await expect(import("../configGuard")).rejects.toThrow(/invalid runtime configuration/);

    const alert = document.querySelector('#root [role="alert"]');
    expect(alert).not.toBeNull();
    expect(alert?.textContent).toContain("SUPABASE_ANON_KEY");
    expect(alert?.textContent).toContain("config_schema_gen.json");
    // The app's own markup is gone: nothing renders half-configured behind it.
    expect(document.getElementById("untouched")).toBeNull();
  });

  it("renders values as text, never as markup", async () => {
    vi.resetModules();
    setRuntimeConfig({ AUTH_MODE: '<img src=x onerror="alert(1)">' });

    await expect(import("../configGuard")).rejects.toThrow();
    expect(document.querySelector("#root img")).toBeNull();
    expect(document.querySelector('#root [role="alert"]')?.textContent).toContain("<img");
  });

  it("does nothing to a document that can work", async () => {
    vi.resetModules();
    setRuntimeConfig(CLOUD);

    await expect(import("../configGuard")).resolves.toBeDefined();
    expect(document.getElementById("untouched")).not.toBeNull();
  });
});
