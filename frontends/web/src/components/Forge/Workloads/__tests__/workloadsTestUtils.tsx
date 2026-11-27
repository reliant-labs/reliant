// Copyright (c) 2025 Reliant Labs

/**
 * Renders the Overview or a workload the way the console does — a query
 * client, the ObservabilityApi and ReleasesApi seams (fixtures), and URL state
 * held in component state so a test can click and assert on what changed.
 * Every test file mocks `@tanstack/react-router`'s useNavigate (forge-ui Link
 * needs it).
 */

import { useState, type ComponentProps, type ReactElement } from "react";
import { render, type RenderResult } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { vi } from "vitest";

import type { DomainsState } from "@/hooks/forge-domain-queries";
import { createTestQueryClient } from "@/test/renderWithQuery";
import { type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ObservabilityFixtures } from "@/test/observabilityFixtures";
import { ReleasesApiProvider } from "@/services/forge/ReleasesApiContext";
import { RepoMetaProvider, type RepoMetaApi } from "@/services/forge/RepoMetaContext";
import { VulnerabilityApiProvider, type VulnerabilityApi } from "@/services/controlPlane/vulnerabilities/VulnerabilityApiContext";

import { createReleasesFixtureApi } from "../../Releases/__fixtures__/releaseFixtures";
import { fixtureVulnerabilityApi } from "../../Security/fixtures";
import { OverviewHome, type HostedByEnv, type OverviewRollback, type OverviewSearch } from "../OverviewHome";
import { WorkloadDetail, type HostedObservation, type WorkloadActions, type WorkloadDetailSearch } from "../WorkloadDetail";
import type { StripEnv } from "../workloadModel";
import { FIXTURE_DOMAINS, FIXTURE_HOSTED, STRIP_ENVS, WORKLOADS_PROJECT, createRepoMetaFixture, createWorkloadsFixtureApi, fixtureHostedObservation } from "../__fixtures__/workloadFixtures";

export function WorkloadsProviders({
  obs,
  repoMeta = createRepoMetaFixture(),
  vulns = fixtureVulnerabilityApi("findings"),
  children,
}: {
  obs: ObservabilityApi;
  repoMeta?: RepoMetaApi;
  /** The Overview's Vulnerabilities card's reads. */
  vulns?: VulnerabilityApi;
  children: ReactElement;
}) {
  return (
    <QueryClientProvider client={createTestQueryClient()}>
      <ObservabilityFixtures api={obs}>
        <ReleasesApiProvider api={createReleasesFixtureApi()}>
          <VulnerabilityApiProvider api={vulns}>
            <RepoMetaProvider api={repoMeta}>{children}</RepoMetaProvider>
          </VulnerabilityApiProvider>
        </ReleasesApiProvider>
      </ObservabilityFixtures>
    </QueryClientProvider>
  );
}

function wrap(obs: ObservabilityApi, ui: ReactElement, repoMeta?: RepoMetaApi, vulns?: VulnerabilityApi): RenderResult {
  return render(
    <WorkloadsProviders obs={obs} repoMeta={repoMeta} vulns={vulns}>
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
  rollback,
  envsUnread,
  vulns,
}: {
  env?: string;
  envs?: StripEnv[];
  search?: OverviewSearch;
  obs?: ObservabilityApi;
  hosted?: HostedByEnv;
  hostedLoading?: boolean;
  domains?: DomainsState;
  rollback?: OverviewRollback;
  envsUnread?: ComponentProps<typeof OverviewHome>["envsUnread"];
  vulns?: VulnerabilityApi;
} = {}): Harness<OverviewSearch> & { navigateTo: ReturnType<typeof vi.fn> } {
  let latest = search;
  const navigateTo = vi.fn();
  function Stateful() {
    const [state, setState] = useState<OverviewSearch>(search);
    return (
      <OverviewHome
        project={WORKLOADS_PROJECT}
        env={env}
        envs={envs}
        envsLoading={false}
        envsUnread={envsUnread}
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
          scoped: (envName) => `/overview?env=${envName}`,
        }}
        navigateTo={navigateTo}
        rollback={rollback}
        hosted={hosted}
        hostedLoading={hostedLoading}
        domains={domains}
        domainsHref="/domains"
      />
    );
  }
  return { ...wrap(obs, <Stateful />, undefined, vulns), current: () => latest, navigateTo };
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
    readConfig,
  }: {
    env?: string;
    search?: WorkloadDetailSearch;
    actions?: Partial<WorkloadActions>;
    /** Defaults to the fixture's GetStatus answer for the env (null for a self-managed one). */
    hosted?: HostedObservation | null;
    obs?: ObservabilityApi;
    repoMeta?: RepoMetaApi;
    readConfig?: ComponentProps<typeof WorkloadDetail>["readConfig"];
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
        readConfig={readConfig}
        hosted={observation}
        declaredInRender={!scoped.placed && scoped.declared.includes(name)}
      />
    );
  }
  return { ...wrap(obs, <Stateful />, repoMeta), current: () => latest, actions: allActions };
}
