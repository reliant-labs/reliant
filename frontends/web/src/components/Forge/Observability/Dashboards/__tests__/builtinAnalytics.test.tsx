// Copyright (c) 2026 Reliant Labs

/**
 * What the console needs to draw forge's built-in product analytics
 * dashboards (forge #2075) as their author meant them: the Product group in
 * reading order, a figure beside its own previous period drawn as a change,
 * country codes as countries, and a funnel's headline in its own unit.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import type { DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";
import type { Panel, PanelResult } from "@/services/controlPlane/observability/types";

import { countryFlag, isCountryCode } from "../../geo";
import { groupIndex } from "../dashboardGroups";
import { formatPercent as funnelPercent } from "../FunnelChart";
import { countryColumn, describeChange, funnelUnit, PanelBody } from "../PanelBody";
import { sqlPreviousFigures } from "../viewerModel";

vi.mock("../../charts/UPlotChart", () => ({ UPlotChart: () => null }));

const sqlPanel = (over: Partial<Panel> = {}): Panel => ({ id: "p", title: "P", kind: "sql", signal: "", series: [], sql: "SELECT 1", ...over });

const summary = (key: string, source: DashboardSummary["source"] = "generated"): DashboardSummary =>
  ({ ref: { kind: "release", projectId: "shop", key }, source, title: key, group: "product" }) as DashboardSummary;

const figures = (columns: string[], values: (number | null)[]): PanelResult =>
  ({ kind: "sql", unit: "count", columns, rows: [{ group: {}, values }], series: [], truncated: false }) as unknown as PanelResult;

describe("forge's built-in product dashboards", () => {
  it("are listed Users, Product, Acquisition, ahead of the other generated ones, whatever order the server sends", () => {
    const product = groupIndex([summary("frontend-web"), summary("acquisition"), summary("checkout", "repo"), summary("product"), summary("users")]).find((g) => g.group === "product")!;
    expect(product.dashboards.map((d) => d.ref.key)).toEqual(["users", "product", "acquisition", "frontend-web", "checkout"]);
  });

  it("draw a figure returned beside its previous 7 days as one figure and its change", () => {
    const split = sqlPreviousFigures(figures(["Active users (7 days)", "Previous 7 days"], [412, 398]))!;
    expect(split.current.columns).toEqual(["Active users (7 days)"]);
    expect(split.current.rows[0].values).toEqual([412]);
    expect(split.previous.rows[0].values).toEqual([398]);
    expect(split.label).toBe("previous 7 days");
    // Figures with no previous column are left alone.
    expect(sqlPreviousFigures(figures(["Page views", "Views per visit"], [48_000, 2.6]))).toBeNull();
  });

  it("name a Country column's ISO codes with a flag, and leave Unattributed as it is", () => {
    expect(countryColumn("Country")).toBe(true);
    expect(countryColumn("geo.country.iso_code")).toBe(true);
    expect(countryColumn("Channel")).toBe(false);
    expect(countryFlag("DE")).toBe("🇩🇪");
    expect(isCountryCode("DE")).toBe(true);
    expect(isCountryCode("Unattributed")).toBe(false);
    expect(countryFlag("Unattributed")).toBe("");
  });

  it("never round a funnel's 99.7% drop to 100%", () => {
    expect(funnelPercent(0.997)).toBe("99.7%");
    expect(funnelPercent(0.71)).toBe("71%");
    expect(funnelPercent(1)).toBe("100%");
  });

  it("head a funnel with what its steps count, never a bare Count", () => {
    expect(funnelUnit("Persons")).toBe("persons");
    expect(funnelUnit("Visitors")).toBe("visitors");
    expect(funnelUnit("Count")).toBe("entrants");
    expect(funnelUnit(undefined)).toBe("entrants");
  });

  it("draw an exact figure in full, and an ordinary one compactly", () => {
    const { unmount } = render(<PanelBody panel={sqlPanel({ unit: "count", display: { exact: true } })} query={{ data: figures(["Accounts"], [12_840]), isLoading: false, error: null }} />);
    expect(screen.getByText("12,840")).toBeTruthy();
    unmount();
    render(<PanelBody panel={sqlPanel({ unit: "count" })} query={{ data: figures(["Accounts"], [12_840]), isLoading: false, error: null }} />);
    expect(screen.getByText("12.8k")).toBeTruthy();
  });

  it("keep an exact figure's change in full when the previous period was zero", () => {
    expect(describeChange(1_284, 0, "count", "previous 7 days", true)).toBe("▲ 1,284 vs previous 7 days");
    expect(describeChange(1_284, 0, "count", "previous 7 days")).toBe("▲ 1.3k vs previous 7 days");
  });

  it("render the Overview's 7-day figures with their change, and a countries table with flags and names", () => {
    const { unmount } = render(<PanelBody panel={sqlPanel({ unit: "count" })} query={{ data: figures(["Active users (7 days)", "Previous 7 days"], [412, 398]), isLoading: false, error: null }} />);
    expect(screen.getByText("412")).toBeTruthy();
    expect(screen.queryByText("398")).toBeNull();
    expect(screen.getByTestId("obs-panel-change").textContent).toBe("▲ 3.5% vs previous 7 days");
    unmount();
    const countries = {
      ...figures(["Visitors", "Sign-ups"], []),
      rows: [
        { group: { Country: "DE" }, values: [3_400, 9] },
        { group: { Country: "Unattributed" }, values: [0, 6] },
      ],
    } as PanelResult;
    render(<PanelBody panel={sqlPanel()} query={{ data: countries, isLoading: false, error: null }} />);
    expect(screen.getByTestId("obs-panel-country").textContent).toContain("Germany");
    expect(screen.getByText("Unattributed")).toBeTruthy();
  });
});
