// Copyright (c) 2025 Reliant Labs

/**
 * The panel drawer over every kind forge's spec has: the picker offers each
 * with a description; the display controls offer only the styles and options
 * the kind takes; a sql or postgres statement runs on Run, with its parameters
 * shown and the server's refusal shown verbatim; a postgres panel's schema
 * browser reads ListDashboardDatabases; a text panel's Markdown is drawn as it
 * is typed.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { Panel } from "@/services/controlPlane/observability/types";

import { createFixtureApi, FIXTURE_ENVS } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { PanelDrawer } from "../PanelDrawer";
import { starterPanel, VISUALIZATIONS } from "../panelBuilder";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
// Monaco loads from a CDN in the app; here the editor is a textarea with Monaco's props.
vi.mock("@monaco-editor/react", () => ({
  Editor: ({ value, onChange, options }: { value: string; onChange: (v: string) => void; options: { ariaLabel: string } }) => (
    <textarea aria-label={options.ariaLabel} value={value} onChange={(e) => onChange(e.target.value)} />
  ),
}));

const starter = starterPanel({ id: "d", title: "D", filters: [], panels: [] });

function renderDrawer(options: { api?: ObservabilityApi; panel?: Panel; envIds?: string[] } = {}) {
  const onCall = vi.fn();
  const onSave = vi.fn();
  const api = options.api ?? createFixtureApi({ onCall });
  renderWithApi(
    api,
    <PanelDrawer
      mode="add"
      panel={options.panel ?? starter}
      dashboardTitle="Checkout"
      scope={{ orgId: "org", projectId: "shop", envIds: options.envIds ?? ["env-prod"], filters: [] }}
      range="1h"
      services={[]}
      environments={FIXTURE_ENVS}
      env="prod"
      scopeLabel="prod"
      version={3}
      envScope="prod"
      saving={false}
      error={null}
      onCancel={vi.fn()}
      onSave={onSave}
    />
  );
  return { onCall, onSave };
}

const kind = (label: string) => within(screen.getByTestId("panel-builder-kind")).getByRole("radio", { name: label });
const style = () => screen.queryByTestId("panel-builder-style");
const styleOptions = () => within(screen.getByTestId("panel-builder-style")).getAllByRole("radio").map((r) => r.textContent);

describe("the kind picker", () => {
  it("offers every kind forge has, each with what it is for", () => {
    renderDrawer();
    const cards = within(screen.getByTestId("panel-builder-kind")).getAllByRole("radio");
    expect(cards.map((card) => card.getAttribute("data-value"))).toEqual(VISUALIZATIONS.map((v) => v.kind));
    for (const v of VISUALIZATIONS) expect(kind(v.label)).toHaveAccessibleDescription(v.description);
    expect(kind("Time series")).toHaveAttribute("aria-checked", "true");
  });
});

describe("display styles", () => {
  it("offers only the styles the kind takes, and the options that style uses", async () => {
    const user = userEvent.setup();
    renderDrawer();
    expect(styleOptions()).toEqual(["Line", "Area", "Bars over time"]);
    expect(screen.queryByTestId("panel-builder-stack")).not.toBeInTheDocument();
    expect(screen.getByTestId("panel-builder-top-n")).toBeInTheDocument();
    await user.click(within(style()!).getByRole("radio", { name: "Area" }));
    expect(screen.getByTestId("panel-builder-stack")).toBeInTheDocument();

    await user.click(kind("Bar"));
    expect(styleOptions()).toEqual(["Bars", "Pie", "Donut"]);
    expect(screen.getByTestId("panel-builder-horizontal")).toBeInTheDocument();
    expect(screen.queryByTestId("panel-builder-top-n")).not.toBeInTheDocument();
    await user.click(within(style()!).getByRole("radio", { name: "Pie" }));
    expect(screen.queryByTestId("panel-builder-horizontal")).not.toBeInTheDocument();
    expect(screen.getByTestId("panel-builder-top-n")).toBeInTheDocument();

    await user.click(kind("Stat"));
    expect(styleOptions()).toEqual(["Stat", "Gauge"]);
    await user.click(within(style()!).getByRole("radio", { name: "Gauge" }));
    expect(screen.getByTestId("panel-builder-gauge-max")).toBeInTheDocument();

    for (const label of ["Table", "Heatmap", "Rows", "Postgres", "Text", "Funnel", "Issues", "Sessions"]) {
      await user.click(kind(label));
      expect(style(), label).not.toBeInTheDocument();
    }
    await user.click(kind("ClickHouse SQL"));
    expect(styleOptions()).toEqual(["Line", "Area", "Bars over time", "Funnel"]);
  });

  it("saves a stacked area with its top N, and a gauge needs its max first", async () => {
    const user = userEvent.setup();
    const { onSave } = renderDrawer();
    await user.click(within(style()!).getByRole("radio", { name: "Area" }));
    await user.click(screen.getByTestId("panel-builder-stack"));
    await user.type(screen.getByTestId("panel-builder-top-n"), "5");
    await user.click(screen.getByTestId("panel-drawer-save"));
    expect(onSave.mock.calls[0][0].display).toEqual({ style: "area", stack: true, topN: 5 });

    await user.click(kind("Stat"));
    await user.click(within(style()!).getByRole("radio", { name: "Gauge" }));
    expect(screen.getByTestId("panel-drawer-problems")).toHaveTextContent("A gauge needs a max");
    expect(screen.getByTestId("panel-drawer-save")).toBeDisabled();
    await user.type(screen.getByTestId("panel-builder-gauge-max"), "250");
    expect(screen.getByTestId("panel-drawer-save")).toBeEnabled();
  });
});

describe("a sql panel", () => {
  it("shows its parameters, runs on Run, and shows the guard's refusal in its own words", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const fixture = createFixtureApi({ onCall });
    const refusal = "the statement reads the system database, which a panel may not";
    const api: ObservabilityApi = {
      ...fixture,
      queryPanel: (req) => (req.panel.sql?.includes("system.") ? Promise.reject(new ConnectError(refusal, Code.InvalidArgument)) : fixture.queryPanel(req)),
    };
    renderDrawer({ api });
    await user.click(kind("ClickHouse SQL"));
    expect(screen.getByTestId("statement-params")).toHaveTextContent("{from:DateTime64(9)}");
    expect(screen.getByTestId("panel-drawer-preview")).toHaveTextContent("Run the statement");
    const sqlReads = () => onCall.mock.calls.filter(([rpc, req]) => rpc === "QueryPanel" && req.panel.kind === "sql");
    expect(sqlReads()).toHaveLength(0);

    await user.click(screen.getByTestId("statement-run"));
    await waitFor(() => expect(sqlReads()).toHaveLength(1));

    const editor = await screen.findByLabelText("ClickHouse statement");
    await user.clear(editor);
    await user.type(editor, "SELECT count() FROM system.tables");
    expect(screen.getByText(/Edited since the last run/)).toBeInTheDocument();
    await user.click(screen.getByTestId("statement-run"));
    expect(await screen.findByTestId("statement-refusal")).toHaveTextContent(refusal);
  });
});

describe("a postgres panel", () => {
  it("browses the environment's databases, binds $1 $2 $3, and runs against the one environment", async () => {
    const user = userEvent.setup();
    const { onCall } = renderDrawer();
    await user.click(kind("Postgres"));
    expect(screen.getByTestId("statement-params")).toHaveTextContent("$1");
    expect(screen.getByTestId("statement-params")).toHaveTextContent("$3");
    const schema = await screen.findByTestId("schema-postgres");
    expect(onCall.mock.calls.find(([rpc]) => rpc === "ListDashboardDatabases")![1]).toEqual({ environmentId: "env-prod" });
    expect(within(schema).getByText("public.users")).toBeInTheDocument();
    await user.click(within(schema).getByRole("button", { name: "Use" }));
    expect(screen.getByTestId("panel-builder-postgres-database")).toHaveValue("app");

    await user.click(screen.getByTestId("statement-run"));
    await waitFor(() => expect(onCall.mock.calls.some(([rpc]) => rpc === "QueryDatabasePanel")).toBe(true));
    expect(onCall.mock.calls.find(([rpc]) => rpc === "QueryDatabasePanel")![1]).toMatchObject({ environmentId: "env-prod", query: { database: "app" } });

    const editor = await screen.findByLabelText("Postgres statement");
    await user.clear(editor);
    await user.type(editor, "DELETE FROM users");
    await user.click(screen.getByTestId("statement-run"));
    expect(await screen.findByTestId("statement-refusal")).toHaveTextContent("dbquery: refused (statement_not_read_only): the statement starts with DELETE");
  });

  it("re-reads on a second Run with nothing edited", async () => {
    const user = userEvent.setup();
    const { onCall } = renderDrawer();
    await user.click(kind("Postgres"));
    const schema = await screen.findByTestId("schema-postgres");
    await user.click(within(schema).getByRole("button", { name: "Use" }));
    const reads = () => onCall.mock.calls.filter(([rpc]) => rpc === "QueryDatabasePanel");
    await user.click(screen.getByTestId("statement-run"));
    await waitFor(() => expect(reads()).toHaveLength(1));
    await waitFor(() => expect(screen.getByTestId("statement-run")).toBeEnabled());
    await user.click(screen.getByTestId("statement-run"));
    await waitFor(() => expect(reads()).toHaveLength(2));
  });

  it("takes comma-separated values and split-by columns as they are typed", async () => {
    const user = userEvent.setup();
    const { onSave } = renderDrawer();
    await user.click(kind("Postgres"));
    await user.click(within(await screen.findByTestId("schema-postgres")).getByRole("button", { name: "Use" }));
    await user.type(screen.getByTestId("panel-builder-postgres-values"), "orders, revenue");
    await user.type(screen.getByTestId("panel-builder-postgres-group"), "region,plan");
    expect(screen.getByTestId("panel-builder-postgres-values")).toHaveValue("orders, revenue");
    expect(screen.getByTestId("panel-builder-postgres-group")).toHaveValue("region,plan");
    expect(screen.getByTestId("panel-drawer-save")).toBeEnabled();
    await user.click(screen.getByTestId("panel-drawer-save"));
    expect((onSave.mock.calls.at(-1)?.[0] as Panel | undefined)?.postgres).toMatchObject({ values: ["orders", "revenue"], group: ["region", "plan"] });
  });

  it("says why it cannot read with more than one environment in scope", async () => {
    const user = userEvent.setup();
    renderDrawer({ envIds: ["env-prod", "env-dev"] });
    await user.click(kind("Postgres"));
    expect(screen.getByText(/Choose one environment for the dashboard/)).toBeInTheDocument();
  });
});

describe("a text panel", () => {
  it("draws the Markdown as it is typed and saves only what a text panel takes", async () => {
    const user = userEvent.setup();
    const { onSave } = renderDrawer();
    await user.click(kind("Text"));
    const markdown = screen.getByTestId("panel-builder-markdown");
    await user.clear(markdown);
    await user.type(markdown, "Owned by **payments**");
    const preview = screen.getByTestId("panel-drawer-preview");
    expect(within(preview).getByText("payments").tagName).toBe("STRONG");
    await user.click(screen.getByTestId("panel-drawer-save"));
    const saved = onSave.mock.calls[0][0] as Panel;
    expect(saved).toMatchObject({ kind: "text", markdown: "Owned by **payments**" });
    expect(saved.where).toBeUndefined();
    expect(saved.series).toEqual([]);
  });
});
