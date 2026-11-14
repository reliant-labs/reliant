// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Firebase Authentication → the standard auth events, from the browser.
//
//   import { onIdTokenChanged, getAdditionalUserInfo } from "firebase/auth";
//   import { createFirebaseAuthTelemetry, reportFirebaseSignIn, reportFirebaseSignOut }
//     from "@reliantlabs/forge-web-runtime/auth/firebase";
//
//   createFirebaseAuthTelemetry((cb) => onIdTokenChanged(auth, cb));
//   const cred = await signInWithPopup(auth, provider);
//   reportFirebaseSignIn(cred.user, getAdditionalUserInfo(cred));
//   reportFirebaseSignOut(); await signOut(auth);
//
// Firebase has no server-side push of auth events without Identity Platform,
// so the browser is where its sign-ups are exact: getAdditionalUserInfo's
// isNewUser. createFirebaseAuthTelemetry identifies every signed-in user (the
// id on spans and replays, and the daily `resumed` sign-in);
// reportFirebaseSignIn sends auth_signed_up for a new user, else
// auth_signed_in {credential}, with the method from the provider id.
//
// Anonymous users (signInAnonymously) are not signed-in users: they are never
// identified or counted, and linking a credential to one (linkWithCredential,
// isNewUser false) is reported by reportFirebaseSignIn like any sign-in.
//
// Structurally typed: nothing is imported from firebase, so it adds no
// dependency. The modular SDK's listeners are functions, so the app passes
// the subscription in.
import { trackAuthEvent, type AuthMethod } from "../auth-events.js";
import { identityTracker, type IdentityTracker } from "./identity.js";

/** The part of a Firebase User the adapter reads. */
export interface FirebaseUserLike {
  uid: string;
  isAnonymous?: boolean;
}

/** The part of getAdditionalUserInfo(credential) the adapter reads. */
export interface FirebaseAdditionalUserInfoLike {
  isNewUser?: boolean;
  providerId?: string | null;
}

export interface FirebaseAuthTelemetryOptions {
  /** The provider name the events carry; "firebase" by default. */
  provider?: string;
}

// The tracker the last createFirebaseAuthTelemetry made, so
// reportFirebaseSignOut can name the user it identified.
let active: { tracker: IdentityTracker; provider: string } | undefined;

/**
 * Subscribe telemetry to Firebase's signed-in user, through the listener the
 * app passes: `(cb) => onIdTokenChanged(auth, cb)` (or onAuthStateChanged).
 * Returns the unsubscribe function. Never throws.
 */
export function createFirebaseAuthTelemetry(
  subscribe: (callback: (user: FirebaseUserLike | null) => void) => () => void,
  options?: FirebaseAuthTelemetryOptions,
): () => void {
  const provider = options?.provider ?? "firebase";
  const tracker = identityTracker(provider);
  active = { tracker, provider };
  try {
    const unsubscribe = subscribe((user) => {
      try {
        if (!user?.uid || user.isAnonymous === true) {
          // Nobody, or an anonymous visitor: cleared, not signed out (an
          // explicit sign-out is reportFirebaseSignOut's).
          if (tracker.current() !== null) {
            tracker.signedOut(false);
          }
          return;
        }
        if (tracker.current() !== user.uid) {
          tracker.signedIn(user.uid);
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

/**
 * Report a completed sign-in: auth_signed_up when Firebase says the user is
 * new (getAdditionalUserInfo(credential).isNewUser), else auth_signed_in
 * {credential}. Anonymous users report nothing. Never throws.
 */
export function reportFirebaseSignIn(
  user: FirebaseUserLike | null | undefined,
  info?: FirebaseAdditionalUserInfoLike | null,
  options?: FirebaseAuthTelemetryOptions,
): void {
  try {
    if (!user?.uid || user.isAnonymous === true) {
      return;
    }
    const { method, socialProvider } = firebaseMethod(info?.providerId);
    trackAuthEvent(info?.isNewUser === true ? "signed_up" : "signed_in", {
      userId: user.uid,
      provider: options?.provider ?? active?.provider ?? "firebase",
      method,
      socialProvider,
    });
  } catch {
    // Telemetry must never break the app.
  }
}

/**
 * Report auth_signed_out for the user the telemetry subscription identified,
 * then clear the id. Call it before signOut(auth). Never throws.
 */
export function reportFirebaseSignOut(): void {
  try {
    active?.tracker.signedOut(true);
  } catch {
    // Telemetry must never break the app.
  }
}

/** Map a Firebase provider id onto the auth method vocabulary. */
export function firebaseMethod(providerId: string | null | undefined): {
  method?: AuthMethod;
  socialProvider?: string;
} {
  const id = (providerId ?? "").toLowerCase();
  switch (id) {
    case "":
      return {};
    case "password":
      return { method: "password" };
    case "emaillink":
      return { method: "magic_link" };
    case "phone":
      return { method: "other" };
    case "google.com":
      return { method: "social", socialProvider: "google" };
    case "github.com":
      return { method: "social", socialProvider: "github" };
    case "apple.com":
      return { method: "social", socialProvider: "apple" };
    case "microsoft.com":
      return { method: "social", socialProvider: "microsoft" };
  }
  if (id.startsWith("saml.") || id.startsWith("oidc.")) {
    return { method: "sso" };
  }
  return { method: "social", socialProvider: "other" };
}
