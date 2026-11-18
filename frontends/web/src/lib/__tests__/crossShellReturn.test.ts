import { describe, expect, it, vi } from "vitest";

const shell = vi.hoisted(() => ({ forge: false }));
vi.mock("@/lib/shell", () => ({
  forgeAppOrigin: () => "https://forge.reliantlabs.io",
  isForgeShell: () => shell.forge,
}));

import { crossShellReturnTo, forgeShellReturnUrl, FORGE_SHELL_RETURN_PREFIX } from "@/lib/crossShellReturn";

describe("crossShellReturnTo", () => {
  it("marks a forge-site path, and leaves Reliant's alone", () => {
    shell.forge = true;
    expect(crossShellReturnTo("/onboarding?plan=x")).toBe(`${FORGE_SHELL_RETURN_PREFIX}/onboarding?plan=x`);
    shell.forge = false;
    expect(crossShellReturnTo("/onboarding?plan=x")).toBe("/onboarding?plan=x");
  });
});

describe("forgeShellReturnUrl", () => {
  it.each([
    ["/__forge/onboarding?plan=x", "https://forge.reliantlabs.io/onboarding?plan=x"],
    ["/__forge/apps", "https://forge.reliantlabs.io/apps"],
    ["/onboarding", null],
    ["/__forgex/apps", null],
    ["/__forge//evil.com/x", null],
    ["/__forge/\\evil.com", null],
    [undefined, null],
  ])("%s → %s", (returnTo, expected) => {
    expect(forgeShellReturnUrl(returnTo)?.toString() ?? null).toBe(expected);
  });
});
