// Copyright (c) 2025 Reliant Labs

/**
 * What a refused GitHub App installation rpc means, in words (control-plane
 * OrganizationService's StartGitHubAppInstall, ListLinkableGitHubAppInstallations,
 * LinkGitHubAppInstallation and UnlinkGitHubAppInstallation;
 * internal/handlers/organization/github_app.go). Read from the error's stable
 * reason, never its prose.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { errorReason, type ErrorReason } from "@/lib/errorReasons";

/** Whether a refusal is fixed by connecting (or reconnecting) the caller's own GitHub account. */
export function needsGitHubConnection(err: unknown): boolean {
  const reason = errorReason(err);
  return reason === "git_credential_not_connected" || reason === "git_credential_needs_reconnect";
}

/** The refusal in words; `orgName` names the org the page is for. */
export function githubLinkErrorMessage(err: unknown, orgName: string, fallback: string): string {
  const copy: Partial<Record<ErrorReason, string>> = {
    github_install_state_invalid: "This link expired or was started by someone else. Start again from this page.",
    admin_required: `Only an owner or admin of ${orgName} can link or unlink GitHub App installations.`,
    not_org_member: `You're not a member of ${orgName}.`,
    human_only: "Only a person signed in to Reliant can do this, not an access token.",
    github_installation_not_visible:
      "Your GitHub account can't see that installation. Ask an owner of the GitHub account it's installed on to link it, or to give you access there.",
    github_installation_linked_elsewhere: "That installation is linked to another Reliant organization. Unlink it there first.",
    git_credential_not_connected: "Connect your GitHub account first: Reliant checks that your own account can see an installation before linking it.",
    git_credential_needs_reconnect: "GitHub rejected your saved sign-in. Reconnect GitHub, then try again.",
    github_app_not_configured: "This deployment has no Reliant GitHub App to link.",
  };
  const reason = errorReason(err) as ErrorReason | null;
  if (reason && copy[reason]) return copy[reason];
  if (err instanceof ConnectError) {
    if (err.code === Code.NotFound) return "That installation is no longer linked. It may have been unlinked in another window.";
    if (err.code === Code.Unavailable) return "GitHub couldn't be reached. Try again in a moment.";
    if (err.code === Code.ResourceExhausted) return "GitHub's rate limit is spent for now. Try again in a few minutes.";
  }
  return fallback;
}
