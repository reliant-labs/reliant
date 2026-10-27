// Copyright (c) 2025 Reliant Labs

/**
 * Renders the Overview or a workload the way the console does — a query
 * client, the ObservabilityApi and ReleasesApi seams (fixtures), and URL state
 * held in component state so a test can click and assert on what changed.
 * Every test file mocks `@tanstack/react-router`'s useNavigate (forge-ui Link
 * needs it).
 */

import { useState, type ReactElement } from "react";
import { render, type RenderResult } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { vi } from "vitest";

import type { DomainsState } from "@/hooks/forge-domain-queries";
import { createTestQueryClient } from "@/test/renderWithQuery";
import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ReleasesApiProvider } from "@/services/forge/ReleasesApiContext";
import { RepoMetaProvider, type RepoMetaApi } from "@/services/forge/RepoMetaContext";

import { createReleasesFixtureApi } from "../../Releases/__fixtures__/releaseFixtures";
import { OverviewHome, type HostedByEnv, type OverviewSearch } from "../OverviewHome";
import { WorkloadDetail, type HostedObservation, type WorkloadActions, type WorkloadDetailSearch } from "../WorkloadDetail";
import type { StripEnv } from "../workloadModel";
import { FIXTURE_DOMAINS, FIXTURE_HOSTED, STRIP_ENVS, WORKLOADS_PROJECT, createRepoMetaFixture, createWorkloadsFixtureApi, fixtureHostedObservation } from "../__fixtures__/workloadFixtures";

export function WorkloadsProviders({ obs, repoMeta = createRepoMetaFixture(), children }: { obs: ObservabilityApi; repoMeta?: RepoMetaApi; children: ReactElement }) {
  return (
    <QueryClientProvider client={createTestQueryClient()}>
      <ObservabilityApiProvider api={obs}>
        <ReleasesApiProvider api={createReleasesFixtureApi()}>
          <RepoMetaProvider api={repoMeta}>{children}</RepoMetaProvider>
        </ReleasesApiProvider>
      </ObservabilityApiProvider>
    </QueryClientProvider>
  );
}

function wrap(obs: ObservabilityApi, ui: ReactElement, repoMeta?: RepoMetaApi): RenderResult {
  return render(
    <WorkloadsProviders obs={obs} repoMeta={repoMeta}>
      {ui}
    </WorkloadsProviders>
  );
}

export interface Harness<S> extends RenderResult {
  current: () => S;
}

export function renderOverview({
  env = "prod",
  envs = STRIP_ENVS,
  search = {},
  obs = createWorkloadsFixtureApi(),
  hosted = FIXTURE_HOSTED,
  hostedLoading = false,
  domains = { availability: "available", domains: FIXTURE_DOMAINS, detail: "" },
}: {
  env?: string;
  envs?: StripEnv[];
  search?: OverviewSearch;
  obs?: ObservabilityApi;
  hosted?: HostedByEnv;
  hostedLoading?: boolean;
  domains?: DomainsState;
} = {}): Harness<OverviewSearch> & { selectEnv: ReturnType<typeof vi.fn>; navigateTo: ReturnType<typeof vi.fn> } {
  let latest = search;
  const selectEnv = vi.fn();
  const navigateTo = vi.fn();
  function Stateful() {
    const [state, setState] = useState<OverviewSearch>(search);
    return (
      <OverviewHome
        project={WORKLOADS_PROJECT}
        env={env}
        envs={envs}
        envsLoading={false}
        search={state}
        onSearch={(next) =>
          setState((prev) => {
            latest = { ...prev, ...next };
            return latest;
          })
        }
        hrefs={{
          workload: (name, envName) => `/w/${envName}/${name}`,
          release: (version) => `/r/${version}`,
          releases: "/releases",
          alerts: "/alerts",
          activity: "/activity",
        }}
        onSelectEnv={selectEnv}
        navigateTo={navigateTo}
        hosted={hosted}
        hostedLoading={hostedLoading}
        domains={domains}
        domainsHref="/domains"
      />
    );
  }
  return { ...wrap(obs, <Stateful />), current: () => latest, selectEnv, navigateTo };
}

export function renderWorkload(
  name: string,
  {
    env = "prod",
    search = {},
    actions = {},
    hosted,
    obs = createWorkloadsFixtureApi(),
    repoMeta,
  }: {
    env?: string;
    search?: WorkloadDetailSearch;
    actions?: Partial<WorkloadActions>;
    /** Defaults to the fixture's GetStatus answer for the env (null for a self-managed one). */
    hosted?: HostedObservation | null;
    obs?: ObservabilityApi;
    repoMeta?: RepoMetaApi;
  } = {}
): Harness<WorkloadDetailSearch> & { actions: WorkloadActions } {
  let latest = search;
  const scoped = STRIP_ENVS.find((candidate) => candidate.name === env)!;
  const allActions: WorkloadActions = { onRestart: vi.fn(), onSetRunState: vi.fn().mockResolvedValue(undefined), unavailableReason: null, ...actions };
  const observation = hosted === undefined ? fixtureHostedObservation(env, name) : hosted;
  function Stateful() {
    const [state, setState] = useState<WorkloadDetailSearch>(search);
    return (
      <WorkloadDetail
        project={WORKLOADS_PROJECT}
        env={{ id: scoped.id, name: scoped.name }}
        name={name}
        search={state}
        onSearch={(next) => {
          latest = next;
          setState(next);
        }}
        hrefs={{ overview: "/overview", release: (version) => `/r/${version}`, activity: "/activity", dashboards: "/dashboards" }}
        actions={allActions}
        hosted={observation}
        declaredInRender={!scoped.placed && scoped.declared.includes(name)}
      />
    );
  }
  return { ...wrap(obs, <Stateful />, repoMeta), current: () => latest, actions: allActions };
}
