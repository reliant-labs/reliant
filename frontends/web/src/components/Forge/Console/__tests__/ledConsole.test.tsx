// Copyright (c) 2026 Reliant Labs

/**
 * The led console's additions (ledNavExtras) and its provider: the console's
 * default, which stands aside under a host's own (the public demo wraps the
 * console's layout in one, with its Home row and agent cards).
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";

import { useConsoleNavExtras } from "../consoleNavExtras";
import { LedConsoleProvider, ledNavExtras, type LedConsoleHost } from "../ledConsole";

const INPUT = { project: "app", page: { kind: "overview" } as const, env: "prod", envs: [{ name: "prod", preview: false }] };

function NavLabels() {
  const { nav } = useConsoleNavExtras();
  return <p data-testid="labels">{nav?.(INPUT)?.items.map((item) => item.label).join(",")}</p>;
}

function renderAt(ui: React.ReactNode) {
  const root = createRootRoute({ component: () => <>{ui}</> });
  render(<RouterProvider router={createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ["/"] }) })} />);
}

describe("LedConsoleProvider", () => {
  it("adds nothing under a host's own led console: the host's rows stand", async () => {
    const host: LedConsoleHost = { navLead: [{ label: "Home", href: "/home" }] };
    renderAt(
      <LedConsoleProvider host={host}>
        <LedConsoleProvider>
          <NavLabels />
        </LedConsoleProvider>
      </LedConsoleProvider>
    );
    expect((await screen.findByTestId("labels")).textContent?.split(",")[0]).toBe("Home");
  });

  it("is the grouped nav on its own, with no Product analytics row when the project has no hub", async () => {
    renderAt(
      <LedConsoleProvider>
        <NavLabels />
      </LedConsoleProvider>
    );
    const labels = (await screen.findByTestId("labels")).textContent?.split(",") ?? [];
    expect(labels.slice(0, 4)).toEqual(["Overview", "Issues", "Performance", "Sessions"]);
    expect(labels).not.toContain("Product analytics");
  });
});

describe("ledNavExtras", () => {
  it("leads a list with its own page, and leaves a filtered list or an open item to the console", () => {
    const own = <p>own</p>;
    expect(ledNavExtras({}, {}).listPage?.("errors", own)).not.toBeNull();
    expect(ledNavExtras({}, { q: "timeout" }).listPage?.("errors", own)).toBeNull();
    expect(ledNavExtras({}, { isort: "last" }).listPage?.("errors", own)).toBeNull();
    expect(ledNavExtras({}, { trace: "t1" }).listPage?.("traces", own)).toBeNull();
    expect(ledNavExtras({}, { range: "6h" }).listPage?.("sessions", own)).toBeNull();
    // Views with no lead keep the console's own page.
    expect(ledNavExtras({}, {}).listPage?.("logs", own)).toBeNull();
  });

  it("gives each page its subtitle, but none on an open item, whose lead is What happened", () => {
    expect(ledNavExtras({}, {}).subtitle?.("traces")).toBe("Step-by-step timeline of one request");
    expect(ledNavExtras({}, { trace: "t1" }).subtitle?.("traces")).toBeNull();
    expect(ledNavExtras({}, {}).subtitle?.("activity")).toBeNull();
  });

  it("on a host page, lights no console row and draws no lead", () => {
    const extras = ledNavExtras({ hostPage: { crumbs: [{ label: "Home" }] } }, {});
    expect(extras.crumbs?.({ kind: "overview" }, [{ label: "Overview" }])).toEqual([{ label: "Home" }]);
    expect(extras.listPage?.("errors", null)).toBeNull();
    expect(extras.nav?.(INPUT)?.items.some((item) => item.active)).toBe(false);
  });
});
