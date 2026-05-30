/**
 * Cloud implementation of the git service. Uses typed Connect-Web clients
 * against `controlplane.v1.GitCredentialService` (see
 * web/src/gen/controlplane/v1/public/git_credential_service_pb.ts).
 *
 * Connect's protojson is camelCase-native — request fields use the generated
 * TS message shapes and snake_case duplicates are not sent.
 */

import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { ConnectError } from "@connectrpc/connect";
import { getControlPlaneClient } from "../client";
import { CONTROL_PLANE_API_URL } from "../config";
import { GitCredentialService } from "@/gen/controlplane/v1/public/git_credential_service_pb";
import type {
  CloneRepoArgs,
  GitAccount,
  GitCredentialStatus,
  GitRepo,
  ListGitReposPage,
} from "./types";

function timestampToISO(ts: Timestamp | undefined): string | undefined {
  if (!ts) return undefined;
  try {
    return timestampDate(ts).toISOString();
  } catch {
    return undefined;
  }
}

export async function getCredential(
  provider: string,
): Promise<GitCredentialStatus> {
  try {
    const res = await getControlPlaneClient(GitCredentialService).getGitCredential({
      provider,
    });
    return {
      available: true,
      hasToken: res.hasToken,
      provider: res.provider || provider,
      scopes: res.scopes ?? "",
      createdAt: timestampToISO(res.createdAt),
      updatedAt: timestampToISO(res.updatedAt),
    };
  } catch (err) {
    // NotFound bubbles up as a credential-missing signal; rethrow so callers
    // (hooks/useGitHubCredential, etc.) can branch on the ConnectError code.
    if (err instanceof ConnectError) throw err;
    throw err;
  }
}

export async function saveCredential(
  provider: string,
  accessToken: string,
  scopes: string,
): Promise<void> {
  await getControlPlaneClient(GitCredentialService).saveGitCredential({
    provider,
    accessToken,
    scopes,
  });
}

export async function deleteCredential(provider: string): Promise<void> {
  await getControlPlaneClient(GitCredentialService).deleteGitCredential({
    provider,
  });
}

// listCredentials isn't a real cloud RPC — credentials are keyed by provider
// and looked up individually via getCredential. Kept as a stub for parity
// with local.ts (the dispatch in index.ts requires structural matching).
export async function listCredentials(
  _provider?: string,
): Promise<{ accounts: GitAccount[] }> {
  return { accounts: [] };
}

export function getOAuthURL(): string | null {
  if (!CONTROL_PLANE_API_URL) return null;
  return `${CONTROL_PLANE_API_URL}/auth/github/authorize`;
}

export async function cloneRepo(
  args: CloneRepoArgs,
): Promise<{ clonedPath: string }> {
  const res = await getControlPlaneClient(GitCredentialService).cloneRepo({
    daemonId: args.daemonId,
    gitRepo: args.gitRepo,
    gitBranch: args.gitBranch,
    path: args.path,
  });
  return { clonedPath: res.clonedPath };
}

export async function listRepos(
  page = 1,
  perPage = 20,
  sort = "updated",
): Promise<ListGitReposPage> {
  const res = await getControlPlaneClient(GitCredentialService).listGitRepos({
    provider: "github",
    page,
    perPage,
    sort,
  });
  const repos: GitRepo[] = res.repos.map((r) => ({
    fullName: r.fullName,
    cloneUrl: r.cloneUrl,
    defaultBranch: r.defaultBranch,
    description: r.description,
    private: r.private,
    language: r.language,
    updatedAt: timestampToISO(r.updatedAt) ?? "",
  }));
  return { repos, hasMore: res.hasMore };
}
