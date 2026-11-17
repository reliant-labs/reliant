// Copyright (c) 2026 Reliant Labs

/**
 * The panel kinds the Product analytics Overview draws (forge dashspec):
 * a bar list, a KPI card, which way a figure is good, a funnel in steps, and
 * Needs attention.
 */

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";

import type { Panel, PanelResult, Series } from "@/services/controlPlane/observability/types";
import { AttentionPanel } from "../AttentionPanel";
import { BarList } from "../BarList";
import { PanelBody, toneOf } from "../PanelBody";
import { StepFunnel } from "../StepFunnel";

vi.mock("../../charts/UPlotChart", () => ({ UPlotChart: vi.fn(() => <div data-testid="sql-chart" />) }));
vi.mock("@/components/forge-ui/link", () => ({ default: ({ href, children, ...rest }: { href: string; children: React.ReactNode }) => <a href={href} {...rest}>{children}</a> }));

// The figures an attention panel reads, by panel id: [now, before].
const figuresById: Record<string, [number | null, number | null]> = {};
vi.mock("@/hooks/observability-queries", () => ({
  usePanelQuery: (_scope: unknown, _range: unknown, panel: Panel, _s: unknown, _g: unknown, options: { offsetKey?: string } = {}) => {
    const pair = figuresById[panel.id];
    if (!pair) return { data: undefined, isLoading: true };
    const value = options.offsetKey ? pair[1] : pair[0];
    return { data: result({ columns: [panel.title], rows: value === null ? [] : [{ group: {}, values: [value] }] }), isLoading: false };
  },
}));

const result = (data: Partial<PanelResult> = {}): PanelResult => ({
  kind: "sql", unit: "", series: [], columns: [], rows: [], stepSeconds: 0,
  truncated: false, funnelSteps: [], issues: [], sessions: [], seriesMeta: [],
  seriesTotal: 0, warnings: [], compiledSql: [], ...data,
});
const ready = (data: PanelResult) => ({ data, isLoading: false, error: null });
const sql = (id: string, extra: Partial<Panel> = {}): Panel => ({ id, title: id, kind: "sql", signal: "", series: [], ...extra });
const row = (group: Record<string, string>, values: (number | null)[]) => ({ group, values });

describe("bar list (display.style: barlist)", () => {
  const channels = result({
    columns: ["Visitors", "Sign-ups", "Conversion %"],
    rows: [row({ Channel: "Direct" }, [200, 3, 1.5]), row({ Channel: "Email" }, [100, 6, 6]), row({ Channel: "Unattributed" }, [0, 2, null])],
  });

  it("draws one labelled bar per row, scaled to the largest, with its other columns", () => {
    render(<BarList panel={sql("channels", { display: { style: "barlist" } })} data={channels} unit="count" keys={["Channel"]} />);
    const items = within(screen.getByRole("list", { name: "channels" })).getAllByRole("listitem");
    expect(items.map((li) => li.getAttribute("data-row"))).toEqual(["Direct", "Email", "Unattributed"]);
    expect(items[0]).toHaveTextContent("Direct");
    expect(items[0]).toHaveTextContent("200");
    // A "%" column holds 0-100 and reads with a % sign; a NULL is a dash, not a zero.
    expect(items[1]).toHaveTextContent("6%");
    expect(items[2]).toHaveTextContent("—");
  });

  it("draws the column display.bar names, and follows it with its share when asked", () => {
    render(<BarList panel={sql("channels", { display: { style: "barlist", bar: "Sign-ups", share: true } })} data={channels} unit="count" keys={["Channel"]} />);
    const email = screen.getAllByRole("listitem")[1];
    // Email brought 6 of the 11 sign-ups.
    expect(email).toHaveTextContent("6");
    expect(email).toHaveTextContent("55%");
  });

  it("keeps every row behind Show all", () => {
    const many = result({ columns: ["Visitors"], rows: Array.from({ length: 20 }, (_, i) => row({ Route: `/r${i}` }, [100 - i])) });
    render(<BarList panel={sql("pages", { display: { style: "barlist" }, height: 8 })} data={many} unit="count" keys={["Route"]} />);
    const shown = screen.getAllByRole("listitem").length;
    expect(shown).toBeLessThan(20);
    fireEvent.click(screen.getByTestId("obs-barlist-all"));
    expect(screen.getAllByRole("listitem")).toHaveLength(20);
    expect(screen.getByTestId("obs-barlist-all")).toHaveTextContent("Show fewer");
  });

  it("draws a row's sparkline from a trend grouped by its label", () => {
    const trend = result({
      series: [
        { label: "Direct", group: { channel: "Direct" }, timestamps: [1, 2, 3], values: [5, 6, 7], value: 7 } as unknown as Series,
      ],
    });
    render(<BarList panel={sql("channels", { display: { style: "barlist" } })} data={channels} unit="count" keys={["Channel"]} trend={trend} />);
    // Only Direct has a trend; the others draw none.
    expect(screen.getAllByTestId("sql-chart")).toHaveLength(1);
  });

  it("is what a sql panel with rows and no time column draws as barlist, and stays a table otherwise", () => {
    const { unmount } = render(<PanelBody panel={sql("channels", { display: { style: "barlist" } })} query={ready(channels)} />);
    expect(screen.getByTestId("obs-panel-barlist")).toBeInTheDocument();
    unmount();
    render(<PanelBody panel={sql("channels")} query={ready(channels)} />);
    expect(screen.getByTestId("obs-panel-table")).toBeInTheDocument();
  });
});

describe("funnel in steps (display.layout: steps)", () => {
  const steps = [
    { event: "Visited", count: 15000 },
    { event: "Signed up", count: 48 },
    { event: "Activated", count: 30 },
  ];

  it("draws every step, the conversion between them, and calls out the weakest", () => {
    render(<StepFunnel steps={steps} label="visitors" />);
    const items = screen.getAllByRole("listitem");
    expect(items.map((li) => li.getAttribute("data-step"))).toEqual(["Visited", "Signed up", "Activated"]);
    const rates = screen.getAllByTestId("obs-step-funnel-rate");
    expect(rates).toHaveLength(2);
    expect(rates[0]).toHaveTextContent("0.3%");
    expect(rates[0].className).toContain("text-warning-ink");
    expect(screen.getByTestId("obs-panel-step-funnel")).toHaveTextContent("weakest step: Signed up");
  });

  it("draws a step nobody reached, and nothing for no steps", () => {
    const { container, rerender } = render(<StepFunnel steps={[{ event: "Visited", count: 10 }, { event: "Bought", count: 0 }]} />);
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
    rerender(<StepFunnel steps={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("draws one step with no conversion", () => {
    render(<StepFunnel steps={[{ event: "Visited", count: 10 }]} />);
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.queryAllByTestId("obs-step-funnel-rate")).toHaveLength(0);
  });

  it("is what a sql funnel with layout steps draws", () => {
    const rows = result({ columns: ["Persons"], rows: [row({ Step: "Visited" }, [9]), row({ Step: "Signed up" }, [5]), row({ Step: "Activated" }, [3])] });
    render(<PanelBody panel={sql("funnel", { display: { style: "funnel", layout: "steps" } })} query={ready(rows)} />);
    expect(screen.getByTestId("obs-panel-step-funnel")).toBeInTheDocument();
  });
});

describe("which way is good (display.good)", () => {
  it("colours an improvement green and a worsening red, by the panel's direction", () => {
    expect(toneOf("up", 120, 100)).toBe("text-success-ink");
    expect(toneOf("up", 80, 100)).toBe("text-danger-ink");
    expect(toneOf("down", 80, 100)).toBe("text-success-ink");
    expect(toneOf("down", 120, 100)).toBe("text-danger-ink");
  });

  it("stays muted with no direction, no comparison, no change, or a change under 2%", () => {
    expect(toneOf(undefined, 120, 100)).toBe("text-ink-muted");
    expect(toneOf("up", 120, null)).toBe("text-ink-muted");
    expect(toneOf("up", 100, 100)).toBe("text-ink-muted");
    expect(toneOf("up", 101, 100)).toBe("text-ink-muted");
  });
});

describe("KPI card (display.style: kpi)", () => {
  const active = result({ columns: ["Active users", "DAU", "WAU", "MAU"], rows: [row({}, [996, 120, 400, 996])] });

  it("leads with the first figure and puts the others under it", () => {
    render(<PanelBody panel={sql("active", { unit: "count", display: { style: "kpi", good: "up" } })} query={ready(active)} />);
    const card = screen.getByTestId("obs-panel-kpi");
    expect(within(card).getByTestId("obs-kpi-value")).toHaveTextContent("996");
    expect(within(card).getByTestId("obs-kpi-sub")).toHaveTextContent(/DAU\s*120.*WAU\s*400.*MAU\s*996/);
  });
});

describe("Needs attention (kind: attention)", () => {
  const watched = [
    sql("visitors", { title: "Visitors", unit: "count", display: { good: "up" } }),
    sql("bounce", { title: "Bounce rate", unit: "percent", display: { good: "down" } }),
    sql("signups", { title: "Sign-ups", unit: "count", display: { good: "up" } }),
    sql("active", { title: "Active users", unit: "count", display: { good: "up" } }),
  ];
  const attention: Panel = { id: "attention", title: "Needs attention", kind: "attention", signal: "", series: [], attention: { threshold: 0.05 } };
  const compare = { offsetKey: "period:30d", offsetMs: () => 0, label: "previous period" };
  const draw = (c: typeof compare | null = compare) =>
    render(<AttentionPanel panel={attention} watched={watched} scope={null} range="30d" dashboardId="product-overview" compare={c} enabled drillHref={(p) => `/to/${p.id}`} />);

  it("lists the figures that moved the wrong way first, then the ones that improved, and links each to its detail", () => {
    Object.assign(figuresById, { visitors: [80, 100], bounce: [0.6, 0.5], signups: [130, 100], active: [101, 100] });
    draw();
    const items = screen.getAllByRole("listitem");
    expect(items.map((li) => li.getAttribute("data-attention"))).toEqual(["bad", "bad", "good"]);
    expect(items[0]).toHaveTextContent("Visitors fell 20%");
    expect(items[1]).toHaveTextContent("Bounce rate rose 20%");
    expect(items[2]).toHaveTextContent("Sign-ups rose 30%");
    expect(within(items[0]).getByRole("link")).toHaveAttribute("href", "/to/visitors");
    // Active users moved 1%, under the 5% threshold.
    expect(screen.queryByText(/Active users/)).not.toBeInTheDocument();
    expect(screen.getByTestId("obs-panel-attention")).toHaveTextContent("2 of 4 watched figures moved the wrong way");
  });

  it("says so when nothing moved", () => {
    Object.assign(figuresById, { visitors: [100, 100], bounce: [0.5, 0.5], signups: [101, 100], active: [100, 100] });
    draw();
    expect(screen.getByTestId("obs-panel-attention")).toHaveTextContent("Nothing moved the wrong way by 5% or more");
    expect(screen.getByTestId("obs-panel-attention")).toHaveTextContent("Every watched figure is within 5%");
  });

  it("asks for a comparison when none is on", () => {
    draw(null);
    expect(screen.getByText(/Turn on Compare/)).toBeInTheDocument();
  });

  it("waits while a figure loads, and leaves out a figure with nothing to compare", () => {
    for (const key of Object.keys(figuresById)) delete figuresById[key];
    Object.assign(figuresById, { visitors: [80, 100], bounce: [0.6, null], signups: [50, 0] });
    const { rerender } = draw();
    // Active users is still loading.
    expect(screen.getByLabelText("Loading")).toBeInTheDocument();
    figuresById.active = [100, 100];
    rerender(<AttentionPanel panel={attention} watched={watched} scope={null} range="30d" dashboardId="product-overview" compare={{ ...compare }} enabled drillHref={() => null} />);
    // Bounce has no earlier figure and sign-ups grew from zero: neither is judged.
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByRole("listitem")).toHaveTextContent("Visitors fell 20%");
  });
});
