// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY proving harness for the Overview and the workload page. NOT a
 * product surface.
 *
 * The real OverviewHome and WorkloadDetail, inside the real console chrome
 * (ConsolePreviewShell), against the workload fixtures (workloadFixtures.ts),
 * the release ledger and the observability fixtures — unauthenticated, no
 * backend — so every state can be looked at in every colour scheme and
 * screenshotted. All state is in the URL, so a screenshot is a link:
 *
 *   /forge-workloads-preview                                   the Overview, scoped to prod
 *   /forge-workloads-preview?env=all                           every environment
 *   /forge-workloads-preview?workload=reliant-temporal-worker  a workload (in the scoped env)
 *   …&tab=logs|traces|profiles|events|history|config
 *   …&case=empty|loading|error|not-set-up|self-managed|local|no-default-branch|action-fails
 *   …&dark=1&scheme=pure-black
 */

import { useEffect, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ReleasesApiProvider } from "@/services/forge/ReleasesApiContext";
import { RepoMetaProvider } from "@/services/forge/RepoMetaContext";

import { ConsolePreviewShell } from "../../Console/ConsolePreviewShell";
import { withSearch } from "../../Console/paths";
import { createReleasesFixtureApi } from "../../Releases/__fixtures__/releaseFixtures";
import { OverviewHome, type OverviewSearch } from "../OverviewHome";
import { WorkloadDetail, type WorkloadActions, type WorkloadDetailSearch } from "../WorkloadDetail";
import { actionsUnavailableReason } from "../WorkloadPages";
import {
  FIXTURE_HOSTED,
  STRIP_ENVS,
  WORKLOADS_PROJECT,
  createRepoMetaFixture,
  createWorkloadsFixtureApi,
  fixtureHostedObservation,
  type WorkloadsFixtureState,
} from "../__fixtures__/workloadFixtures";

const BASE = "/forge-workloads-preview";

const SCHEMES = [
  "professional-blue",
  "refined-neutral",
  "modern-teal",
  "slate",
  "forest",
  "vibrant-pink",
  "energetic-orange",
  "bold-red",
  "purple-classic",
  "pure-black",
] as const;

const CASES: { id: string; label: string; state: WorkloadsFixtureState }[] = [
  { id: "data", label: "the fixtures", state: "data" },
  { id: "empty", label: "nothing reported", state: "empty" },
  { id: "loading", label: "loading", state: "loading" },
  { id: "error", label: "telemetry unavailable", state: "error" },
  { id: "not-set-up", label: "observability not set up", state: "not-set-up" },
  { id: "self-managed", label: "self-managed env (no restart)", state: "data" },
  { id: "local", label: "local env (no restart)", state: "data" },
  { id: "no-default-branch", label: "repo default branch unknown", state: "data" },
  { id: "action-fails", label: "restart / stop refused", state: "data" },
  { id: "no-envs", label: "nothing deployed", state: "data" },
];

type PreviewSearch = OverviewSearch &
  WorkloadDetailSearch & { workload?: string; env?: string; case?: string; dark?: boolean | string | number; scheme?: string };

const CONTROL = "rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink";

export default function WorkloadsPreview() {
  const search = useSearch({ strict: false }) as PreviewSearch;
  const navigate = useNavigate();
  const env = search.env ?? "prod";
  const caseId = search.case ?? "data";
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0]!;
  // The router parses `dark=1` as JSON, so it arrives as a number.
  const dark = !!search.dark && String(search.dark) !== "0" && String(search.dark) !== "false";
  const scheme = search.scheme ?? "professional-blue";

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.setAttribute("data-color-scheme", scheme);
  }, [dark, scheme]);

  // A fresh client and fixtures per case, so one case's answers never leak into the next.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const queryClient = useMemo(() => new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } }), [caseId]);
  const obsApi = useMemo(() => createWorkloadsFixtureApi(active.state), [active]);
  const releasesApi = useMemo(() => createReleasesFixtureApi(), []);
  const envs = active.id === "no-envs" ? [] : STRIP_ENVS;

  const go = (next: Partial<PreviewSearch>, replace = true) =>
    void navigate({ to: BASE, search: (prev: Record<string, unknown>) => ({ ...prev, ...next }), replace });
  const keep = { case: search.case, dark: search.dark, scheme: search.scheme };
  const href = (extra: Record<string, unknown>) => withSearch(BASE, { env, ...keep, ...extra });

  const workloadEnv = STRIP_ENVS.find((candidate) => candidate.name === env) ?? STRIP_ENVS[0]!;
  const refuse = () => Promise.reject(new ConnectError("the caller may not write deployments in this organization", Code.PermissionDenied));
  const later = <T,>(value: T) => new Promise<T>((resolve) => setTimeout(() => resolve(value), 600));
  const kind = active.id === "self-managed" ? "self_managed" : active.id === "local" ? "local" : workloadEnv.placed ? "persistent" : "self_managed";
  const actions: WorkloadActions = {
    onRestart: () =>
      active.id === "action-fails"
        ? refuse()
        : later({ environmentId: workloadEnv.id, environmentName: workloadEnv.name, workload: search.workload ?? "", requestedAt: new Date().toISOString() }),
    onSetRunState: () => (active.id === "action-fails" ? refuse() : later(undefined)),
    unavailableReason: actionsUnavailableReason(kind, workloadEnv.name),
  };
  const repoMeta = useMemo(() => createRepoMetaFixture(active.id === "no-default-branch" ? null : "main"), [active]);

  return (
    <ConsolePreviewShell
      page={search.workload ? { kind: "workload", env: workloadEnv.name, name: search.workload } : { kind: "overview" }}
      env={env}
      onEnvChange={(next) => go({ env: next })}
      project={WORKLOADS_PROJECT}
      envs={STRIP_ENVS.map((candidate) => ({ name: candidate.name, preview: candidate.preview }))}
      controls={
        <>
          <select data-testid="preview-case" value={caseId} onChange={(e) => go({ case: e.target.value === "data" ? undefined : e.target.value })} className={CONTROL}>
            {CASES.map((c) => (
              <option key={c.id} value={c.id}>
                {c.label}
              </option>
            ))}
          </select>
          <select data-testid="preview-scheme" value={scheme} onChange={(e) => go({ scheme: e.target.value })} className={CONTROL}>
            {SCHEMES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
          <button type="button" data-testid="preview-dark" onClick={() => go({ dark: dark ? undefined : "1" })} className={CONTROL}>
            {dark ? "Dark" : "Light"}
          </button>
        </>
      }
    >
      <QueryClientProvider client={queryClient}>
        <ObservabilityApiProvider api={obsApi}>
          <ReleasesApiProvider api={releasesApi}>
            <RepoMetaProvider api={repoMeta}>
            {search.workload ? (
              <WorkloadDetail
                key={`${caseId}-${workloadEnv.name}-${search.workload}`}
                project={WORKLOADS_PROJECT}
                env={{ id: workloadEnv.id, name: workloadEnv.name }}
                name={search.workload}
                search={{ tab: search.tab, range: search.range, q: search.q, severity: search.severity, trace: search.trace, span: search.span, profileType: search.profileType }}
                onSearch={(next) => go({ tab: undefined, range: undefined, q: undefined, severity: undefined, trace: undefined, span: undefined, profileType: undefined, ...next }, false)}
                hrefs={{
                  overview: href({}),
                  release: (version) => withSearch("/forge-releases-preview", { version, dark: search.dark, scheme: search.scheme }),
                  activity: href({}),
                  dashboards: withSearch("/forge-dashboards-preview", { dark: search.dark, scheme: search.scheme }),
                }}
                actions={actions}
                hosted={kind === "persistent" ? fixtureHostedObservation(workloadEnv.name, search.workload) : null}
                declaredInRender={!workloadEnv.placed && workloadEnv.declared.includes(search.workload)}
              />
            ) : (
              <OverviewHome
                key={caseId}
                project={WORKLOADS_PROJECT}
                env={env}
                envs={envs}
                envsLoading={false}
                search={{ q: search.q, status: search.status, kind: search.kind, range: search.range }}
                onSearch={(next) => go(next)}
                hrefs={{
                  workload: (name, envName) => href({ workload: name, env: envName }),
                  release: (version) => withSearch("/forge-releases-preview", { version, dark: search.dark, scheme: search.scheme }),
                  releases: withSearch("/forge-releases-preview", { dark: search.dark, scheme: search.scheme }),
                  alerts: href({}),
                  activity: href({}),
                }}
                onSelectEnv={(next) => go({ env: next })}
                navigateTo={(target) => void navigate({ href: target })}
                hosted={FIXTURE_HOSTED}
              />
            )}
            </RepoMetaProvider>
          </ReleasesApiProvider>
        </ObservabilityApiProvider>
      </QueryClientProvider>
    </ConsolePreviewShell>
  );
}
