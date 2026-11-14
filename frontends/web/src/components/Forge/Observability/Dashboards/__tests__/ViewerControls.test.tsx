// Copyright (c) 2025 Reliant Labs
import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createFixtureApi } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import type { Dashboard } from "@/services/controlPlane/observability/types";
import { filterLabel, ViewerFilters, ViewOptions } from "../ViewerControls";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const dashboard: Dashboard = {
  id: "health", title: "Health", filters: [], annotations: [{ source: "releases" }],
  sections: [{ id: "activity", title: "Activity" }, { id: "financial", title: "Financial" }],
  panels: [{ id: "requests", title: "Requests", section: "activity", kind: "line", signal: "traces", series: [], groupBy: ["service.name"] }],
};

describe("viewer controls", () => {
  it("keep grouping and section/metric visibility in one View options popover, as URL patches", async () => {
    const onSearch = vi.fn();
    const user = userEvent.setup();
    renderWithApi(createFixtureApi(), <ViewOptions dashboard={dashboard} search={{ hiddenPanels: ["requests"] }} onSearch={onSearch} />);
    // One panel is hidden: the trigger says one thing differs from the saved dashboard.
    await user.click(screen.getByTestId("dashboard-view-options"));
    expect(screen.getByTestId("dashboard-view-options")).toHaveTextContent("1");
    await user.selectOptions(screen.getByLabelText("Breakdown"), "none");
    expect(onSearch).toHaveBeenLastCalledWith({ breakdown: "none" });
    await user.click(within(screen.getByTestId("dashboard-sections")).getByRole("checkbox", { name: "Activity" }));
    expect(onSearch).toHaveBeenLastCalledWith({ hiddenSections: ["activity"] });
    await user.click(within(screen.getByTestId("dashboard-metrics")).getByRole("checkbox", { name: /Requests/ }));
    expect(onSearch).toHaveBeenLastCalledWith({ hiddenPanels: undefined });
    await user.click(screen.getByRole("checkbox", { name: /Alert bands/ }));
    expect(onSearch).toHaveBeenLastCalledWith({ alerts: false });
  });

  it("name a filter in the console's words, and leave out an environment filter the scope already decides", () => {
    expect(filterLabel({ label: "deployment.environment.name", field: "deployment.environment.name" })).toBe("Environment");
    expect(filterLabel({ label: "RPC method", field: "span.attr.rpc.method" })).toBe("RPC method");
    const withEnv: Dashboard = { ...dashboard, filters: [{ id: "env", label: "deployment.environment.name", field: "deployment.environment.name" }] };
    const { unmount } = renderWithApi(createFixtureApi(), <ViewerFilters dashboard={withEnv} scope={null} range="1h" search={{}} onSearch={vi.fn()} oneEnv />);
    expect(screen.queryByLabelText("Environment")).not.toBeInTheDocument();
    unmount();
    renderWithApi(createFixtureApi(), <ViewerFilters dashboard={withEnv} scope={null} range="1h" search={{}} onSearch={vi.fn()} oneEnv={false} />);
    expect(screen.getByLabelText("Environment")).toBeInTheDocument();
    expect(screen.queryByText("deployment.environment.name")).not.toBeInTheDocument();
  });
});
