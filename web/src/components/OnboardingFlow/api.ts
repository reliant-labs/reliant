/**
 * Control-plane API client for cloud onboarding steps.
 *
 * Uses plain fetch against Connect-Web JSON endpoints so we don't need
 * generated proto types. The Connect protocol sends/receives JSON by default:
 *   POST /rpc/controlplane.v1.SomeService/SomeMethod
 *   Content-Type: application/json
 */

import { supabase } from "@/lib/supabase";

// Control-plane API URL — set via env var when cloud mode is active
const CONTROL_PLANE_API_URL =
  import.meta.env.VITE_CONTROL_PLANE_API_URL || "";

let _baseUrl = CONTROL_PLANE_API_URL;
let _tokenGetter: () => Promise<string | null> = async () => {
  try {
    const { data: { session } } = await supabase.auth.getSession();
    return session?.access_token ?? null;
  } catch {
    return null;
  }
};

/**
 * Override the API base URL and token getter (used during initialization).
 */
export function initOnboardingApi(
  apiBaseUrl: string,
  getToken: () => Promise<string | null>,
) {
  _baseUrl = apiBaseUrl;
  _tokenGetter = getToken;
}

/**
 * Call a Connect-Web JSON unary RPC.
 */
async function callRPC<Req extends Record<string, unknown>, Res>(
  service: string,
  method: string,
  request: Req,
): Promise<Res> {
  const token = await _tokenGetter();

  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };
  if (token) {
    headers["Authorization"] = `Bearer ${token}`;
  }

  const resp = await fetch(`${_baseUrl}/${service}/${method}`, {
    method: "POST",
    headers,
    body: JSON.stringify(request),
  });

  if (!resp.ok) {
    let detail = "";
    try {
      const body = await resp.json();
      detail = body.message || body.msg || JSON.stringify(body);
    } catch {
      detail = resp.statusText;
    }
    throw new Error(detail || `RPC ${method} failed (${resp.status})`);
  }

  return resp.json() as Promise<Res>;
}

// ── Git Credential Service ──────────────────────────────────

const GIT_CREDENTIAL_SERVICE = "controlplane.v1.GitCredentialService";

export interface GitRepo {
  fullName: string;
  cloneUrl: string;
  defaultBranch: string;
  description: string;
  private: boolean;
  language: string;
  updatedAt: string;
}

export interface ListGitReposResponse {
  repos: GitRepo[];
  hasMore: boolean;
}

export interface SaveGitCredentialRequest {
  provider: string;
  accessToken: string;
  scopes: string;
}

export async function saveGitCredential(req: SaveGitCredentialRequest): Promise<void> {
  await callRPC<Record<string, unknown>, void>(GIT_CREDENTIAL_SERVICE, "SaveGitCredential", {
    provider: req.provider,
    accessToken: req.accessToken,
    scopes: req.scopes,
  });
}

export async function listGitRepos(
  page = 1,
  perPage = 20,
  sort = "updated",
): Promise<{ repos: GitRepo[]; hasMore: boolean }> {
  const res = await callRPC<Record<string, unknown>, ListGitReposResponse>(
    GIT_CREDENTIAL_SERVICE,
    "ListGitRepos",
    { provider: "github", page, perPage, sort },
  );
  return { repos: res.repos ?? [], hasMore: res.hasMore ?? false };
}

export async function cloneRepo(req: {
  daemonName: string;
  gitRepo: string;
  gitBranch: string;
  path: string;
}): Promise<{ clonedPath: string }> {
  const res = await callRPC<Record<string, unknown>, { cloned_path?: string; clonedPath?: string }>(
    GIT_CREDENTIAL_SERVICE,
    "CloneRepo",
    {
      daemonName: req.daemonName,
      daemon_name: req.daemonName,
      gitRepo: req.gitRepo,
      git_repo: req.gitRepo,
      gitBranch: req.gitBranch,
      git_branch: req.gitBranch,
      path: req.path,
    },
  );
  return { clonedPath: res.clonedPath ?? res.cloned_path ?? req.path };
}

// ── User Service ────────────────────────────────────────────

const USER_SERVICE = "controlplane.v1.UserService";

export interface ControlPlaneUser {
  onboardingCompleted?: boolean;
  freeCreditsEligible?: boolean;
  reliantCreditsEligible?: boolean;
  creditsEligible?: boolean;
  welcomeCreditEligible?: boolean;
  hasFreeCredits?: boolean;
  freeCreditsAvailable?: boolean;
  creditsAvailable?: boolean;
  trialCreditsRemaining?: number | string;
  freeCreditsRemaining?: number | string;
  creditsRemaining?: number | string;
  creditBalance?: number | string;
  globalBudgetAvailable?: boolean;
  budgetAvailable?: boolean;
  ipAllowed?: boolean;
  ipRestricted?: boolean;
}

interface GetCurrentUserResponse {
  user?: ControlPlaneUser;
}

function isPositiveNumber(value: unknown): boolean {
  if (typeof value === "number") return value > 0;
  if (typeof value === "string") return Number(value) > 0;
  return false;
}

export function hasReliantCreditEligibility(user: ControlPlaneUser | undefined): boolean {
  if (!user) return false;
  if (user.ipRestricted === true || user.ipAllowed === false) return false;
  if (user.globalBudgetAvailable === false || user.budgetAvailable === false) return false;

  return Boolean(
    user.freeCreditsEligible ||
      user.reliantCreditsEligible ||
      user.creditsEligible ||
      user.welcomeCreditEligible ||
      user.hasFreeCredits ||
      user.freeCreditsAvailable ||
      user.creditsAvailable ||
      isPositiveNumber(user.trialCreditsRemaining) ||
      isPositiveNumber(user.freeCreditsRemaining) ||
      isPositiveNumber(user.creditsRemaining) ||
      isPositiveNumber(user.creditBalance),
  );
}

let _userPromise: Promise<GetCurrentUserResponse> | null = null;
export function getCurrentUser(): Promise<GetCurrentUserResponse> {
  if (!_userPromise) {
    _userPromise = callRPC<Record<string, unknown>, GetCurrentUserResponse>(
      USER_SERVICE,
      "GetCurrentUser",
      {},
    ).finally(() => {
      setTimeout(() => {
        _userPromise = null;
      }, 30_000);
    });
  }
  return _userPromise;
}

export async function completeOnboardingRPC(
  onboardingData: Record<string, unknown>,
): Promise<void> {
  await callRPC(USER_SERVICE, "CompleteOnboarding", { onboardingData });
}

// ── Daemon Service ──────────────────────────────────────────

const DAEMON_SERVICE = "controlplane.v1.DaemonService";

export interface Daemon {
  daemonId?: string;
  daemon_id?: string;
  hostname?: string;
  // Control-plane uses `name` as the AIP resource name (e.g. "daemons/abc123").
  // OSS uses `daemon_id` / `daemonId`. Both JSON variants are covered.
  name?: string;
  status: number;
}

interface ListDaemonsResponse {
  daemons: Daemon[];
}

// Control-plane DaemonStatus.ACTIVE is 3; OSS DaemonRegistry DAEMON_STATUS_ACTIVE is 1.
const ACTIVE_DAEMON_STATUSES = new Set([1, 3]);

export async function createDaemon(req: {
  name: string;
  daemonType: number;
  size: number;
  gitRepo: string;
  gitBranch: string;
}): Promise<void> {
  await callRPC(DAEMON_SERVICE, "CreateDaemon", req);
}

export async function listDaemons(): Promise<{ daemons: Daemon[] }> {
  const res = await callRPC<Record<string, unknown>, ListDaemonsResponse>(
    DAEMON_SERVICE,
    "ListDaemons",
    {},
  );
  return { daemons: res.daemons ?? [] };
}

export function hasActiveDaemon(daemons: Daemon[]): boolean {
  return daemons.some((d) => ACTIVE_DAEMON_STATUSES.has(d.status));
}

export function getActiveDaemonName(daemons: Daemon[]): string {
  const daemon = daemons.find((d) => ACTIVE_DAEMON_STATUSES.has(d.status));
  if (!daemon) return "";
  return daemon.hostname || getDaemonId(daemon);
}

export async function resumeDaemon(daemonId: string): Promise<void> {
  if (!daemonId) throw new Error("Cannot resume daemon: no daemon ID available");
  await callRPC(DAEMON_SERVICE, "ResumeDaemon", { daemonId, daemon_id: daemonId, name: daemonId });
}

/** Extract a usable daemon identifier from any response shape. */
export function getDaemonId(daemon: Daemon): string {
  return daemon.daemonId || daemon.daemon_id || daemon.name || "";
}

export function getFirstDaemonId(daemons: Daemon[]): string {
  const d = daemons[0];
  if (!d) return "";
  return getDaemonId(d);
}

// ── LLM Gateway Service ─────────────────────────────────────

const LLM_GATEWAY_SERVICE = "controlplane.v1.LLMGatewayService";

export async function createLLMKey(req: {
  name: string;
  models: string[];
}): Promise<void> {
  await callRPC(LLM_GATEWAY_SERVICE, "CreateLLMKey", req);
}