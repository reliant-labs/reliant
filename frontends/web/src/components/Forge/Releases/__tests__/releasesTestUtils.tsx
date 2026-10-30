// Copyright (c) 2025 Reliant Labs

/**
 * Renders a Releases page the way the console does — a query client, the
 * ReleasesApi and ObservabilityApi seams (fixtures), and URL state held in
 * component state so a test can click and assert on what changed. Every test
 * file mocks `@tanstack/react-router`'s useNavigate (forge-ui Link needs it).
 */

import { useState, type ReactElement } from "react";
import { render, type RenderResult } from "@testing-library/react";
import { QueryClientProvider, type QueryClient } from "@tanstack/react-query";
import { vi } from "vitest";

import { createTestQueryClient } from "@/test/renderWithQuery";
import type { PromotionList } from "@/services/controlPlane/observability/deliveryTypes";
import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ReleasesApiProvider, type ReleasesApi } from "@/services/forge/ReleasesApiContext";

import { createFixtureApi } from "../../Observability/__fixtures__/fixtureApi";
import { ReleaseDetail, type ReleaseDetailSearch } from "../ReleaseDetail";
import { ReleasesList, type PromoteControl, type ReleasesSearch } from "../ReleasesList";
import { FIXTURE_DELIVERY, FIXTURE_DORA, RELEASE_ENVS, RELEASE_PROJECT } from "../__fixtures__/releaseFixtures";

export function fixtureObsApi(over: Partial<ObservabilityApi> = {}): ObservabilityApi {
  return {
    ...createFixtureApi(),
    listReleases: async (req) =>
      ({
        promotions: FIXTURE_DELIVERY.filter((p) => p.promotedAtMs >= req.window.startMs && p.promotedAtMs <= req.window.endMs),
        truncated: false,
      }) satisfies PromotionList,
    getDoraMetrics: async () => FIXTURE_DORA,
    ...over,
  };
}

function wrap(api: ReleasesApi, obs: ObservabilityApi, ui: ReactElement, client = createTestQueryClient()): RenderResult {
  return render(
    <QueryClientProvider client={client}>
      <ObservabilityApiProvider api={obs}>
        <ReleasesApiProvider api={api}>{ui}</ReleasesApiProvider>
      </ObservabilityApiProvider>
    </QueryClientProvider>
  );
}

export interface Harness<S> extends RenderResult {
  client: QueryClient;
  current: () => S;
  promote: ReturnType<typeof vi.fn>;
  selectEnv: ReturnType<typeof vi.fn>;
}

export function renderList(
  api: ReleasesApi,
  {
    env = "prod",
    search = {},
    blocked = null,
    obs = fixtureObsApi(),
    scopeIsLocal = false,
  }: { env?: string; search?: ReleasesSearch; blocked?: string | null; obs?: ObservabilityApi; scopeIsLocal?: boolean } = {}
): Harness<ReleasesSearch> {
  let latest = search;
  const promote = vi.fn();
  const selectEnv = vi.fn();
  function Stateful() {
    const [state, setState] = useState<ReleasesSearch>(search);
    const control: PromoteControl = { onPromote: promote, blockedReason: blocked };
    return (
      <ReleasesList
        project={RELEASE_PROJECT}
        env={env}
        envs={RELEASE_ENVS}
        envsLoading={false}
        search={state}
        onSearch={(next) =>
          setState((prev) => {
            latest = { ...prev, ...next };
            return latest;
          })
        }
        releaseHref={(version, extra) => `/r/${version}${extra ? `?${new URLSearchParams(extra as Record<string, string>)}` : ""}`}
        promote={control}
        scopeIsLocal={scopeIsLocal}
      />
    );
  }
  const client = createTestQueryClient();
  return { ...wrap(api, obs, <Stateful />, client), client, current: () => latest, promote, selectEnv };
}

export function renderDetail(
  api: ReleasesApi,
  version: string,
  { env = "prod", search = {}, blocked = null }: { env?: string; search?: ReleaseDetailSearch; blocked?: string | null } = {}
): Harness<ReleaseDetailSearch> {
  let latest = search;
  const promote = vi.fn();
  function Stateful() {
    const [state, setState] = useState<ReleaseDetailSearch>(search);
    return (
      <ReleaseDetail
        project={RELEASE_PROJECT}
        version={version}
        env={env}
        envs={RELEASE_ENVS}
        envsLoading={false}
        search={state}
        onSearch={(next) =>
          setState((prev) => {
            latest = { ...prev, ...next };
            return latest;
          })
        }
        listHref="/releases"
        releaseHref={(v) => `/r/${v}`}
        activityHref={(name, target) => `/activity?env=${name}&target=${target}`}
        promote={{ onPromote: promote, blockedReason: blocked }}
      />
    );
  }
  const client = createTestQueryClient();
  return { ...wrap(api, fixtureObsApi(), <Stateful />, client), client, current: () => latest, promote, selectEnv: vi.fn() };
}
