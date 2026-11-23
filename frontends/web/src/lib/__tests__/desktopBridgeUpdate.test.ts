/**
 * A desktop build whose preload predates a bridge must get a clear "update the
 * desktop app" error, never a TypeError.
 *
 * The desktop app loads the web bundle from the server, so the bundle can be
 * newer than the preload that exposes `window.electronAPI`. Each case runs
 * against an OLD preload (the bridge missing) and a CURRENT one.
 */
import { afterEach, describe, expect, it, vi } from "vitest";

import { DesktopUpdateRequiredError } from "@/lib/desktopUpdate";
import { openCheckout, openEmbeddedCheckoutWindow } from "@/lib/stripeCheckout";
import { startOAuthViaDesktop } from "@/lib/oauth-desktop";
import { startOAuthSignIn } from "@/lib/oauth-signin";

const setPreload = (api: Record<string, unknown>) => {
  (window as unknown as { electronAPI: unknown }).electronAPI = api;
};

/** A preload from before the bridges: other desktop features, none of these. */
const oldPreload = () => setPreload({ openExternal: vi.fn() });

afterEach(() => {
  delete (window as unknown as { electronAPI?: unknown }).electronAPI;
});

describe("Stripe checkout on the desktop app", () => {
  const stripeUrl = "https://checkout.stripe.com/c/pay/cs_test_123";

  it("asks for an update when the preload has no checkout window", async () => {
    oldPreload();
    const before = window.location.href;

    await expect(openCheckout(stripeUrl, "https://app.example/return")).rejects.toThrow(
      DesktopUpdateRequiredError,
    );
    await expect(
      openEmbeddedCheckoutWindow({ kind: "compute_plan", planId: "plan-1" }),
    ).rejects.toThrow(/Update the Reliant desktop app/);
    // The system browser stays off the checkout path: no redirect fallback.
    expect(window.location.href).toBe(before);
  });

  it("hosts the trip in the desktop window when the preload has it", async () => {
    const openStripeCheckout = vi.fn(async () => ({ outcome: "success" as const }));
    setPreload({ openStripeCheckout });

    await expect(openCheckout(stripeUrl, "https://app.example/return")).resolves.toBe("success");
    await expect(
      openEmbeddedCheckoutWindow({ kind: "compute_plan", planId: "plan-1" }),
    ).resolves.toBe("success");
    expect(openStripeCheckout).toHaveBeenCalledTimes(2);
    expect(openStripeCheckout).toHaveBeenCalledWith({
      checkoutUrl: stripeUrl,
      returnUrl: "https://app.example/return",
    });
  });
});

describe("provider sign-in through the desktop receiver", () => {
  it("asks for an update when the preload has no receiver", async () => {
    oldPreload();
    await expect(startOAuthViaDesktop("https://auth.example/?r={redirect_uri}")).rejects.toThrow(
      DesktopUpdateRequiredError,
    );
  });

  it("asks for an update when the preload has only half of the receiver", async () => {
    setPreload({ startProviderOAuth: vi.fn() });
    await expect(startOAuthViaDesktop("https://auth.example/?r={redirect_uri}")).rejects.toThrow(
      DesktopUpdateRequiredError,
    );
  });

  it("runs the flow when the preload has the receiver", async () => {
    setPreload({
      startProviderOAuth: vi.fn(async () => ({ flowId: "f", redirectUri: "http://localhost:1/cb" })),
      waitForProviderOAuth: vi.fn(async () => ({
        code: "c",
        state: "s",
        redirectUri: "http://localhost:1/cb",
      })),
      cancelProviderOAuth: vi.fn(),
    });
    await expect(startOAuthViaDesktop("https://auth.example/?r={redirect_uri}")).resolves.toEqual({
      code: "c",
      state: "s",
      redirectUri: "http://localhost:1/cb",
    });
  });
});

describe("account sign-in on the desktop app", () => {
  it("asks for an update when the preload has no loopback listener", async () => {
    oldPreload();
    await expect(startOAuthSignIn("google")).rejects.toThrow(DesktopUpdateRequiredError);
  });
});
