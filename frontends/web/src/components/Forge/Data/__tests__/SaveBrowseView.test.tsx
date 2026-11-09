// Copyright (c) 2025 Reliant Labs
import { expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { ForgeConsoleContext, type ForgeConsole } from "../../Console/consoleContext";
import { createFixtureApi, FIXTURE_PROJECT } from "../../Observability/__fixtures__/fixtureApi";
import { renderWithApi } from "../../Observability/__tests__/obsTestUtils";
import { SaveBrowseView } from "../SaveBrowseView";
import { encodeFilter } from "../dataUrl-model";

vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@/hooks/observability-queries", async (original) => ({ ...await original<typeof import("@/hooks/observability-queries")>(), useObservabilityOrg: () => ({ orgId: "org-1" }) }));
const search = { db: "shop", table: "public.users", cols: ["id"], f: [encodeFilter({ column: "id", op: "eq", values: ["'; DROP TABLE users; --"] })], sort: ["id.desc"], row: "42", offset: 50, size: 25 };
const consoleContext = { forgeProject: FIXTURE_PROJECT, href: () => "/dashboard", env: "prod" } as unknown as ForgeConsole;
function setup(failure = false) {
  const onCall = vi.fn();
  const api = createFixtureApi({ onCall });
  if (failure) api.saveDashboard = vi.fn().mockRejectedValue(new ConnectError("stale version", Code.Aborted));
  renderWithApi(api, <ForgeConsoleContext.Provider value={consoleContext}><SaveBrowseView search={search} environmentId="env-prod" /></ForgeConsoleContext.Provider>);
  return { onCall, api };
}

it("saves a structured browse panel as a new version without altering the existing panels or environment scope", async () => {
  const { onCall } = setup();
  const user = userEvent.setup();
  await waitFor(() => expect(screen.getByRole("button", { name: "Save as view" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "Save as view" }));
  await user.selectOptions(screen.getByRole("combobox", { name: "Dashboard" }), "checkout-latency");
  await waitFor(() => expect(screen.getByRole("button", { name: "Save view" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "Save view" }));
  await screen.findByRole("link", { name: "View saved dashboard" });
  const saved = onCall.mock.calls.find(([method]) => method === "SaveDashboard")?.[1];
  expect(saved).toMatchObject({ projectId: FIXTURE_PROJECT, slug: "checkout-latency", baseVersion: 4 });
  expect(saved).not.toHaveProperty("envScope");
  expect(saved.spec.panels.at(-1)).toMatchObject({ kind: "browse", browse: { database: "shop", table: "public.users", columns: ["id"], filters: [{ column: "id", op: "eq", values: ["'; DROP TABLE users; --"] }], sort: [{ column: "id", desc: true }], size: 25 } });
  expect(saved.spec.panels.at(-1).browse).not.toHaveProperty("row");
  expect(saved.spec.panels.at(-1).browse).not.toHaveProperty("offset");
  expect(saved.spec.panels[0].id).toBe("p95");
});

it("surfaces a stale-version refusal and does not silently retry or replace a dashboard", async () => {
  const { api } = setup(true);
  const user = userEvent.setup();
  await waitFor(() => expect(screen.getByRole("button", { name: "Save as view" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "Save as view" }));
  await user.selectOptions(screen.getByRole("combobox", { name: "Dashboard" }), "checkout-latency");
  await waitFor(() => expect(screen.getByRole("button", { name: "Save view" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "Save view" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("stale version");
  expect(api.saveDashboard).toHaveBeenCalledTimes(1);
});
