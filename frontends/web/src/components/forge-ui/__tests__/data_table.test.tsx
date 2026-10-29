import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import DataTable, { type DataTableColumn } from "../data_table";

type Row = { id: string; name: string; status: string };

const ROWS: Row[] = [
  { id: "a", name: "api", status: "Healthy" },
  { id: "b", name: "worker", status: "Degraded" },
];

const COLUMNS: DataTableColumn<Row>[] = [
  { key: "name", header: "Name" },
  { key: "status", header: "Status" },
  { key: "actions", header: "Actions", srHeader: true, interactive: true, render: () => <button type="button">Restart</button> },
];

describe("DataTable stacked", () => {
  it("labels every cell but the title with its column header, so a phone can read it as a card", () => {
    render(<DataTable stacked columns={COLUMNS} data={ROWS} getRowKey={(row) => row.id} />);
    const [, first] = screen.getAllByRole("row");
    const cells = within(first!).getAllByRole("cell");
    expect(cells.map((cell) => cell.getAttribute("data-label"))).toEqual([null, "Status", "Actions"]);
  });

  it("leads the card with the stackTitle column instead of the first", () => {
    render(
      <DataTable
        stacked
        columns={[{ key: "status", header: "Status" }, { key: "name", header: "Name", stackTitle: true }]}
        data={ROWS}
        getRowKey={(row) => row.id}
      />,
    );
    const [, first] = screen.getAllByRole("row");
    const cells = within(first!).getAllByRole("cell");
    expect(cells.map((cell) => cell.getAttribute("data-label"))).toEqual(["Status", null]);
  });

  it("labels nothing when not stacked", () => {
    const { container } = render(<DataTable columns={COLUMNS} data={ROWS} getRowKey={(row) => row.id} />);
    expect(container.querySelector("[data-label]")).toBeNull();
  });
});

describe("DataTable row targets", () => {
  it("names a row's action by getRowLabel, and keeps an interactive cell's own controls", async () => {
    const open = vi.fn();
    render(
      <DataTable
        columns={[{ key: "name", header: "Name", render: (_, row) => <>{row.name} <span>v1.2.3</span></> }, COLUMNS[2]!]}
        data={ROWS}
        getRowKey={(row) => row.id}
        getRowAction={(row) => () => open(row.id)}
        getRowLabel={(row) => row.name}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "worker" }));
    expect(open).toHaveBeenCalledWith("b");
    expect(screen.getAllByRole("button", { name: "Restart" })).toHaveLength(2);
  });
});
