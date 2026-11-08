import { afterEach, describe, expect, it, vi } from "vitest";
import { captureOAuthCallbackCredentials, takeOAuthCallbackCredentials } from "../oauthCallbackCredentials";

const FAKE = "4/0A_FAKE_NOT_A_REAL_AUTH_CODE";
afterEach(() => {
  takeOAuthCallbackCredentials();
  window.history.replaceState(null, "", "/");
});

describe("OAuth callback boot capture", () => {
  it("removes credentials before a router/telemetry reader, retaining exchange inputs only in memory", () => {
    const query = new URLSearchParams({ code: FAKE, token_hash: "FAKE_EMAIL_HASH", error_description: `Failed ${FAKE}`, error: "server_error", state: "FAKE_STATE", returnTo: "/claim?project=example", source: "link", type: "email_change" });
    window.history.replaceState({ navigation: "keep" }, "", `/auth/callback?${query}#access_token=FAKE_ACCESS&refresh_token=FAKE_REFRESH&id_token=FAKE_ID&anchor=keep`);
    captureOAuthCallbackCredentials();
    const observedByTelemetry = window.location.href;
    expect(observedByTelemetry).not.toMatch(/FAKE|error_description|token_hash|[?&]code=|state=/);
    expect(window.history.state).toEqual({ navigation: "keep" });
    const safeUrl = new URL(observedByTelemetry);
    expect(safeUrl.searchParams.get("returnTo")).toBe("/claim?project=example");
    expect(safeUrl.searchParams.get("source")).toBe("link");
    expect(safeUrl.searchParams.get("type")).toBe("email_change");
    expect(safeUrl.hash).toBe("#anchor=keep");
    expect(takeOAuthCallbackCredentials()).toEqual({ code: FAKE, token_hash: "FAKE_EMAIL_HASH", error: "server_error", error_description: `Failed ${FAKE}` });
    expect(takeOAuthCallbackCredentials()).toEqual({});
  });

  it("captures fragment errors without retaining arbitrary fragment credentials", () => {
    window.history.replaceState(null, "", `/auth/callback#${new URLSearchParams({ error: "server_error", error_description: FAKE, provider_token: "FAKE_PROVIDER", code_verifier: "FAKE_VERIFIER" })}`);
    captureOAuthCallbackCredentials();
    expect(window.location.hash).toBe("");
    expect(takeOAuthCallbackCredentials()).toEqual({ error: "server_error", error_description: FAKE });
  });

  it("leaves GitHub repository linking and non-auth routes alone", () => {
    for (const path of ["/auth/github/callback", "/claim", "/oauth/consent"]) {
      window.history.replaceState(null, "", `${path}?code=FAKE_OTHER_PROTOCOL#state=other`);
      const before = window.location.href;
      captureOAuthCallbackCredentials();
      expect(window.location.href).toBe(before);
      expect(takeOAuthCallbackCredentials()).toEqual({});
    }
  });

  it("aborts boot before telemetry can start if browser history writes are denied", () => {
    window.history.replaceState(null, "", `/auth/callback?code=${encodeURIComponent(FAKE)}`);
    document.body.innerHTML = '<div id="root"></div>';
    const denied = vi.spyOn(window.history, "replaceState").mockImplementation(() => {
      throw new Error(`Denied ${FAKE}`);
    });
    const telemetryStart = vi.fn();
    let failure: unknown;
    try {
      captureOAuthCallbackCredentials();
      telemetryStart(window.location.href);
    } catch (error) {
      failure = error;
    } finally {
      denied.mockRestore();
    }
    expect(telemetryStart).not.toHaveBeenCalled();
    expect(failure).toEqual(new Error("Authentication callback URL could not be cleared safely."));
    expect((failure as Error).cause).toBeUndefined();
    expect(document.body.textContent).toContain("restart sign-in");
    expect(document.body.textContent).not.toContain(FAKE);
    expect(document.querySelector("a")?.getAttribute("href")).toBe("/auth");
    // The browser still holds the URL; no claim that it was successfully cleared.
    window.history.replaceState(null, "", "/auth/callback");
    expect(takeOAuthCallbackCredentials()).toEqual({});
  });

  it("boots before every other application import", async () => {
    const source = await import("../../main.tsx?raw");
    const firstImport = source.default.match(/^import\s+[^;]+;/m)?.[0];
    expect(firstImport).toBe('import "./lib/oauthCallbackBoot";');
  });
});
