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

import { createTestQueryClient } from "@/test/renderWithQuery";
import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ReleasesApiProvider } from "@/services/forge/ReleasesApiContext";

import { createReleasesFixtureApi } from "../../Releases/__fixtures__/releaseFixtures";
import { OverviewHome, type OverviewSearch } from "../OverviewHome";
import { WorkloadDetail, type RestartControl, type WorkloadDetailSearch } from "../WorkloadDetail";
import type { StripEnv } from "../workloadModel";
import { STRIP_ENVS, WORKLOADS_PROJECT, createWorkloadsFixtureApi } from "../__fixtures__/workloadFixtures";

function wrap(obs: ObservabilityApi, ui: ReactElement): RenderResult {
  return render(
    <QueryClientProvider client={createTestQueryClient()}>
      <ObservabilityApiProvider api={obs}>
        <ReleasesApiProvider api={createReleasesFixtureApi()}>{ui}</ReleasesApiProvider>
      </ObservabilityApiProvider>
    </QueryClientProvider>
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
}: { env?: string; envs?: StripEnv[]; search?: OverviewSearch; obs?: ObservabilityApi } = {}): Harness<OverviewSearch> & { selectEnv: ReturnType<typeof vi.fn>; navigateTo: ReturnType<typeof vi.fn> } {
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
    restart = { onRestart: vi.fn(), unavailableReason: null },
    obs = createWorkloadsFixtureApi(),
  }: { env?: string; search?: WorkloadDetailSearch; restart?: RestartControl; obs?: ObservabilityApi } = {}
): Harness<WorkloadDetailSearch> {
  let latest = search;
  const scoped = STRIP_ENVS.find((candidate) => candidate.name === env)!;
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
        restart={restart}
      />
    );
  }
  return { ...wrap(obs, <Stateful />), current: () => latest };
}
