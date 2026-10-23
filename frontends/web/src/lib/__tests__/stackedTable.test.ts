import { describe, expect, it } from "vitest";
import { labelStackedTable } from "../stackedTable";

function table(html: string): HTMLTableElement {
  const host = document.createElement("div");
  host.innerHTML = html;
  return host.querySelector("table")!;
}

describe("labelStackedTable", () => {
  it("names each body cell by its column's header, leaving the title (first) cell unlabelled", () => {
    const t = table(`<table>
      <thead><tr><th>Name</th><th>Status</th><th>Ready</th></tr></thead>
      <tbody><tr><td>api</td><td>Healthy</td><td>2/2</td></tr></tbody>
    </table>`);
    labelStackedTable(t);
    const cells = Array.from(t.tBodies[0]!.rows[0]!.cells);
    expect(cells.map((c) => c.getAttribute("data-label"))).toEqual([null, "Status", "Ready"]);
  });

  it("follows a header colSpan and leaves a spanning body cell unlabelled", () => {
    const t = table(`<table>
      <thead><tr><th>Name</th><th colspan="2">Usage</th><th>Version</th></tr></thead>
      <tbody>
        <tr><td>api</td><td>cpu</td><td>mem</td><td>v1</td></tr>
        <tr><td>worker</td><td colspan="3">Nothing reports from it.</td></tr>
      </tbody>
    </table>`);
    labelStackedTable(t);
    const [full, spanned] = Array.from(t.tBodies[0]!.rows);
    expect(Array.from(full!.cells).map((c) => c.getAttribute("data-label"))).toEqual([null, "Usage", "Usage", "Version"]);
    expect(spanned!.cells[1]!.hasAttribute("data-label")).toBe(false);
  });

  it("uses screen-reader-only header text for a visually empty column", () => {
    const t = table(`<table>
      <thead><tr><th>Name</th><th><span class="sr-only">Actions</span></th></tr></thead>
      <tbody><tr><td>api</td><td><button>…</button></td></tr></tbody>
    </table>`);
    labelStackedTable(t);
    expect(t.tBodies[0]!.rows[0]!.cells[1]!.getAttribute("data-label")).toBe("Actions");
  });

  it("leads with the cell marked data-stack-title, and labels the first cell instead", () => {
    // A trace row starts with its time; its root operation is what names it.
    const t = table(`<table>
      <thead><tr><th>Started</th><th>Root operation</th><th>Duration</th></tr></thead>
      <tbody><tr><td>12:01</td><th scope="row" data-stack-title>GET /checkout</th><td>840ms</td></tr></tbody>
    </table>`);
    labelStackedTable(t);
    const cells = Array.from(t.tBodies[0]!.rows[0]!.cells);
    expect(cells.map((c) => c.getAttribute("data-label"))).toEqual(["Started", null, "Duration"]);
  });

  it("drops a stale label when a cell becomes the title", () => {
    const t = table(`<table>
      <thead><tr><th>Name</th><th>Status</th></tr></thead>
      <tbody><tr><td data-label="Name">api</td><td>ok</td></tr></tbody>
    </table>`);
    labelStackedTable(t);
    expect(t.tBodies[0]!.rows[0]!.cells[0]!.hasAttribute("data-label")).toBe(false);
  });

  it("does nothing for a table with no header", () => {
    const t = table(`<table><tbody><tr><td>a</td></tr></tbody></table>`);
    labelStackedTable(t);
    expect(t.tBodies[0]!.rows[0]!.cells[0]!.hasAttribute("data-label")).toBe(false);
  });
});
