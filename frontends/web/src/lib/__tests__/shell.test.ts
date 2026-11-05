import { afterEach, describe, expect, it } from "vitest";

import { setRuntimeConfig } from "@/test/runtimeConfig";
import { getAppURL } from "@/lib/constants";
import {
  DEFAULT_FORGE_APP_HOST,
  SHELL_OVERRIDE_KEY,
  forgeAppHost,
  forgeAppOrigin,
  normalizeHost,
  reliantAppOrigin,
  resetShellForTests,
  resolveShell,
  type ShellInputs,
} from "@/lib/shell";

/**
 * One bundle, two shells: the hostname picks Reliant or the forge shell, with
 * a remembered `?shell=` override for looking at the forge shell without DNS.
 */

function memoryStorage(initial: Record<string, string> = {}) {
  const data = new Map(Object.entries(initial));
  return {
    data,
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v),
    removeItem: (k: string) => void data.delete(k),
  };
}

const base = (over: Partial<ShellInputs> = {}): ShellInputs => ({
  hostname: "app.reliantlabs.io",
  search: "",
  forgeHost: DEFAULT_FORGE_APP_HOST,
  electron: false,
  storage: memoryStorage(),
  ...over,
});

afterEach(() => resetShellForTests());

describe("resolveShell", () => {
  it("is Reliant on Reliant's host", () => {
    expect(resolveShell(base())).toBe("reliant");
  });

  it("is forge on the forge host (forge.reliantlabs.io by default), case-insensitively", () => {
    expect(DEFAULT_FORGE_APP_HOST).toBe("forge.reliantlabs.io");
    expect(resolveShell(base({ hostname: "forge.reliantlabs.io" }))).toBe("forge");
    expect(resolveShell(base({ hostname: "FORGE.reliantlabs.io" }))).toBe("forge");
  });

  it("does not match a lookalike host", () => {
    expect(resolveShell(base({ hostname: "forge.reliantlabs.io.evil.test" }))).toBe("reliant");
    expect(resolveShell(base({ hostname: "app.forge.reliantlabs.io" }))).toBe("reliant");
  });

  it("follows a configured forge host", () => {
    expect(resolveShell(base({ hostname: "console.example.test", forgeHost: "console.example.test" }))).toBe("forge");
    expect(resolveShell(base({ hostname: "forge.reliantlabs.io", forgeHost: "console.example.test" }))).toBe("reliant");
  });

  it("?shell=forge picks the forge shell anywhere and remembers it; ?shell=auto forgets it", () => {
    const storage = memoryStorage();
    expect(resolveShell(base({ hostname: "localhost", search: "?shell=forge", storage }))).toBe("forge");
    expect(storage.data.get(SHELL_OVERRIDE_KEY)).toBe("forge");
    // The query string is gone on the next load (a sign-in redirect); the override is not.
    expect(resolveShell(base({ hostname: "localhost", storage }))).toBe("forge");
    expect(resolveShell(base({ hostname: "localhost", search: "?shell=auto", storage }))).toBe("reliant");
    expect(storage.data.has(SHELL_OVERRIDE_KEY)).toBe(false);
  });

  it("?shell=reliant overrides the forge host", () => {
    expect(resolveShell(base({ hostname: "forge.reliantlabs.io", search: "?shell=reliant" }))).toBe("reliant");
  });

  it("ignores an unknown override value and survives storage that throws", () => {
    const throwing = {
      getItem: () => {
        throw new Error("denied");
      },
      setItem: () => {
        throw new Error("denied");
      },
      removeItem: () => {
        throw new Error("denied");
      },
    };
    expect(resolveShell(base({ search: "?shell=nonsense" }))).toBe("reliant");
    expect(resolveShell(base({ search: "?shell=forge", storage: throwing }))).toBe("forge");
    expect(resolveShell(base({ hostname: "forge.reliantlabs.io", storage: throwing }))).toBe("forge");
  });

  it("is always Reliant in the desktop app, override or not", () => {
    expect(resolveShell(base({ hostname: "forge.reliantlabs.io", search: "?shell=forge", electron: true }))).toBe("reliant");
  });
});

describe("hosts", () => {
  it("normalises a configured host however it is written", () => {
    expect(normalizeHost("https://Forge.Example.test/")).toBe("forge.example.test");
    expect(normalizeHost("forge.example.test:443")).toBe("forge.example.test");
    expect(normalizeHost("")).toBe("");
  });

  it("reads FORGE_APP_HOST from the runtime config, defaulting to forge.reliantlabs.io", () => {
    setRuntimeConfig({});
    expect(forgeAppHost()).toBe("forge.reliantlabs.io");
    expect(forgeAppOrigin()).toBe("https://forge.reliantlabs.io");
    setRuntimeConfig({ FORGE_APP_HOST: "https://forge.staging.test" });
    expect(forgeAppHost()).toBe("forge.staging.test");
  });

  it("links to Reliant at APP_URL, never back at the forge host itself", () => {
    setRuntimeConfig({ APP_URL: "https://app.staging.test/" });
    expect(reliantAppOrigin()).toBe("https://app.staging.test");
    setRuntimeConfig({ APP_URL: "https://forge.reliantlabs.io" });
    expect(reliantAppOrigin()).toBe("https://app.reliantlabs.io");
  });
});

describe("getAppURL in the forge shell", () => {
  it("is this origin even when APP_URL names Reliant's, so OAuth returns to the forge host", () => {
    setRuntimeConfig({ APP_URL: "https://app.reliantlabs.io" });
    expect(getAppURL()).toBe("https://app.reliantlabs.io");
    resetShellForTests("forge");
    expect(getAppURL()).toBe(window.location.origin);
  });
});
