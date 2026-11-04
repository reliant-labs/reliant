// Copyright (c) 2025 Reliant Labs
import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createFixtureApi } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import type { Dashboard } from "@/services/controlPlane/observability/types";
import { ViewerControls } from "../ViewerControls";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const dashboard: Dashboard = {
  id: "health", title: "Health", filters: [], annotations: [{ source: "releases" }],
  sections: [{ id: "activity", title: "Activity" }, { id: "financial", title: "Financial" }],
  panels: [{ id: "requests", title: "Requests", section: "activity", kind: "line", signal: "traces", series: [], groupBy: ["service.name"] }],
};

describe("viewer controls", () => {
  it("exposes supported grouping and independent section/metric visibility as URL patches", async () => {
    const onSearch = vi.fn();
    const user = userEvent.setup();
    renderWithApi(createFixtureApi(), <ViewerControls dashboard={dashboard} scope={null} range="1h" search={{ hiddenPanels: ["requests"] }} onSearch={onSearch} />);
    await user.selectOptions(screen.getByLabelText("breakdown"), "none");
    expect(onSearch).toHaveBeenLastCalledWith({ breakdown: "none" });
    await user.click(screen.getByTestId("dashboard-sections"));
    await user.click(screen.getByRole("menuitemcheckbox", { name: "Activity" }));
    expect(onSearch).toHaveBeenLastCalledWith({ hiddenSections: ["activity"] });
    await user.click(screen.getByTestId("dashboard-metrics"));
    await user.click(screen.getByRole("menuitemcheckbox", { name: /Requests/ }));
    expect(onSearch).toHaveBeenLastCalledWith({ hiddenPanels: undefined });
    expect(screen.queryByLabelText("RPC service")).not.toBeInTheDocument();
  });
});
