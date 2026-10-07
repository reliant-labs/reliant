// Copyright (c) 2025 Reliant Labs

/**
 * The GitHub user id of a git credential's account, when control-plane
 * reports one — "Only from: Me" on GitHub for a hosted account.
 *
 * control-plane's GetGitCredentialResponse does not report it yet.
 * control-plane already reads GET /user to fill account_login
 * (gitcredential.decorateWithGitHubIdentity) and drops the `id` beside it; the
 * change it needs is `string account_id = 14;` on GetGitCredentialResponse,
 * set from that same answer (strconv.FormatInt(user.ID, 10)).
 *
 * This repo's copy of that contract is generated from control-plane's
 * (proto-vendor/controlplane, drift-gated from control-plane's CI), so the
 * field cannot be declared here first. Until it lands and is synced, the read
 * below finds nothing and "Me" on GitHub needs a saved GitHub connection
 * (hooks/useMySenderId). Once synced, the generated message carries
 * `accountId` and this picks it up with no change.
 */
export function readAccountId(res: object): string | undefined {
  const id = (res as { accountId?: unknown }).accountId;
  return typeof id === "string" && /^[1-9][0-9]*$/.test(id) ? id : undefined;
}
