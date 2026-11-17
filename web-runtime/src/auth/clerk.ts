// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Clerk → the standard auth events, from the browser.
//
//   import { createClerkAuthTelemetry } from "@reliantlabs/forge-web-runtime/auth/clerk";
//   await clerk.load();
//   createClerkAuthTelemetry(clerk);
//
// Clerk calls addListener's callback with its resources whenever they
// change. A user → identifyUser(user.id) (the id on spans and replays, and the
// daily `resumed` sign-in); a user that goes away → auth_signed_out for them.
// `user` undefined means Clerk has not loaded yet and is ignored. Credential
// sign-ins and sign-ups come from the server (forge/pkg/authhooks' Clerk
// webhook handler).
//
// Structurally typed: nothing is imported from @clerk/*, so it adds no
// dependency. With @clerk/clerk-react, pass the object useClerk() returns.
import { identityTracker } from "./identity.js";

/** The part of Clerk's resources the adapter reads. */
export interface ClerkResourcesLike {
  user?: { id: string } | null;
}

/** The part of a Clerk instance the adapter subscribes to. */
export interface ClerkLike {
  addListener(callback: (resources: ClerkResourcesLike) => void): () => void;
}

export interface ClerkAuthTelemetryOptions {
  /** The provider name the events carry; "clerk" by default. */
  provider?: string;
}

/**
 * Subscribe telemetry to Clerk's session: identifyUser for the signed-in
 * user, auth_signed_out when they leave. Returns the unsubscribe function.
 * Never throws.
 */
export function createClerkAuthTelemetry(
  clerk: ClerkLike,
  options?: ClerkAuthTelemetryOptions,
): () => void {
  const tracker = identityTracker(options?.provider ?? "clerk");
  try {
    const unsubscribe = clerk.addListener((resources) => {
      try {
        const user = resources?.user;
        if (user === undefined) {
          return; // not loaded yet
        }
        if (user === null || !user.id) {
          if (tracker.current() !== null) {
            tracker.signedOut(true);
          }
          return;
        }
        if (tracker.current() !== user.id) {
          tracker.signedIn(user.id);
        }
      } catch {
        // Telemetry must never break the app.
      }
    });
    return () => {
      try {
        unsubscribe();
      } catch {
        // Telemetry must never break the app.
      }
    };
  } catch {
    return () => {};
  }
}
