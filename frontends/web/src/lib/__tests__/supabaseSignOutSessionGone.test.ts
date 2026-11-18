// Copyright (c) 2025 Reliant Labs

/**
 * Signing out of a session the server has already ended must succeed and
 * clear the local copy.
 *
 * GoTrue answers /logout for a session it no longer has with 403
 * `session_not_found`, which auth-js raises as AuthSessionMissingError.
 * auth-js before 2.92.0 returned that error from signOut() WITHOUT removing
 * the stored session, so authStore.signOut threw and the browser stayed
 * signed in: prod obs 542d1acb (forge-web, 2026-11-17), one user's three
 * sign-out attempts in two minutes. This pins the dependency's fix.
 */

import { describe, expect, it } from "vitest";
import { GoTrueClient } from "@supabase/auth-js";

const STORAGE_KEY = "test-auth";

function memoryStorage() {
  const items = new Map<string, string>();
  return {
    items,
    getItem: (key: string) => items.get(key) ?? null,
    setItem: (key: string, value: string) => void items.set(key, value),
    removeItem: (key: string) => void items.delete(key),
  };
}

function unsignedJWT(claims: Record<string, unknown>): string {
  const b64 = (v: unknown) => btoa(JSON.stringify(v)).replace(/=+$/, "").replace(/\+/g, "-").replace(/\//g, "_");
  return `${b64({ alg: "HS256", typ: "JWT" })}.${b64(claims)}.sig`;
}

describe("supabase signOut of a session the server already ended", () => {
  it("resolves without an error and removes the stored session", async () => {
    const storage = memoryStorage();
    const expiresAt = Math.floor(Date.now() / 1000) + 3600;
    storage.setItem(
      STORAGE_KEY,
      JSON.stringify({
        access_token: unsignedJWT({ sub: "u1", exp: expiresAt, session_id: "gone" }),
        refresh_token: "r1",
        token_type: "bearer",
        expires_in: 3600,
        expires_at: expiresAt,
        user: { id: "u1", aud: "authenticated", app_metadata: {}, user_metadata: {}, created_at: "2026-01-01T00:00:00Z" },
      }),
    );
    const fetch = async (input: RequestInfo | URL) => {
      if (String(input).includes("/logout")) {
        return new Response(JSON.stringify({ code: 403, error_code: "session_not_found", msg: "Session from session_id claim in JWT does not exist" }), {
          status: 403,
          headers: { "Content-Type": "application/json", "x-supabase-api-version": "2024-01-01" },
        });
      }
      return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });
    };
    const client = new GoTrueClient({
      url: "http://auth.test/auth/v1",
      storageKey: STORAGE_KEY,
      storage,
      fetch,
      autoRefreshToken: false,
      persistSession: true,
      detectSessionInUrl: false,
    });

    const { error } = await client.signOut();

    expect(error).toBeNull();
    expect(storage.items.has(STORAGE_KEY)).toBe(false);
  });
});
