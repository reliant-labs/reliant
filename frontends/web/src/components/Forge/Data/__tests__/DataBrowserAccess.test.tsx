// Copyright (c) 2025 Reliant Labs

/**
 * Policy and the audited environment's confirmation: nothing is read in an
 * audited environment until the reader gives a reason, once per tab session,
 * in a dialog naming the environment; every read then carries confirm_env_id
 * and the reason. A deny policy and an empty environment say what they are.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code } from "@connectrpc/connect";

import { createDataFixtureApi } from "../__fixtures__/fixtureDataApi";
import { readAccess, writeAccess } from "../dataAccess-model";
import { browseCalls, PROD, renderBrowser, STAGING, searchOf } from "./dataTestUtils";
import { decodeFilter } from "../dataUrl-model";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

afterEach(() => { window.sessionStorage.clear(); vi.unstubAllEnvs(); });

const described = (api: ReturnType<typeof createDataFixtureApi>) => api.calls.filter((call) => call.method === "describeDatabase");

describe("an audited environment", () => {
  it("asks for a reason, naming the environment, before reading anything", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    renderBrowser(api, { db: "shop", table: "public.orders" }, PROD);
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Reading prod's data is audited");
    expect(screen.getByTestId("data-access-confirm")).toHaveTextContent("Read prod's data");
    expect(screen.getByTestId("data-access-confirm")).toBeDisabled();
    expect(described(api)).toHaveLength(0);
    expect(browseCalls(api)).toHaveLength(0);

    await userEvent.type(screen.getByTestId("data-access-reason"), "Investigating #4412");
    fireEvent.click(screen.getByTestId("data-access-confirm"));

    await waitFor(() => expect(screen.getByTestId("data-grid")).toHaveAttribute("data-state", "ready"));
    const access = { confirmEnvId: "env-prod", reason: "Investigating #4412" };
    expect(described(api)[0]!.req).toMatchObject({ environmentId: "env-prod", database: "shop", access });
    expect(browseCalls(api).at(-1)).toMatchObject({ access });
    expect(readAccess("env-prod")).toEqual(access);
    expect(screen.getByTestId("data-reason")).toHaveTextContent("Reading as: Investigating #4412");
  });

  it("asks once per tab session: a kept reason is sent without asking", async () => {
    writeAccess("env-prod", "Kept");
    const api = createDataFixtureApi({ onlyShop: true });
    renderBrowser(api, { db: "shop", table: "public.users" }, PROD);
    await waitFor(() => expect(screen.getByTestId("data-grid")).toHaveAttribute("data-state", "ready"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(browseCalls(api)[0]).toMatchObject({ access: { confirmEnvId: "env-prod", reason: "Kept" } });
  });

  it("waits, with a way back to the question, when the reader cancels", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    renderBrowser(api, { db: "shop" }, PROD);
    await screen.findByRole("dialog");
    fireEvent.click(screen.getByTestId("data-access-cancel"));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByTestId("data-needs-reason")).toHaveTextContent("prod is audited: reads need a reason");
    expect(described(api)).toHaveLength(0);
    fireEvent.click(screen.getByTestId("data-give-reason"));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });

  it("asks again when the server says the confirmation is missing", async () => {
    writeAccess("env-prod", "Stale");
    const api = createDataFixtureApi({ onlyShop: true, fail: { method: "describeDatabase", code: Code.FailedPrecondition, reason: "confirmation_required" } });
    renderBrowser(api, { db: "shop" }, PROD);
    expect(await screen.findByRole("dialog")).toHaveTextContent("Reading prod's data is audited");
    expect(readAccess("env-prod")).toBeNull();
  });

  it("never asks in an open environment", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    renderBrowser(api, { db: "shop", table: "public.orders" }, STAGING);
    await waitFor(() => expect(screen.getByTestId("data-grid")).toHaveAttribute("data-state", "ready"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(browseCalls(api)[0]).not.toHaveProperty("access");
  });
});

describe("Identity app data discovery", () => {
  const subject = JSON.stringify({ environmentId: "env-prod", userId: "subject-1", email: "ada@example.com" });
  it("waits for the existing reason flow, then describes one chosen database without rows or counts", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    renderBrowser(api, { db: "shop", subject }, PROD);
    await screen.findByRole("dialog");
    expect(described(api)).toHaveLength(0);
    expect(browseCalls(api)).toHaveLength(0);
    fireEvent.change(screen.getByTestId("data-access-reason"), { target: { value: "Support case 42" } });
    fireEvent.click(screen.getByTestId("data-access-confirm"));
    expect(await screen.findByTestId("data-identity-tables")).toHaveTextContent("No explicit identity-subject");
    expect(described(api)).toHaveLength(1);
    expect(described(api)[0]!.req).toMatchObject({ access: { reason: "Support case 42", confirmEnvId: "env-prod" } });
    expect(browseCalls(api)).toHaveLength(0);
  });
  it("turns only explicitly marked described columns into exact bound links without reading rows", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    const original = api.describeDatabase.bind(api);
    api.describeDatabase = async (...args) => {
      const description = await original(...args);
      return { ...description, tables: description.tables.map((t) => ({ ...t, columns: t.columns.map((c, i) => ({ ...c, identitySubject: t.name === "users" && i === 0 ? "id" as const : undefined })) })) };
    };
    writeAccess("env-prod", "Case 42");
    renderBrowser(api, { db: "shop", subject }, PROD);
    const discovery = await screen.findByTestId("data-identity-tables");
    const link = within(discovery).getByRole("link");
    const search = searchOf(link.getAttribute("href"));
    expect(search.db).toBe("shop");
    expect(search.table).toBe("public.users");
    expect(decodeFilter(search.f![0]!)?.values).toEqual(["subject-1"]);
    expect(browseCalls(api)).toHaveLength(0);
  });
  it("offers no subject links or row reads from an old URL in a contained build", async () => {
    vi.stubEnv("VITE_IDENTITY_DATA_LINKS", "disabled");
    const api = createDataFixtureApi({ onlyShop: true });
    renderBrowser(api, { db: "shop", subject }, STAGING);
    expect(await screen.findByTestId("data-identity-tables")).toHaveTextContent("crosslinks are disabled");
    expect(within(screen.getByTestId("data-identity-tables")).queryByRole("link")).not.toBeInTheDocument();
    expect(browseCalls(api)).toHaveLength(0);
  });
  it("never probes after metadata admission is refused", async () => {
    const api = createDataFixtureApi({ fail: { method: "listBrowsableDatabases", code: Code.PermissionDenied } });
    renderBrowser(api, { subject }, PROD);
    await screen.findByTestId("data-databases-error");
    expect(described(api)).toHaveLength(0);
    expect(browseCalls(api)).toHaveLength(0);
  });
  it("does not link a prod subject in staging and never reads a count", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    renderBrowser(api, { db: "shop", subject }, STAGING);
    expect(await screen.findByTestId("data-identity-tables")).toHaveTextContent("another environment");
    expect(browseCalls(api)).toHaveLength(0);
  });
});

describe("policies and empty environments", () => {
  it("explains a deny policy and reads nothing", async () => {
    const api = createDataFixtureApi({ onlyShop: true, policy: { "env-staging": "deny" } });
    renderBrowser(api, { db: "shop" }, STAGING);
    const denied = await screen.findByTestId("data-policy-deny");
    expect(denied).toHaveTextContent("staging doesn't allow reading its data");
    expect(denied).toHaveTextContent("forge.DataAccess");
    expect(screen.getByTestId("data-policy")).toHaveAttribute("data-policy", "deny");
    expect(described(api)).toHaveLength(0);
  });

  it("opens a readable database over an unreachable one listed first", async () => {
    const view = renderBrowser(createDataFixtureApi(), {}, STAGING);
    await waitFor(() => expect(view.search().db).toBe("shop"));
    expect(within(screen.getByTestId("data-databases")).getByTestId("data-db-analytics")).toBeDisabled();
  });

  it("says an environment with no recorded release has databases not known yet, not that they are unreachable", async () => {
    renderBrowser(createDataFixtureApi({ fail: { method: "listBrowsableDatabases", code: Code.FailedPrecondition, reason: "shape_unknown" } }), {}, STAGING);
    const error = await screen.findByTestId("data-databases-error");
    expect(error).toHaveAttribute("data-kind", "shape-unknown");
    expect(error).toHaveTextContent("staging's databases aren't known yet");
    expect(error).toHaveTextContent("reliant forge env deploy staging");
  });

  it("says when an environment declares no database", async () => {
    renderBrowser(createDataFixtureApi({ noDatabases: true }), {}, STAGING);
    expect(await screen.findByTestId("data-no-databases")).toHaveTextContent("staging has no databases");
  });
});
