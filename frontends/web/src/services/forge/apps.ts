// Copyright (c) 2025 Reliant Labs

/**
 * APPS — what a user has built and deployed, as one list.
 *
 * An app is a FORGE PROJECT (forge.yaml `name`), which is already the identity
 * the control plane keys every environment by: (org, project, env). Nothing
 * here invents a new record. The list is the union of two sources the web
 * already reads:
 *
 *   the control plane   every environment in the org (ListEnvironments with
 *                       no project filter), grouped by project, with each
 *                       environment's live URLs from GetStatus.
 *   Reliant projects    a project whose checkout is a forge project but which
 *                       has never been deployed: an app that is not live yet.
 *
 * Plain functions over the cloudEnvs.ts shapes, so the Apps page, the sidebar,
 * the home view and a chat's deploy card all read one model, and the tests
 * build it from object literals.
 */

import type { CloudEnv, CloudEnvStatus } from "./cloudEnvs";
import {
  hostedVerdictOf,
  workloadCustomDomain,
  workloadOpenUrl,
  type ForgeHostedWorkload,
  type HostedVerdict,
} from "./topology";

/**
 * One site a person can open: a frontend (a static site), at its custom
 * domain when that is live and healthy, else at its generated address.
 */
export interface AppLink {
  workload: string;
  url: string;
  /** The URL without its scheme or trailing slash, for display. */
  host: string;
  /** `url` is the tenant's own domain rather than the platform's generated hostname. */
  custom: boolean;
}

export interface AppEnv {
  id: string;
  name: string;
  preview: boolean;
  /** The worst of the environment's deployments, as the control plane judged it. */
  verdict: HostedVerdict;
  /** The release live there, "" before the first deploy. */
  release: string;
  /** When that release was promoted here: the last deploy. */
  deployedAt?: string;
  /** Its sites. Empty while the status loads, and for an environment with no frontend. */
  links: AppLink[];
  /** The status read has not answered yet. */
  loading: boolean;
}

export interface AppSummary {
  /** The forge project name — the app's identity, and its console's URL segment. */
  name: string;
  /** The Reliant project whose checkout this app is built from, when there is one. */
  projectId?: string;
  /** Long-lived environments, prod first. */
  envs: AppEnv[];
  /** Preview environments, counted rather than listed: one per open pull request. */
  previews: number;
  /** The newest deploy across every environment. */
  lastDeployedAt?: string;
}

/** The Reliant project fields the app list reads. */
export interface AppProjectRow {
  id: string;
  is_forge: boolean;
  forge_project_name?: string;
}

export function hostOf(url: string): string {
  return url.replace(/^[a-z]+:\/\//i, "").replace(/\/+$/, "");
}

/**
 * The sites a person can open in one environment, by workload name.
 *
 * Frontends only. An API is a backend's address for the frontend and other
 * programs to call, not a destination for a person — its URL lives on the
 * workload's page in the console. A database has no URL, and a site whose
 * address is not allocated yet has nothing to link to: it is left out rather
 * than linked to an empty string. Each link goes through workloadOpenUrl, so a
 * live custom domain wins over the generated hostname here exactly as it does
 * everywhere else.
 */
export function appLinksOf(workloads: ForgeHostedWorkload[]): AppLink[] {
  const links: AppLink[] = [];
  for (const workload of workloads) {
    if (workload.tier !== "static") continue;
    const url = workloadOpenUrl(workload);
    if (!url) continue;
    links.push({ workload: workload.name ?? "", url, host: hostOf(url), custom: workloadCustomDomain(workload) !== "" });
  }
  return links.sort((a, b) => a.workload.localeCompare(b.workload));
}

const ENV_RANK: Record<string, number> = { prod: 0, production: 0, staging: 1 };

/** prod, then staging, then the rest by name. */
export function compareEnvNames(a: string, b: string): number {
  return (ENV_RANK[a] ?? 2) - (ENV_RANK[b] ?? 2) || a.localeCompare(b);
}

/**
 * One environment's status as the list reads it: the answer, `null` when the
 * read failed (nothing to show, but nothing pending either), or `undefined`
 * while it is still loading.
 */
export type AppEnvStatus = CloudEnvStatus | null | undefined;

function toAppEnv(env: CloudEnv, status: AppEnvStatus): AppEnv {
  return {
    id: env.id,
    name: env.name,
    preview: env.kind === "preview",
    verdict: status?.verdict ?? hostedVerdictOf(undefined),
    release: status?.currentPromotion?.releaseVersion ?? "",
    deployedAt: status?.currentPromotion?.createdAt,
    links: status ? appLinksOf(status.workloads) : [],
    loading: status === undefined,
  };
}

/**
 * The environments Reliant runs, and so can link to: persistent and preview.
 * A local environment runs on a developer's machine under `forge env up` and
 * has no public URL; an unknown kind is not claimed as hosted.
 */
export function isHostedEnv(env: CloudEnv): boolean {
  return env.kind === "persistent" || env.kind === "preview";
}

/**
 * Every app: the org's hosted environments grouped by forge project, plus the
 * forge projects among `projects` that have none yet.
 *
 * Ordered by the newest deploy, so what the user just shipped leads; apps that
 * were never deployed follow, by name.
 */
export function buildApps(
  envs: CloudEnv[],
  statuses: ReadonlyMap<string, AppEnvStatus>,
  projects: AppProjectRow[]
): AppSummary[] {
  const byName = new Map<string, AppSummary>();
  const app = (name: string) => {
    let found = byName.get(name);
    if (!found) {
      found = { name, envs: [], previews: 0 };
      byName.set(name, found);
    }
    return found;
  };

  for (const env of envs) {
    if (!env.project || !isHostedEnv(env)) continue;
    const summary = app(env.project);
    const appEnv = toAppEnv(env, statuses.get(env.id));
    if (appEnv.preview) summary.previews += 1;
    else summary.envs.push(appEnv);
    if (appEnv.deployedAt && (!summary.lastDeployedAt || appEnv.deployedAt > summary.lastDeployedAt)) {
      summary.lastDeployedAt = appEnv.deployedAt;
    }
  }

  for (const project of projects) {
    const name = (project.forge_project_name ?? "").trim();
    if (!project.is_forge || !name) continue;
    const summary = app(name);
    summary.projectId ??= project.id;
  }

  for (const summary of byName.values()) summary.envs.sort((a, b) => compareEnvNames(a.name, b.name));

  return [...byName.values()].sort((a, b) => {
    if (a.lastDeployedAt && b.lastDeployedAt) return b.lastDeployedAt.localeCompare(a.lastDeployedAt);
    if (a.lastDeployedAt) return -1;
    if (b.lastDeployedAt) return 1;
    return a.name.localeCompare(b.name);
  });
}

/** The one link that stands for the app: prod's site, else the first site in any environment. */
export function primaryLink(app: Pick<AppSummary, "envs">): AppLink | undefined {
  return app.envs.flatMap((env) => env.links)[0];
}

/** Whether the app has a site a person can open right now. */
export function isLive(app: Pick<AppSummary, "envs">): boolean {
  return app.envs.some((env) => env.links.length > 0);
}

/** Whether any long-lived environment has had a release promoted to it. */
export function isDeployed(app: Pick<AppSummary, "envs" | "lastDeployedAt">): boolean {
  return !!app.lastDeployedAt || app.envs.some((env) => env.release !== "");
}

// ── A deploy in a chat ──────────────────────────────────────────────────────

/** Flags of `forge env deploy|promote` that take a value as the next word. */
const VALUE_FLAGS = new Set(["--target", "-t", "--release", "--version", "--to", "--from", "--project", "--dir", "-C", "--config", "--timeout"]);

/**
 * The environment a shell command deployed to, when it is a forge deploy:
 * `forge env deploy <env>` (with or without the `reliant` launcher), or
 * `forge env promote --to <env>`. Null for anything else.
 *
 * Read from the command an agent ran rather than from its output: the output
 * is forge's prose and free to change, while the command line is the
 * documented interface. What the deploy PRODUCED — its URLs, its health — is
 * then read from the control plane, never parsed out of a log.
 */
export function deployedEnvOf(command: string): string | null {
  // Each `&&`/`;`/`|` segment is its own command; the last deploy in the line wins.
  let found: string | null = null;
  for (const segment of command.split(/&&|\|\||;|\||\n/)) {
    const words = segment.trim().split(/\s+/);
    const at = words.findIndex((word, i) => word === "forge" && words[i + 1] === "env" && (words[i + 2] === "deploy" || words[i + 2] === "promote"));
    if (at < 0) continue;
    const verb = words[at + 2];
    const rest = words.slice(at + 3);
    const env = verb === "promote" ? flagValue(rest, "--to") : firstPositional(rest);
    if (env) found = env;
  }
  return found;
}

function flagValue(words: string[], flag: string): string | null {
  for (let i = 0; i < words.length; i++) {
    if (words[i] === flag) return clean(words[i + 1]);
    if (words[i].startsWith(`${flag}=`)) return clean(words[i].slice(flag.length + 1));
  }
  return null;
}

function firstPositional(words: string[]): string | null {
  for (let i = 0; i < words.length; i++) {
    const word = words[i];
    if (word.startsWith("-")) {
      if (!word.includes("=") && VALUE_FLAGS.has(word)) i++;
      continue;
    }
    return clean(word);
  }
  return null;
}

function clean(word: string | undefined): string | null {
  const value = (word ?? "").replace(/^['"]|['"]$/g, "");
  return /^[A-Za-z0-9][\w.-]*$/.test(value) ? value : null;
}
