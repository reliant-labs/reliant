// Copyright (c) 2025 Reliant Labs

import type { GetGitCredentialResponse } from "@/gen/controlplane/services/git_credential/v1/git_credential_pb";
import { isGitHubUserId } from "@/lib/onlyFromFilter";

/**
 * The GitHub user id of a git credential's account — "Only from: Me" on
 * GitHub for a hosted account.
 *
 * control-plane reads it from the same GET /user answer that fills
 * account_login (gitcredential.decorateWithGitHubIdentity) and reports it as
 * GetGitCredentialResponse.account_id. It is empty when control-plane could
 * not resolve the account (GitHub unreachable, token dead); anything that is
 * not a real GitHub user id is treated the same, so "Me" is never offered
 * from a value no trigger.sender.id can carry.
 */
export function readAccountId(res: Pick<GetGitCredentialResponse, "accountId">): string | undefined {
  return isGitHubUserId(res.accountId) ? res.accountId : undefined;
}
