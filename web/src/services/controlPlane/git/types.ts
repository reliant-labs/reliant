export interface GitCredentialStatus {
  available: boolean; // false in local mode
  hasToken: boolean;
  provider: string;
  scopes: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface GitAccount {
  provider: string;
  account_login: string;
  scopes: string;
  created_at?: string;
  updated_at?: string;
}

export interface CloneRepoArgs {
  daemonId: string;
  gitRepo: string;
  gitBranch: string;
  path: string;
}

/**
 * UI-friendly repo descriptor returned by `gitService.listRepos`.
 *
 * `updatedAt` is normalized to an ISO string so the picker UI can render it
 * with `new Date(...)` without depending on the generated protobuf Timestamp
 * shape. The control-plane public proto omits any account-scoping field on
 * GitRepo today; if it comes back, add it here without reviving the
 * snake_case fallback shim.
 */
export interface GitRepo {
  fullName: string;
  cloneUrl: string;
  defaultBranch: string;
  description: string;
  private: boolean;
  language: string;
  updatedAt: string;
}

export interface ListGitReposPage {
  repos: GitRepo[];
  hasMore: boolean;
}

export interface GitService {
  getCredential(provider: string): Promise<GitCredentialStatus>;
  saveCredential(
    provider: string,
    accessToken: string,
    scopes: string,
  ): Promise<void>;
  deleteCredential(provider: string): Promise<void>;
  listCredentials(
    provider?: string,
  ): Promise<{ accounts: GitAccount[] }>;
  getOAuthURL(): string | null;
  cloneRepo(args: CloneRepoArgs): Promise<{ clonedPath: string }>;
  listRepos(
    page?: number,
    perPage?: number,
    sort?: string,
  ): Promise<ListGitReposPage>;
}
