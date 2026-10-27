// Copyright (c) 2025 Reliant Labs

/**
 * Renders the Observability panel the way the env page does — a query client,
 * the ObservabilityApi seam, and URL state — with the URL held in component
 * state so a test can click through views and assert on what was navigated.
 *
 * Every test file also mocks `uplot` and `@tanstack/react-virtual` — see
 * obsMocks.ts.
 */

import { useState, type ReactElement } from "react";
import { render, type RenderResult } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { ObsQuotaProvider } from "@/services/controlPlane/observability/ObsQuotaContext";
import {
  ObservabilityApiProvider,
  type ObservabilityApi,
} from "@/services/controlPlane/observability/ObservabilityApiContext";

import ObservabilityPanel, { type ObsPlace } from "../ObservabilityPanel";
import { FIXTURE_ENVS, FIXTURE_PROJECT } from "../__fixtures__/fixtureApi";
import type { ObsNavigate, ObsSearch } from "../obsSearch";

export interface PanelHarness extends RenderResult {
  /** Every navigate the panel asked for, in order. */
  navigations: ObsSearch[];
  /** The URL state the panel is rendering now. */
  current: () => ObsSearch;
}

/** The env page's tab, pinned to prod — what most tests render. */
export const ENV_PLACE: ObsPlace = { kind: "env", env: { id: "env-prod", name: "prod" } };
/** The project area, every fixture environment. */
export const PROJECT_PLACE: ObsPlace = { kind: "project", project: FIXTURE_PROJECT, environments: [...FIXTURE_ENVS] };

export function renderPanel(api: ObservabilityApi, initial: ObsSearch = {}, place: ObsPlace = ENV_PLACE): PanelHarness {
  const navigations: ObsSearch[] = [];
  let latest = initial;
  function Stateful() {
    const [search, setSearch] = useState<ObsSearch>(initial);
    const navigate: ObsNavigate = (next) => {
      navigations.push(next);
      setSearch((prev) => {
        const merged: Record<string, unknown> = { ...prev, ...next };
        for (const key of Object.keys(merged)) if (merged[key] === undefined) delete merged[key];
        latest = merged as ObsSearch;
        return latest;
      });
    };
    return <ObservabilityPanel place={place} search={search} navigate={navigate} />;
  }
  const result = renderWithApi(api, <Stateful />);
  return { ...result, navigations, current: () => latest };
}

export function renderWithApi(api: ObservabilityApi, ui: ReactElement): RenderResult {
  const client = createTestQueryClient();
  return render(
    <QueryClientProvider client={client}>
      <ObsQuotaProvider><ObservabilityApiProvider api={api}>{ui}</ObservabilityApiProvider></ObsQuotaProvider>
    </QueryClientProvider>
  );
}

/** The org place: two apps with environments (both with a `prod`), and one app without any. */
export const ORG_PLACE: ObsPlace = {
  kind: "org",
  apps: ["api", "shop", "web"],
  environments: [
    { id: "env-shop-prod", name: "shop/prod", project: "shop", declared: null },
    { id: "env-shop-dev", name: "shop/dev", project: "shop", declared: null },
    { id: "env-api-prod", name: "api/prod", project: "api", declared: null },
  ],
};
