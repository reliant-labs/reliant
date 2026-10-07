/** How the credential was obtained. Only a GitHub App token expires, and
 *  only the non-App kinds are really governed by their scope string. */
export type GitCredentialKind = "github_app" | "oauth_app" | "pat" | "unknown";

/** Whether the credential still works. "expired" and "needsReconnect" both
 *  require the user to act; "valid" covers a token that is renewable without
 *  them doing anything. */
export type GitCredentialHealth = "valid" | "expired" | "needsReconnect" | "unknown";

/** A GitHub App installation the connected account can reach. */
export interface GitAppInstallation {
  accountLogin: string;
  accountType: string;
  avatarUrl: string;
  /** Where the user manages which repositories this installation can see. */
  configureUrl: string;
  /** "all" when the installation covers every repo, else "selected". */
  repositorySelection: string;
}

export interface GitCredentialStatus {
  available: boolean; // false in local mode
  hasToken: boolean;
  provider: string;
  scopes: string;
  createdAt?: string;
  updatedAt?: string;

  /** The GitHub account the token belongs to. Empty when the provider could
   *  not be reached — the UI then shows health rather than an unverified name. */
  accountLogin?: string;
  /** GitHub's numeric user id for that account (digits, as a string): the id
   *  `trigger.sender.id` carries, so what "Only from: Me" stores. Unset until
   *  control-plane reports it — its GetGitCredentialResponse does not yet
   *  carry the id (see readAccountId in cloud.ts) — and whenever the login
   *  above is unset. */
  accountId?: string;
  accountAvatarUrl?: string;
  kind?: GitCredentialKind;
  health?: GitCredentialHealth;
  /** ISO timestamp; absent for credential kinds that never expire. */
  expiresAt?: string;
  installations?: GitAppInstallation[];

  /** GitHub's installation flow — where the user ADDS an account or widens a
   *  "selected repositories" installation. This is a different action from
   *  reconnecting: re-running OAuth re-authorizes the user and changes
   *  nothing about which accounts or repos the App can see.
   *
   *  Absent when the control plane has no App slug configured; callers must
   *  omit the affordance rather than render a dead link. */
  installUrl?: string;
  /** The App's URL slug, for callers building their own GitHub links. */
  appSlug?: string;
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
