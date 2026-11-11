// Copyright (c) 2025 Reliant Labs
import { describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { setReaderPrincipal } from "../../../AuditedAccess/readerSession";
import { writeAccess } from "../../../Data/dataAccess-model";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code } from "@connectrpc/connect";
import type { ReactNode } from "react";
import { DataBrowserApiProvider } from "@/services/controlPlane/observability/DataBrowserApiContext";
import type { Panel, PanelResult } from "@/services/controlPlane/observability/types";
import { createDataFixtureApi, fixtureUserId } from "../../../Data/__fixtures__/fixtureDataApi";
import { browseCalls, dataHref, searchOf, STAGING, PROD } from "../../../Data/__tests__/dataTestUtils";
import { decodeFilter, decodeList } from "../../../Data/dataUrl-model";
import { DataPanelBody, type PanelDataContext } from "../DataPanel";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
const context: PanelDataContext = { env: STAGING, href: dataHref, params: {} };
const panel: Panel = { id: "users", title: "Users", kind: "table", signal: "traces", series: [], groupBy: ["user_id"], link: { database: "shop", table: "public.users", column: "id", fromField: "user_id" } };
const result: PanelResult = { kind: "table", unit: "count", stepSeconds: 0, bucketSeconds: 0, window: { startMs: 0, endMs: 1000 }, columns: ["count"], rows: [{ group: { user_id: fixtureUserId(0) }, values: [4] }, { group: {}, values: [2] }], series: [], funnelSteps: [], issues: [], sessions: [], seriesMeta: [], seriesTotal: 0, warnings: [], compiledSql: [], truncated: false };
const query = { data: result, isLoading: false, error: null };
function setup(api = createDataFixtureApi()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { api, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}><DataBrowserApiProvider api={api}>{children}</DataBrowserApiProvider></QueryClientProvider> };
}

describe("dashboard Data links", () => {
  it.each(["table", "bar", "postgres"])("links %s result rows and disables rows with missing fields", async (kind) => {
    const { api, wrapper } = setup();
    render(<DataPanelBody panel={{ ...panel, kind, postgres: kind === "postgres" ? { database: "shop", sql: "select 1", draw: "table" } : undefined }} query={query} context={context} />, { wrapper });
    const link = await screen.findByRole("link", { name: /Open in Data/ });
    expect(decodeFilter(searchOf(link.getAttribute("href")).f![0]!)).toEqual({ column: "id", op: "eq", values: [fixtureUserId(0)] });
    expect(screen.getByText(/Data unavailable/)).toBeInTheDocument();
    expect(browseCalls(api)).toHaveLength(0);
  });
  it("opens composite row identities only with every required field present", async () => {
    const { wrapper } = setup();
    const composite = { ...panel, link: { database: "shop", table: "public.order_items", row: true, fromFields: ["order", "line"] } };
    const data = { ...result, rows: [{ group: { order: "42,3", line: "1\\\\2" }, values: [4] }, { group: { order: "42" }, values: [2] }] } as PanelResult;
    render(<DataPanelBody panel={composite} query={{ ...query, data }} context={context} />, { wrapper });
    const link = await screen.findByRole("link", { name: "Open in Data" });
    expect(decodeList(searchOf(link.getAttribute("href")).row!)).toEqual(["42,3", "1\\\\2"]);
    expect(screen.getByText("Data unavailable")).toBeInTheDocument();
  });
  it("does not request a missing DB or a broad organization scope", async () => {
    const { api, wrapper } = setup();
    const view = render(<DataPanelBody panel={{ ...panel, link: { ...panel.link!, database: "not-here" } }} query={query} context={context} />, { wrapper });
    await screen.findByText(/not-here is unavailable/);
    expect(api.calls.filter((call) => call.method === "describeDatabase")).toHaveLength(0);
    view.rerender(<DataPanelBody panel={panel} query={query} />);
    expect(screen.queryByRole("link", { name: /Open in Data/ })).not.toBeInTheDocument();
  });
  it("shows unknown kinds as explicitly unavailable without crashing", () => {
    render(<DataPanelBody panel={{ ...panel, kind: "from-a-newer-forge" }} query={query} />);
    expect(screen.getByTestId("obs-panel-unknown-kind")).toHaveTextContent("doesn't draw");
  });
});

const saved: Panel = { id: "view", kind: "browse", title: "Saved users", signal: "", series: [], browse: { database: "shop", table: "public.users", columns: ["id"], filters: [{ column: "id", op: "eq", values: ["{owner}"] }], sort: [{ column: "id", desc: true }], size: 25 } };
describe("saved browse panels", () => {
  it("drops the mounted saved-view grid and requires B's own confirmation on an account transition", async () => {
    writeAccess(PROD.id, "Reader A saved view");
    const { api, wrapper } = setup();
    render(<DataPanelBody panel={saved} query={query} context={{ ...context, env: PROD, params: { owner: fixtureUserId(0) } }} />, { wrapper });
    await screen.findAllByTestId("data-row");
    const reads = browseCalls(api).length;
    act(() => setReaderPrincipal("reader-b"));
    expect(screen.queryAllByTestId("data-row")).toHaveLength(0);
    await userEvent.click(await screen.findByRole("button", { name: "Give a Data access reason" }));
    expect(browseCalls(api)).toHaveLength(reads);
    await userEvent.type(screen.getByTestId("data-access-reason"), "Reader B saved view");
    await userEvent.click(screen.getByTestId("data-access-confirm"));
    await screen.findAllByTestId("data-row");
    expect(browseCalls(api).at(-1)).toMatchObject({ access: { reason: "Reader B saved view", confirmEnvId: PROD.id } });
    act(() => setReaderPrincipal(null));
    expect(screen.queryAllByTestId("data-row")).toHaveLength(0);
    expect(screen.getByRole("status")).toHaveTextContent("Sign in");
  });

  it("runs structured BrowseTable, opens the same view, and rebinds on env changes", async () => {
    const { api, wrapper } = setup(createDataFixtureApi({ policy: { "env-prod": "open" } }));
    const view = render(<DataPanelBody panel={saved} query={query} context={{ ...context, params: { owner: fixtureUserId(0) } }} />, { wrapper });
    await screen.findByTestId("saved-browse-panel");
    await waitFor(() => expect(browseCalls(api)).toHaveLength(1));
    expect(browseCalls(api)[0]).toMatchObject({ environmentId: STAGING.id, database: "shop", table: "public.users", filters: [{ column: "id", op: "eq", values: [fixtureUserId(0)] }], sort: [{ column: "id", desc: true }], limit: 25 });
    expect(decodeFilter(searchOf(screen.getByRole("link", { name: "Open in Data" }).getAttribute("href")).f![0]!)).toMatchObject({ values: [fixtureUserId(0)] });
    view.rerender(<DataPanelBody panel={saved} query={query} context={{ ...context, env: PROD, params: { owner: fixtureUserId(1) } }} />);
    await waitFor(() => expect(browseCalls(api)).toHaveLength(2));
    expect(browseCalls(api)[1]).toMatchObject({ environmentId: PROD.id, filters: [{ values: [fixtureUserId(1)] }] });
  });
  it("asks for audited confirmation before describing or reading, and never carries reason to another env", async () => {
    sessionStorage.clear();
    const { api, wrapper } = setup();
    const view = render(<DataPanelBody panel={saved} query={query} context={{ ...context, env: PROD, params: { owner: fixtureUserId(0) } }} />, { wrapper });
    await screen.findByRole("button", { name: "Give a Data access reason" });
    expect(api.calls.filter((call) => call.method !== "listBrowsableDatabases")).toHaveLength(0);
    await userEvent.click(screen.getByRole("button", { name: "Give a Data access reason" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    view.rerender(<DataPanelBody panel={saved} query={query} context={{ ...context, params: { owner: fixtureUserId(0) } }} />);
    await waitFor(() => expect(browseCalls(api)).toHaveLength(1));
    expect(browseCalls(api)[0]).toMatchObject({ environmentId: STAGING.id });
    expect(browseCalls(api)[0]).not.toHaveProperty("access");
  });
  it.each(["describeDatabase", "browseTable"] as const)("renders db:query grant refusal at %s without retry loops", async (method) => {
    const { api, wrapper } = setup(createDataFixtureApi({ fail: { method, code: Code.PermissionDenied, message: "db:query required" } }));
    render(<DataPanelBody panel={saved} query={query} context={{ ...context, params: { owner: "x" } }} />, { wrapper });
    await screen.findByRole("alert");
    expect(api.calls.filter((call) => call.method === method)).toHaveLength(1);
    if (method === "describeDatabase") expect(browseCalls(api)).toHaveLength(0);
  });
  it("rejects unknown parameters before BrowseTable runs", async () => {
    const { api, wrapper } = setup();
    render(<DataPanelBody panel={saved} query={query} context={context} />, { wrapper });
    await screen.findByText(/invalid columns, filters or parameters/);
    expect(browseCalls(api)).toHaveLength(0);
  });
  it("never reads when policy denies", async () => {
    const { api, wrapper } = setup(createDataFixtureApi({ policy: { "env-staging": "deny" } }));
    render(<DataPanelBody panel={saved} query={query} context={context} />, { wrapper });
    await screen.findByText(/Data access is denied/);
    expect(api.calls.filter((call) => call.method !== "listBrowsableDatabases")).toHaveLength(0);
  });
});
