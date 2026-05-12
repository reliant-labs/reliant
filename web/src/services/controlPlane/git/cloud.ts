/**
 * Cloud implementation of the git service. Talks to the control-plane
 * `controlplane.v1.GitCredentialService` via Connect JSON.
 */

import { controlPlaneFetch } from "../client";
import { CONTROL_PLANE_API_URL } from "../config";
import type { GitCredentialStatus, GitAccount } from "./types";

const SVC = "controlplane.v1.GitCredentialService";

export async function getCredential(
  provider: string,
): Promise<GitCredentialStatus> {
  const res = await controlPlaneFetch(SVC, "GetGitCredential", {
    provider,
    account_login: "",
  });
  return {
    available: true,
    hasToken: Boolean(res.has_token),
    provider: res.provider ?? provider,
    scopes: res.scopes ?? "",
    createdAt: res.created_at,
    updatedAt: res.updated_at,
  };
}

export async function saveCredential(
  provider: string,
  accessToken: string,
  scopes: string,
): Promise<void> {
  await controlPlaneFetch(SVC, "SaveGitCredential", {
    provider,
    access_token: accessToken,
    scopes,
    account_login: "",
  });
}

export async function deleteCredential(provider: string): Promise<void> {
  await controlPlaneFetch(SVC, "DeleteGitCredential", {
    provider,
    account_login: "",
  });
}

export async function listCredentials(
  provider?: string,
): Promise<{ accounts: GitAccount[] }> {
  const res = await controlPlaneFetch(SVC, "ListGitCredentials", {
    provider: provider ?? "",
  });
  return { accounts: res.accounts ?? [] };
}

export function getOAuthURL(): string | null {
  if (!CONTROL_PLANE_API_URL) return null;
  return `${CONTROL_PLANE_API_URL}/auth/github/authorize`;
}

export async function cloneRepo(
  daemonName: string,
  gitRepo: string,
  gitBranch: string,
  path: string,
  accountLogin?: string,
): Promise<{ clonedPath: string }> {
  const res = await controlPlaneFetch(SVC, "CloneRepo", {
    daemon_name: daemonName,
    git_repo: gitRepo,
    git_branch: gitBranch,
    path,
    account_login: accountLogin ?? "",
  });
  return { clonedPath: res.cloned_path };
}
