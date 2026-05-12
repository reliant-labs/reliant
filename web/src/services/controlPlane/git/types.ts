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
  cloneRepo(
    daemonName: string,
    gitRepo: string,
    gitBranch: string,
    path: string,
    accountLogin?: string,
  ): Promise<{ clonedPath: string }>;
}
