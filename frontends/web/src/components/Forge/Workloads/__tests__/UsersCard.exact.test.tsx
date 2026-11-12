// Copyright (c) 2026 Reliant Labs

/**
 * The Overview Users card draws the figures forge marks exact (display.exact:
 * the account and active/new/returning counts) in full, as the Users page and
 * the Users dashboard state them: 1,284, never 1.3k.
 */

import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";

import { createShopApi } from "../../Demo/demoData/shop/shopApi";
import { DEMO_ORG, demoNow, resetDemoNowForTests } from "../../Demo/demoData/shopStory";
import { renderWithApi } from "../../Observability/__tests__/obsTestUtils";
import { UsersCard } from "../UsersCard";

vi.mock("../../Observability/charts/UPlotChart", () => ({ UPlotChart: () => null }));

beforeAll(() => resetDemoNowForTests(Date.UTC(2026, 10, 11, 15, 0, 0)));
afterAll(() => resetDemoNowForTests());

describe("the Overview Users card", () => {
  it("draws the exact figures in full", async () => {
    const api = createShopApi(demoNow());
    const overview = (await api.getDashboard({ orgId: DEMO_ORG, ref: { kind: "release", projectId: "shop", key: "overview" } })).spec;
    for (const id of ["active-7d", "new-7d", "returning-7d", "accounts"]) {
      expect(overview.panels.find((p) => p.id === id)?.display?.exact, id).toBe(true);
    }
    renderWithApi(api, <UsersCard scope={{ orgId: DEMO_ORG, projectId: "shop", envIds: [], filters: [] }} project="shop" range="7d" />);
    const active = await screen.findByTestId("overview-users-active");
    await waitFor(() => expect(within(active).getAllByRole("definition")[0].textContent).not.toBe("—"));
    for (const id of ["overview-users-active", "overview-users-new", "overview-users-returning"]) {
      const figure = within(screen.getByTestId(id)).getAllByRole("definition")[0].textContent ?? "";
      expect(figure, id).toMatch(/^\d{1,3}(,\d{3})*$/);
    }
    const meta = screen.getByTestId("overview-users").textContent ?? "";
    expect(meta).toMatch(/\d{1,3}(,\d{3})* accounts?/);
    expect(meta).not.toMatch(/\d(\.\d)?k accounts/);
  });
});
