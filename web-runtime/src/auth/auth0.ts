// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Auth0 (auth0-spa-js) → the standard auth events, from the browser.
//
//   import { syncAuth0, reportAuth0SignOut } from "@reliantlabs/forge-web-runtime/auth/auth0";
//   const auth0 = await createAuth0Client({ ... });
//   if (location.search.includes("code=")) await auth0.handleRedirectCallback();
//   await syncAuth0(auth0);                          // on load, and after the redirect callback
//   ...
//   await reportAuth0SignOut(auth0); auth0.logout(); // before logout redirects away
//
// auth0-spa-js has no change listener, so the app calls syncAuth0 where it
// learns who is signed in: identifyUser(user.sub) for a signed-in user (the id
// on spans and replays, and the daily `resumed` sign-in), the id cleared
// otherwise. logout() navigates away, so its auth_signed_out is reported
// first with reportAuth0SignOut. Credential sign-ins and sign-ups come from the
// server (forge/pkg/authhooks' Auth0 Log Stream handler).
//
// With @auth0/auth0-react, call identifyUser(user?.sub ?? null, { provider:
// "auth0" }) from the barrel wherever useAuth0() changes instead.
//
// Structurally typed: nothing is imported from @auth0/*, so it adds no
// dependency.
import { identifyUser, trackAuthEvent } from "../auth-events.js";

/** The part of an Auth0Client the adapter reads. */
export interface Auth0ClientLike {
  isAuthenticated(): Promise<boolean>;
  getUser(): Promise<{ sub?: string } | undefined>;
}

export interface Auth0AuthTelemetryOptions {
  /** The provider name the events carry; "auth0" by default. */
  provider?: string;
}

/**
 * Identify the signed-in Auth0 user, or clear the id when nobody is. Call it
 * on load and after handleRedirectCallback. Never throws or rejects.
 */
export async function syncAuth0(
  client: Auth0ClientLike,
  options?: Auth0AuthTelemetryOptions,
): Promise<void> {
  try {
    const sub = (await client.isAuthenticated())
      ? (await client.getUser())?.sub
      : undefined;
    identifyUser(sub || null, { provider: options?.provider ?? "auth0" });
  } catch {
    // Telemetry must never break the app.
  }
}

/**
 * Report auth_signed_out for the signed-in Auth0 user, then clear the id.
 * Call it before logout(), which navigates away. Never throws or rejects.
 */
export async function reportAuth0SignOut(
  client: Auth0ClientLike,
  options?: Auth0AuthTelemetryOptions,
): Promise<void> {
  try {
    const sub = (await client.getUser())?.sub;
    if (sub) {
      trackAuthEvent("signed_out", {
        userId: sub,
        provider: options?.provider ?? "auth0",
      });
      return;
    }
    identifyUser(null);
  } catch {
    // Telemetry must never break the app.
  }
}
