// Copyright (c) 2025 Reliant Labs

/**
 * Renders a Releases page the way the console does — a query client, the
 * fixture ledger served as DeployService (ReleasesFixtureScope), the
 * ObservabilityApi seam (fixtures), and URL state held in
 * component state so a test can click and assert on what changed. Every test
 * file mocks `@tanstack/react-router`'s useNavigate (forge-ui Link needs it).
 */

import { useState, type ReactElement, type ReactNode } from "react";
import { render, type RenderResult } from "@testing-library/react";
import type { QueryClient } from "@tanstack/react-query";
import { vi } from "vitest";

import { createTestQueryClient } from "@/test/renderWithQuery";
import type { PromotionList } from "@/services/controlPlane/observability/deliveryTypes";
import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { observabilityFixtureTransport } from "@/test/observabilityFixtures";
import { mockTransport } from "@/lib/test-utils";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";

import { createFixtureApi } from "../../Observability/__fixtures__/fixtureApi";
import { ReleaseDetail, type ReleaseDetailSearch } from "../ReleaseDetail";
import { ReleasesList, type PromoteControl, type ReleasesSearch } from "../ReleasesList";
import { FIXTURE_DELIVERY, FIXTURE_DORA, RELEASE_ENVS, RELEASE_PROJECT, type ReleasesFixtureApi } from "../__fixtures__/releaseFixtures";
import { releasesFixtureHandlers } from "../__fixtures__/releaseFixtureTransport";

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

// Both fixture families share one transport binding on the query client.
export function ReleasesTestScope({ api, obs, client, children }: { api: ReleasesFixtureApi; obs: ObservabilityApi; client?: QueryClient; children: ReactNode }) {
  const [scope] = useState(() => {
    const handlers = releasesFixtureHandlers(api);
    const releases = mockTransport(handlers);
    const observability = observabilityFixtureTransport(() => obs, Date.now());
    const pick = (method: { parent: { typeName: string }; name: string }) =>
      `${method.parent.typeName}/${method.name}` in handlers ? releases : observability;
    return createTransportScope({
      id: "releases-test",
      queryClient: client ?? createTestQueryClient(),
      transport: {
        unary: (method, ...rest) => pick(method).unary(method, ...rest),
        stream: (method, ...rest) => pick(method).stream(method, ...rest),
      },
    });
  });
  return <TransportScopeProvider scope={scope}><ObservabilityApiProvider api={obs}>{children}</ObservabilityApiProvider></TransportScopeProvider>;
}

function wrap(api: ReleasesFixtureApi, obs: ObservabilityApi, ui: ReactElement, client = createTestQueryClient()): RenderResult {
  return render(<ReleasesTestScope api={api} obs={obs} client={client}>{ui}</ReleasesTestScope>);
}

export interface Harness<S> extends RenderResult {
  client: QueryClient;
  current: () => S;
  promote: ReturnType<typeof vi.fn>;
  selectEnv: ReturnType<typeof vi.fn>;
}

export function renderList(
  api: ReleasesFixtureApi,
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
  api: ReleasesFixtureApi,
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
