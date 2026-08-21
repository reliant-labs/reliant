import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/api/settings-grpc", () => ({
  settingsGrpc: {
    completeCodexOAuth: vi.fn(),
    completeClaudeOAuth: vi.fn(),
  },
}));

vi.mock("@/api/daemon-grpc", () => ({
  startOAuthViaDaemon: vi.fn(),
}));

vi.mock("@/lib/oauth-local", () => ({
  startOAuthViaLocalServer: vi.fn(),
}));

import { startOAuthViaDaemon } from "@/api/daemon-grpc";
import { runCodexOAuthFlow } from "@/lib/codex-oauth";
import { runClaudeOAuthFlow } from "@/lib/claude-oauth";

/**
 * PKCE needs SHA-256, which lives on `crypto.subtle`. That object only exists
 * in a SECURE CONTEXT — an https:// page, or http:// on localhost/127.0.0.1.
 * Reach the app over plain http:// on a LAN IP or a tunnel host (the mobile
 * surface and the ngrok/cloudflared hosts `vite.config.ts` allow-lists are
 * exactly that shape) and `crypto.subtle` is `undefined`.
 *
 * Before this fix the flow read `crypto.subtle.digest` unguarded and died with
 * "Cannot read properties of undefined (reading 'digest')" — a TypeError that
 * names neither the cause nor the remedy, thrown out of the flow rather than
 * returned as a result the UI knows how to display.
 */

const originalCrypto = globalThis.crypto;

/** A non-secure context: crypto exists, crypto.subtle does not. */
const stubInsecureContext = () => {
  Object.defineProperty(globalThis, "crypto", {
    configurable: true,
    writable: true,
    value: {
      getRandomValues: (values: Uint8Array) => {
        for (let i = 0; i < values.length; i += 1) values[i] = (i * 31) % 256;
        return values;
      },
      // subtle intentionally absent — this is what a non-secure origin gives you.
    } as unknown as Crypto,
  });
};

afterEach(() => {
  Object.defineProperty(globalThis, "crypto", {
    configurable: true,
    writable: true,
    value: originalCrypto,
  });
  vi.clearAllMocks();
});

describe.each([
  ["Codex", runCodexOAuthFlow],
  ["Claude", runClaudeOAuthFlow],
])("%s OAuth on a non-secure origin", (_label, runFlow) => {
  it("returns a failed result instead of throwing a TypeError", async () => {
    stubInsecureContext();

    // Must not reject: the UI hooks render `result.message`.
    const result = await runFlow();

    expect(result.ok).toBe(false);
  });

  it("reports pkce_generation_failed with an actionable message", async () => {
    stubInsecureContext();

    const result = await runFlow();

    if (result.ok) throw new Error("expected the flow to fail");
    expect(result.errorCode).toBe("pkce_generation_failed");
    // The message must name the cause (insecure origin) and the remedy
    // (HTTPS/localhost) rather than leaking "reading 'digest'".
    expect(result.message).toMatch(/secure context|HTTPS/i);
    expect(result.message).not.toMatch(/digest/);
  });

  it("never starts the browser round trip it cannot complete", async () => {
    stubInsecureContext();

    await runFlow();

    // Without a code challenge there is nothing to authorize against; opening
    // a browser window here would strand the user on a dead flow.
    expect(startOAuthViaDaemon).not.toHaveBeenCalled();
  });
});
