/**
 * The desktop forge sign-in helper: which provider config.js names, whether
 * the desktop bridge is there, and that the main process's failure reaches
 * the caller as an Error.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  authProvider,
  FORGE_AUTH_API_BASE,
  isDesktopForgeSignIn,
  notifyDesktopSessionChanged,
  signInWithDesktopBrowser,
  signOutDesktop,
} from "../desktop-forge-sign-in";

type Bridge = NonNullable<Window["electronAPI"]["forgeAuth"]>;

function setConfig(config: Record<string, unknown> | undefined) {
  window.__FORGE_CONFIG__ = config;
}

function setBridge(bridge: Partial<Bridge> | undefined) {
  (window as unknown as { electronAPI?: unknown }).electronAPI = bridge ? { forgeAuth: bridge } : undefined;
}

afterEach(() => {
  setConfig(undefined);
  setBridge(undefined);
});

describe("authProvider", () => {
  it("is supabase without config", () => {
    setConfig(undefined);
    expect(authProvider()).toBe("supabase");
  });

  it.each([["supabase"], ["Forge"], [""], [1], [true]])("is supabase for AUTH_PROVIDER=%j", (value) => {
    setConfig({ AUTH_PROVIDER: value });
    expect(authProvider()).toBe("supabase");
  });

  it("is forge only for exactly \"forge\"", () => {
    setConfig({ AUTH_PROVIDER: "forge" });
    expect(authProvider()).toBe("forge");
  });

  it("reaches reliant-api through the same-origin named API", () => {
    expect(FORGE_AUTH_API_BASE).toBe("/_api/@reliant");
  });
});

describe("isDesktopForgeSignIn", () => {
  it("needs forge mode and the bridge", () => {
    setConfig({ AUTH_PROVIDER: "forge" });
    expect(isDesktopForgeSignIn()).toBe(false);
    setBridge({ signIn: vi.fn() });
    expect(isDesktopForgeSignIn()).toBe(true);
    setConfig({ AUTH_PROVIDER: "supabase" });
    expect(isDesktopForgeSignIn()).toBe(false);
  });

  it("is false for a desktop build whose electronAPI lacks forgeAuth", () => {
    setConfig({ AUTH_PROVIDER: "forge" });
    (window as unknown as { electronAPI?: unknown }).electronAPI = { platform: "darwin" };
    expect(isDesktopForgeSignIn()).toBe(false);
  });
});

describe("signInWithDesktopBrowser", () => {
  it("returns the account the main process signed in", async () => {
    setBridge({ signIn: vi.fn().mockResolvedValue({ success: true, subject: "u-1", email: "a@test" }) });
    await expect(signInWithDesktopBrowser()).resolves.toEqual({ subject: "u-1", email: "a@test" });
  });

  it("throws the main process's message on failure", async () => {
    setBridge({ signIn: vi.fn().mockResolvedValue({ success: false, error: "browser closed" }) });
    await expect(signInWithDesktopBrowser()).rejects.toThrow("browser closed");
  });

  it("throws a generic message when the failure carries none", async () => {
    setBridge({ signIn: vi.fn().mockResolvedValue({ success: false }) });
    await expect(signInWithDesktopBrowser()).rejects.toThrow("Sign-in did not complete.");
  });

  it("throws without the bridge", async () => {
    await expect(signInWithDesktopBrowser()).rejects.toThrow(/cannot sign in through the browser/);
  });
});

describe("signOutDesktop / notifyDesktopSessionChanged", () => {
  it("are no-ops without the bridge", async () => {
    await expect(signOutDesktop()).resolves.toBeUndefined();
    await expect(notifyDesktopSessionChanged()).resolves.toBeUndefined();
  });

  it("call the bridge when present", async () => {
    const signOut = vi.fn().mockResolvedValue({ success: true });
    const sessionChanged = vi.fn().mockResolvedValue({ success: true });
    setBridge({ signOut, sessionChanged });
    await signOutDesktop();
    await notifyDesktopSessionChanged();
    expect(signOut).toHaveBeenCalledOnce();
    expect(sessionChanged).toHaveBeenCalledOnce();
  });
});
