// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Supabase Auth → the standard auth events, from the browser.
//
//   import { createSupabaseAuthTelemetry } from "@reliantlabs/forge-web-runtime/auth/supabase";
//   createSupabaseAuthTelemetry(supabase);   // once, next to createClient
//
// supabase-js reports every change of session through onAuthStateChange. This
// maps them onto identifyUser / trackAuthEvent (auth-events.ts):
//
//   INITIAL_SESSION, SIGNED_IN, TOKEN_REFRESHED, USER_UPDATED with a session
//       → identifyUser(user.id): the id on spans and replays, and the daily
//         `resumed` sign-in. Not a credential sign-in: supabase-js emits
//         SIGNED_IN again when a tab regains focus, so the browser cannot tell
//         a sign-in from a refocus. Credential sign-ins and sign-ups come from
//         the server (forge/pkg/authhooks' Supabase triggers).
//   SIGNED_OUT → auth_signed_out for the user identified last, then the id is
//       cleared.
//
// Anonymous sessions (signInAnonymously) are NOT signed-in users: by default
// they are skipped (the id is cleared, nothing is sent), and the same user id
// is identified once is_anonymous flips to false (linkIdentity, updateUser).
// Pass { anonymous: "identify" } to attach anonymous ids too.
//
// Structurally typed: it imports nothing from @supabase/supabase-js, so it adds
// no dependency, and any client version whose onAuthStateChange has this shape
// fits.
import { identityTracker } from "./identity.js";

/** The part of a Supabase session the adapter reads. */
export interface SupabaseSessionLike {
  user: { id: string; is_anonymous?: boolean | null };
}

/** The part of a SupabaseClient the adapter subscribes to. */
export interface SupabaseClientLike {
  auth: {
    onAuthStateChange(
      callback: (event: string, session: SupabaseSessionLike | null) => void,
    ): { data: { subscription: { unsubscribe(): void } } };
  };
}

export interface SupabaseAuthTelemetryOptions {
  /**
   * What to do with an anonymous session: "skip" (the default) treats it as
   * nobody signed in; "identify" attaches its id like a real user's.
   */
  anonymous?: "skip" | "identify";
  /** The provider name the events carry; "supabase" by default. */
  provider?: string;
}

/**
 * The onAuthStateChange callback that maps Supabase's auth events onto
 * telemetry, for an app that already has its own subscription: call it
 * from there with the same (event, session). Never throws.
 */
export function supabaseAuthTelemetryListener(
  options?: SupabaseAuthTelemetryOptions,
): (event: string, session: SupabaseSessionLike | null) => void {
  const tracker = identityTracker(options?.provider ?? "supabase");
  const identifyAnonymous = options?.anonymous === "identify";
  return (event, session) => {
    try {
      if (event === "SIGNED_OUT") {
        tracker.signedOut(true);
        return;
      }
      const user = session?.user;
      if (!user?.id) {
        if (event === "INITIAL_SESSION") {
          tracker.signedOut(false);
        }
        return;
      }
      if (user.is_anonymous === true && !identifyAnonymous) {
        // An anonymous visitor is nobody; a real user before them is
        // cleared, not signed out (the provider did not say they left).
        if (tracker.current() !== null) {
          tracker.signedOut(false);
        }
        return;
      }
      tracker.signedIn(user.id);
    } catch {
      // Telemetry must never break the app.
    }
  };
}

/**
 * Subscribe telemetry to a Supabase client's auth state: identifyUser for
 * every signed-in (non-anonymous) session, auth_signed_out on sign-out.
 * Returns the unsubscribe function. Never throws.
 */
export function createSupabaseAuthTelemetry(
  client: SupabaseClientLike,
  options?: SupabaseAuthTelemetryOptions,
): () => void {
  try {
    const { data } = client.auth.onAuthStateChange(
      supabaseAuthTelemetryListener(options),
    );
    return () => {
      try {
        data.subscription.unsubscribe();
      } catch {
        // Telemetry must never break the app.
      }
    };
  } catch {
    return () => {};
  }
}
