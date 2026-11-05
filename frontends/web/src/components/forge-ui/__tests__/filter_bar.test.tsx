import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { FilterBar, FilterChip, FilterMenu, FilterSearch } from "../filter_bar";

describe("FilterBar", () => {
  it("draws a set chip filled and an unset one plain, each with its count", () => {
    render(
      <FilterBar label="Filter services">
        <FilterChip on onClick={() => undefined} count={2} testId="on">
          Needs attention
        </FilterChip>
        <FilterChip on={false} onClick={() => undefined} count={6} testId="off">
          All
        </FilterChip>
      </FilterBar>
    );
    expect(screen.getByRole("group", { name: "Filter services" })).toBeInTheDocument();
    expect(screen.getByTestId("on")).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByTestId("on").className).toContain("bg-accent/10");
    expect(screen.getByTestId("on")).toHaveTextContent("Needs attention2");
    expect(screen.getByTestId("off")).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByTestId("off").className).not.toContain("bg-accent/10");
  });

  it("names how many filters are set, and clears them", async () => {
    const onClear = vi.fn();
    const { rerender } = render(
      <FilterBar label="Filter" active={0} onClear={onClear}>
        <FilterSearch value="" onChange={() => undefined} placeholder="Search" />
      </FilterBar>
    );
    expect(screen.queryByRole("button", { name: /Clear filters/ })).toBeNull();
    rerender(
      <FilterBar label="Filter" active={2} onClear={onClear}>
        <FilterSearch value="api" onChange={() => undefined} placeholder="Search" />
      </FilterBar>
    );
    await userEvent.click(screen.getByRole("button", { name: /Clear filters · 2/ }));
    expect(onClear).toHaveBeenCalledOnce();
  });

  it("reads a menu's dimension while unset and its value once set, with option counts and a clear", async () => {
    const onChange = vi.fn();
    const options = [
      { value: "deployment", label: "Deployment", count: 4 },
      { value: "statefulset", label: "StatefulSet", count: 1 },
    ];
    const { rerender } = render(<FilterMenu label="Kind" options={options} onChange={onChange} testId="kind" />);
    expect(screen.getByTestId("kind")).toHaveTextContent(/^Kind$/);
    await userEvent.click(screen.getByTestId("kind"));
    // Counts are drawn, but an option's name is its label alone.
    expect(screen.getByRole("menuitemradio", { name: "StatefulSet" })).toHaveTextContent("StatefulSet1");
    await userEvent.click(screen.getByRole("menuitemradio", { name: "StatefulSet" }));
    expect(onChange).toHaveBeenLastCalledWith("statefulset");

    rerender(<FilterMenu label="Kind" value="statefulset" options={options} onChange={onChange} testId="kind" />);
    expect(screen.getByTestId("kind")).toHaveTextContent("Kind: StatefulSet");
    await userEvent.click(screen.getByRole("button", { name: "Clear kind filter" }));
    expect(onChange).toHaveBeenLastCalledWith(undefined);
  });
});
