/**
 * Local-only fallback for the git service. Used when no control plane is
 * configured. Returns structured "unavailable" responses so the UI can
 * render a clean "cloud feature" message instead of crashing.
 */

import type {
  CloneRepoArgs,
  GitAccount,
  GitCredentialStatus,
  ListGitReposPage,
} from "./types";

export async function getCredential(
  provider: string,
): Promise<GitCredentialStatus> {
  return {
    available: false,
    hasToken: false,
    provider,
    scopes: "",
  };
}

export async function saveCredential(
  _provider: string,
  _accessToken: string,
  _scopes: string,
): Promise<void> {
  throw new Error("Git credential storage requires a Reliant Cloud account");
}

export async function deleteCredential(_provider: string): Promise<void> {
  throw new Error("Git credential storage requires a Reliant Cloud account");
}

export async function listCredentials(
  _provider?: string,
): Promise<{ accounts: GitAccount[] }> {
  return { accounts: [] };
}

export function getOAuthURL(): string | null {
  return null;
}

export async function cloneRepo(
  _args: CloneRepoArgs,
): Promise<{ clonedPath: string }> {
  throw new Error("Git clone via control plane requires a Reliant Cloud account");
}

export async function listRepos(
  _page = 1,
  _perPage = 20,
  _sort = "updated",
): Promise<ListGitReposPage> {
  return { repos: [], hasMore: false };
}
