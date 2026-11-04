// Copyright (c) 2025 Reliant Labs

/**
 * Takes a claim link's token out of the URL as the FIRST thing the app does
 * after the config guard — before the router is created (it reads the location
 * when it is constructed) and before any telemetry SDK can stamp the URL on a
 * span. A bare import, for the same reason as browser-log-boot: imports
 * evaluate in order, and a call in main.tsx's body would run after every one of
 * them. See lib/claimLink.ts for what and why.
 */
import { captureClaimLinkFromUrl } from "./claimLink";

captureClaimLinkFromUrl();

// A second link pasted into a tab already on /claim changes only the fragment:
// no reload, so the boot capture above never sees it. Capture it here, and
// start over when it is a different claim, because the page decided what to
// show from the first one.
export function onClaimHashChange(restart: () => void): void {
  if (captureClaimLinkFromUrl()) restart();
}

window.addEventListener("hashchange", () => onClaimHashChange(() => window.location.reload()));
