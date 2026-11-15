// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The one piece the provider adapters (supabase.ts, clerk.ts, auth0.ts,
// firebase.ts) share: what "a real user is signed in" and "nobody is" mean for
// telemetry. Each adapter only translates its provider's callbacks into these
// two calls, so the rules live here once:
//
//   - a signed-in user is identified (identifyUser): their id joins every
//     span and replay, and the daily `resumed` sign-in counts them as active;
//   - leaving a signed-in user sends `auth_signed_out` for them, then clears
//     the id; with nobody identified there is nothing to sign out, so the id
//     is only cleared;
//   - an anonymous session (Supabase's signInAnonymously, Firebase's
//     anonymous auth) is nobody: it is not a signed-in user and is never
//     counted as one.
//
// Everything goes through auth-events.ts, so GPC/DNT, the analytics tiers and
// "never throws" are inherited, not re-implemented.
import { identifyUser, trackAuthEvent } from "../auth-events.js";

/** Telemetry's view of who is signed in, for one provider's callbacks. */
export interface IdentityTracker {
  /** A real (non-anonymous) user is signed in. */
  signedIn(userId: string): void;
  /**
   * Nobody is signed in. `explicit` says the person signed out (the provider
   * said so); otherwise the user is only cleared.
   */
  signedOut(explicit: boolean): void;
  /** The user identified last, or null. */
  current(): string | null;
}

export function identityTracker(provider: string): IdentityTracker {
  let current: string | null = null;
  return {
    signedIn(userId) {
      if (!userId) {
        return;
      }
      current = userId;
      identifyUser(userId, { provider });
    },
    signedOut(explicit) {
      const last = current;
      current = null;
      if (explicit && last !== null) {
        // trackAuthEvent clears the id after sending.
        trackAuthEvent("signed_out", { userId: last, provider });
        return;
      }
      identifyUser(null);
    },
    current: () => current,
  };
}
