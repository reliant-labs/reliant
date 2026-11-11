// Copyright (c) 2025 Reliant Labs

/**
 * Takes an org invitation link's token out of the URL at boot, beside
 * claimLinkBoot and for the same reason: before the router is created and
 * before any telemetry SDK can stamp the URL on a span. A bare import; see
 * lib/inviteLink.ts for what and why.
 */
import { captureInviteLinkFromUrl } from "./inviteLink";

captureInviteLinkFromUrl();

// A second link pasted into a tab already on /accept-invite changes only the
// fragment, so nothing reloads and the boot capture never sees it. Capture it
// here, and start over when it is a different invitation.
export function onInviteHashChange(restart: () => void): void {
  if (captureInviteLinkFromUrl()) restart();
}

window.addEventListener("hashchange", () => onInviteHashChange(() => window.location.reload()));
