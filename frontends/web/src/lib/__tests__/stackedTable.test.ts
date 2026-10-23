import { describe, expect, it } from "vitest";
import { labelStackedTable } from "../stackedTable";

function table(html: string): HTMLTableElement {
  const host = document.createElement("div");
  host.innerHTML = html;
  return host.querySelector("table")!;
}

describe("labelStackedTable", () => {
  it("names each body cell by its column's header", () => {
    const t = table(`<table>
      <thead><tr><th>Name</th><th>Status</th><th>Ready</th></tr></thead>
      <tbody><tr><td>api</td><td>Healthy</td><td>2/2</td></tr></tbody>
    </table>`);
    labelStackedTable(t);
    const cells = Array.from(t.tBodies[0]!.rows[0]!.cells);
    expect(cells.map((c) => c.getAttribute("data-label"))).toEqual(["Name", "Status", "Ready"]);
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
    expect(Array.from(full!.cells).map((c) => c.getAttribute("data-label"))).toEqual(["Name", "Usage", "Usage", "Version"]);
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

  it("does nothing for a table with no header", () => {
    const t = table(`<table><tbody><tr><td>a</td></tr></tbody></table>`);
    labelStackedTable(t);
    expect(t.tBodies[0]!.rows[0]!.cells[0]!.hasAttribute("data-label")).toBe(false);
  });
});
