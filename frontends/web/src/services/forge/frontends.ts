// Copyright (c) 2025 Reliant Labs

/**
 * FRONTENDS — what a user can open in a browser, per environment.
 *
 * A frontend is a deployment on the control plane's STATIC tier. It has a live
 * URL as soon as the platform allocates its hostname (the default
 * `<word-pair>.sites.<platform domain>`), whether or not a custom domain is
 * bound: GetStatus reports it as `observed.url`. That URL is read here, never
 * composed — the scheme and port are the environment's to spell (dev serves
 * plain HTTP). Custom domains come from the org's domain list, by binding.
 * The address a row OPENS is workloadOpenUrl's — the custom domain once it is
 * live and healthy — so the console and the Apps page link to the same place.
 *
 * Pure functions over shapes the console already holds, so the environment
 * page and the Overview read one model and the tests build it from literals.
 */

import { compareEnvNames, hostOf } from "./apps";
import type { DomainState, ForgeDomain } from "./domains";
import { hostedVerdictOf, workloadLink, workloadOpenUrl, type ForgeHostedWorkload, type HostedVerdict } from "./topology";

/** A custom domain bound to a frontend, and where it is in acquisition. */
export interface FrontendDomain {
  hostname: string;
  state: DomainState;
  /** Openable only once the domain serves over HTTPS; null before that. */
  url: string | null;
}

export interface Frontend {
  env: string;
  name: string;
  /**
   * Where the site is opened (workloadOpenUrl): its custom domain once that
   * is live and healthy, else the platform URL. "" when it has neither.
   */
  url: string;
  /** `url` without scheme or trailing slash, for display. */
  host: string;
  /** The default platform URL, "" until the platform has allocated a hostname. Equal to `url` without a live custom domain. */
  platformUrl: string;
  /** `platformUrl` without scheme or trailing slash. */
  platformHost: string;
  /** The release live in this environment — a site is promoted with its environment. */
  release: string;
  verdict: HostedVerdict;
  /** Stopped by its owner or held by billing: the site is not serving. */
  stopped: boolean;
  /** The custom domains that serve it; null when the org's domains could not be read. */
  domains: FrontendDomain[] | null;
}

export interface FrontendEnv {
  id: string;
  name: string;
  release: string;
}

/**
 * The frontends among one environment's workloads, by name.
 *
 * Only the static tier: a backend with a URL is an API (appLinksOf), and a
 * database has none. A deleted deployment is gone, not a frontend. A redirect
 * binding (www → apex) serves nothing of its own, so it is not listed as one
 * of the site's domains.
 */
export function frontendsOf(
  env: FrontendEnv,
  workloads: readonly ForgeHostedWorkload[],
  domains: readonly ForgeDomain[] | null
): Frontend[] {
  const out: Frontend[] = [];
  for (const workload of workloads) {
    const name = (workload.name ?? "").trim();
    if (name === "" || (workload.tier ?? "").trim().toLowerCase() !== "static") continue;
    if ((workload.observed_state ?? "").trim().toLowerCase() === "deleted") continue;
    const url = workloadOpenUrl(workload);
    const platformUrl = workloadLink(workload);
    out.push({
      env: env.name,
      name,
      url,
      host: url === "" ? "" : hostOf(url),
      platformUrl,
      platformHost: platformUrl === "" ? "" : hostOf(platformUrl),
      release: env.release,
      verdict: hostedVerdictOf(workload.verdict),
      stopped: workload.observed_state === "suspended" || workload.declared_run_state === "suspended",
      domains:
        domains === null
          ? null
          : domains
              .filter(
                (domain) =>
                  domain.binding?.environmentId === env.id && domain.binding.target === name && !domain.binding.redirectTo
              )
              .map((domain) => ({
                hostname: domain.hostname,
                state: domain.state,
                url: domain.state === "live" ? `https://${domain.hostname}` : null,
              }))
              .sort((a, b) => a.hostname.localeCompare(b.hostname)),
    });
  }
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

/** An environment as the Overview's strip holds it. */
export interface ScopeEnv extends FrontendEnv {
  preview: boolean;
  /** Reliant runs it. Only those have a platform hostname. */
  placed: boolean;
}

/**
 * The frontends of the environments a console scope covers: the one named, or
 * every long-lived one for "all" (previews are one per pull request and would
 * bury the real sites). Prod leads.
 */
export function frontendsInScope(
  scope: string,
  envs: readonly ScopeEnv[],
  hosted: ReadonlyMap<string, ReadonlyMap<string, ForgeHostedWorkload>>,
  domains: readonly ForgeDomain[] | null
): Frontend[] {
  const covered = envs.filter((env) => env.placed && (scope === "all" ? !env.preview : env.name === scope));
  return covered
    .sort((a, b) => compareEnvNames(a.name, b.name))
    .flatMap((env) => frontendsOf(env, [...(hosted.get(env.name)?.values() ?? [])], domains));
}
