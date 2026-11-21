// Copyright (c) 2025 Reliant Labs

/**
 * Takes a dev sign-in link's token (`#dev_sign_in=<token>`) out of the URL at
 * boot, before the router reads the location, and signs the browser in with
 * it (lib/devSignIn.ts). A bare import for the same reason as claimLinkBoot.
 *
 * Off in every production build: devSignInEnabled() is false unless this is
 * Vite's dev server started with VITE_DEV_SIGN_IN=true.
 */
import { adoptDevToken, devSignInEnabled, takeDevTokenFromUrl } from "./devSignIn";

export function bootDevSignIn(): Promise<void> | null {
  if (!devSignInEnabled()) return null;
  const token = takeDevTokenFromUrl();
  if (token === null) return null;
  return adoptDevToken(token, window.location.pathname + window.location.search).catch((error: unknown) => {
    console.error("[DevSignIn] The dev sign-in link was not used:", error instanceof Error ? error.message : error);
  });
}

void bootDevSignIn();
