// Copyright (c) 2025 Reliant Labs

/**
 * The database selector, the table list and the grid, against the fixture
 * shop (fixtureDataApi.ts) in an OPEN environment: what is shown, and that
 * every control changes the URL state and the BrowseTable request the way
 * dataUrl-model says.
 */

import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code } from "@connectrpc/connect";

import { createDataFixtureApi, fixtureUserId } from "../__fixtures__/fixtureDataApi";
import { browseCalls, renderBrowser, searchOf } from "./dataTestUtils";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

const gridReady = async () => waitFor(() => expect(screen.getByTestId("data-grid")).toHaveAttribute("data-state", "ready"));
const headers = () => within(screen.getByTestId("data-grid-scroll")).getAllByRole("columnheader").map((th) => th.textContent);

describe("databases and tables", () => {
  it("opens the first readable database, and shows an unreachable one disabled with its reason", async () => {
    const api = createDataFixtureApi();
    const view = renderBrowser(api);
    await waitFor(() => expect(view.search().db).toBe("shop"));
    expect(view.navigations[0]).toEqual({ search: { db: "shop" }, replace: true });

    const analytics = screen.getByTestId("data-db-analytics");
    expect(analytics).toBeDisabled();
    expect(screen.getByTestId("data-db-unreachable")).toHaveTextContent("analytics can't be browsed: external database not dialable from the control plane.");
    expect(screen.getByTestId("data-db-shop")).toHaveAttribute("aria-checked", "true");
    expect(screen.getByTestId("data-policy")).toHaveAttribute("data-policy", "open");
  });

  it("lists tables by schema with estimates and kind badges, and filters them", async () => {
    renderBrowser(createDataFixtureApi(), { db: "shop" });
    const rail = await screen.findByTestId("data-tables");
    expect(within(rail).getAllByRole("heading").map((h) => h.textContent)).toEqual(["billing", "public"]);
    expect(screen.getByTestId("data-table-public.orders")).toHaveTextContent("orders~1.2k");
    expect(screen.getByTestId("data-table-public.order_totals")).toHaveTextContent("order_totalsview");
    expect(screen.getByTestId("data-pick-table")).toHaveTextContent("Choose a table");
    expect(searchOf(screen.getByTestId("data-table-public.users").getAttribute("href"))).toEqual({ db: "shop", table: "public.users" });

    await userEvent.type(screen.getByTestId("data-table-search"), "inv");
    expect(within(rail).getAllByRole("link").map((a) => a.textContent)).toEqual(["invoices~180"]);
  });

  it("explains a table this database does not have", async () => {
    renderBrowser(createDataFixtureApi(), { db: "shop", table: "public.nope" });
    expect(await screen.findByTestId("data-table-missing")).toHaveTextContent("public.nope isn't readable in shop");
  });
});

describe("the grid", () => {
  it("shows the columns from Describe, the first page in key order, NULL as a mark and the estimated total", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    renderBrowser(api, { db: "shop", table: "public.orders" });
    await gridReady();
    // Headers say the DESCRIBED type, shortened; the full spelling is the title.
    expect(headers()).toEqual(["Open row", "idbigint", "user_iduuid", "statustext", "total_centsbigint", "notestext", "metadatajsonb", "created_attimestamptz"]);
    expect(screen.getByTestId("data-sort-created_at").title).toContain("created_at · timestamp with time zone");
    // Alignment follows the RESULT type ("int8"), never compared with the described "bigint".
    expect(screen.getByTestId("data-sort-total_cents")).toHaveClass("justify-end");
    const rows = screen.getAllByTestId("data-row");
    expect(rows).toHaveLength(50);
    expect(within(rows[0]!).getAllByRole("cell")[1]).toHaveTextContent("1000");
    // Order 1001 has no notes: NULL, drawn as a mark.
    expect(within(rows[1]!).getAllByRole("cell")[5]!.querySelector("[data-null]")).toHaveTextContent("NULL");
    expect(screen.getByTestId("data-range")).toHaveTextContent("Rows 1–50 of ~235");
    expect(browseCalls(api).at(-1)).toMatchObject({ table: "public.orders", columns: [], filters: [], sort: [], limit: 50, offset: 0, count: "estimated" });
    // The primary key sorts the default grid, and the header says so.
    expect(screen.getByTestId("data-sort-id").closest("th")).toHaveAttribute("aria-sort", "ascending");
  });

  it("sorts by a header: asc, desc, then back to the default", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    const view = renderBrowser(api, { db: "shop", table: "public.orders", page: 3 });
    await gridReady();
    fireEvent.click(screen.getByTestId("data-sort-total_cents"));
    expect(view.search()).toEqual({ db: "shop", table: "public.orders", sort: ["total_cents.asc"] });
    await waitFor(() => expect(browseCalls(api).at(-1)).toMatchObject({ sort: [{ column: "total_cents", desc: false }], offset: 0 }));
    fireEvent.click(screen.getByTestId("data-sort-total_cents"));
    expect(view.search().sort).toEqual(["total_cents.desc"]);
    fireEvent.click(screen.getByTestId("data-sort-total_cents"));
    expect(view.search().sort).toBeUndefined();
    // Shift adds a second key.
    fireEvent.click(screen.getByTestId("data-sort-status"));
    fireEvent.click(screen.getByTestId("data-sort-created_at"), { shiftKey: true });
    expect(view.search().sort).toEqual(["status.asc", "created_at.asc"]);
  });

  it("adds filters with the builder: IN takes a list, IS NULL takes nothing", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    const view = renderBrowser(api, { db: "shop", table: "public.orders" });
    await gridReady();

    await userEvent.click(screen.getByTestId("data-filter-add"));
    await userEvent.selectOptions(screen.getByTestId("data-filter-column"), "status");
    await userEvent.selectOptions(screen.getByTestId("data-filter-op"), "in");
    await userEvent.type(screen.getByTestId("data-filter-value"), "paid{enter}shipped");
    await userEvent.click(screen.getByTestId("data-filter-apply"));
    expect(view.search().f).toEqual(["status.in.paid,shipped"]);

    await userEvent.click(screen.getByTestId("data-filter-add"));
    await userEvent.selectOptions(screen.getByTestId("data-filter-column"), "notes");
    // A non-null column offers no IS NULL; a nullable one does, with no value box.
    await userEvent.selectOptions(screen.getByTestId("data-filter-op"), "not_null");
    expect(screen.queryByTestId("data-filter-value")).not.toBeInTheDocument();
    await userEvent.click(screen.getByTestId("data-filter-apply"));
    expect(view.search().f).toEqual(["status.in.paid,shipped", "notes.not_null"]);

    await waitFor(() =>
      expect(browseCalls(api).at(-1)).toMatchObject({
        filters: [
          { column: "status", op: "in", values: ["paid", "shipped"] },
          { column: "notes", op: "not_null", values: [] },
        ],
      })
    );
    await gridReady();
    expect(screen.getAllByTestId("data-filter-chip").map((chip) => chip.textContent)).toEqual(["status in (paid, shipped)", "notes is not NULL"]);
    for (const row of screen.getAllByTestId("data-row")) expect(["paid", "shipped"]).toContain(within(row).getAllByRole("cell")[3]!.textContent);

    // LIKE is offered on text only (a described type: "bigint" is not).
    await userEvent.click(screen.getByTestId("data-filter-add"));
    await userEvent.selectOptions(screen.getByTestId("data-filter-column"), "total_cents");
    expect(within(screen.getByTestId("data-filter-op")).queryByText("matches (any case)")).not.toBeInTheDocument();
    expect(within(screen.getByTestId("data-filter-op")).queryByText("is NULL")).not.toBeInTheDocument();
  });

  it("pages by offset, with has_more driving Next and the size choosable", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    const view = renderBrowser(api, { db: "shop", table: "public.orders" });
    await gridReady();
    expect(screen.getByTestId("data-page-prev")).toBeDisabled();
    fireEvent.click(screen.getByTestId("data-page-next"));
    expect(view.search().offset).toBe(50);
    await waitFor(() => expect(browseCalls(api).at(-1)).toMatchObject({ offset: 50, limit: 50 }));
    await waitFor(() => expect(screen.getByTestId("data-range")).toHaveTextContent("Rows 51–100"));

    // A new page size keeps the first row on screen.
    await userEvent.selectOptions(screen.getByTestId("data-page-size"), "200");
    expect(view.search()).toEqual({ db: "shop", table: "public.orders", offset: 50, size: 200 });
    view.go({ db: "shop", table: "public.orders", size: 200, offset: 200 });
    // The last page: no next, and the total is exact.
    await waitFor(() => expect(screen.getByTestId("data-range")).toHaveTextContent("Rows 201–230 of 230"));
    expect(screen.getByTestId("data-page-next")).toBeDisabled();
    fireEvent.click(screen.getByTestId("data-page-prev"));
    // Back a page size, to the first row (an offset of 0 leaves the URL).
    expect(view.search().offset).toBeUndefined();
  });

  it("counts exactly on request, and says when the count stopped at the server's cap", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    renderBrowser(api, { db: "shop", table: "public.events" });
    await gridReady();
    expect(screen.getByTestId("data-total")).toHaveTextContent("~26k");
    fireEvent.click(screen.getByTestId("data-count-exact"));
    await waitFor(() => expect(screen.getByTestId("data-total")).toHaveTextContent("10,000+"));
    expect(browseCalls(api).at(-1)).toMatchObject({ count: "exact" });
  });

  it("opens a cell's whole value, pretty-printed, with Copy", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    renderBrowser(createDataFixtureApi({ onlyShop: true }), { db: "shop", table: "public.orders" });
    await gridReady();
    const metadata = within(screen.getAllByTestId("data-row")[0]!).getAllByTestId("data-cell").find((cell) => cell.closest("td")?.dataset.column === "metadata")!;
    fireEvent.click(metadata);
    const popover = await screen.findByTestId("data-cell-popover");
    expect(within(popover).getByTestId("data-cell-full").textContent).toBe('{\n  "channel": "mobile",\n  "coupon": "AUTUMN"\n}');
    fireEvent.click(within(popover).getByTestId("data-cell-copy"));
    expect(writeText).toHaveBeenCalledWith('{\n  "channel": "mobile",\n  "coupon": "AUTUMN"\n}');
    expect(await within(popover).findByText("Copied")).toBeInTheDocument();
  });

  it("links a foreign-key cell to the referenced row, and each row to its row page", async () => {
    renderBrowser(createDataFixtureApi({ onlyShop: true }), { db: "shop", table: "public.orders" });
    await gridReady();
    const first = screen.getAllByTestId("data-row")[0]!;
    const fk = within(first).getByTestId("data-fk-link");
    expect(fk).toHaveTextContent(fixtureUserId(0));
    expect(searchOf(fk.getAttribute("href"))).toEqual({ db: "shop", table: "public.users", row: fixtureUserId(0) });
    expect(searchOf(within(first).getByTestId("data-row-open").getAttribute("href"))).toEqual({ db: "shop", table: "public.orders", row: "1000" });
  });

  it("opens a row on the grid's whole view, so the row page's Back returns to that view", async () => {
    const grid = { db: "shop", table: "public.orders", f: ["status.eq.pending"], sort: ["id.desc"], cols: ["id", "status", "total_cents"], offset: 25, size: 25 };
    const view = renderBrowser(createDataFixtureApi({ onlyShop: true }), grid);
    await gridReady();
    const open = searchOf(within(screen.getAllByTestId("data-row")[0]!).getByTestId("data-row-open").getAttribute("href"));
    expect(open).toEqual({ ...grid, row: open.row });
    expect(open.row).toBeDefined();

    view.go(open);
    await waitFor(() => expect(screen.getByTestId("data-row-page")).toHaveAttribute("data-state", "ready"));
    expect(searchOf(screen.getByTestId("data-row-back").getAttribute("href"))).toEqual(grid);
  });

  it("hides columns with the picker, still reading the key so rows open", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    const view = renderBrowser(api, { db: "shop", table: "public.orders" });
    await gridReady();
    fireEvent.click(screen.getByTestId("data-columns"));
    fireEvent.click(screen.getByTestId("data-column-id"));
    fireEvent.click(screen.getByTestId("data-column-metadata"));
    expect(view.search().cols).toEqual(["user_id", "status", "total_cents", "notes", "created_at"]);
    await waitFor(() => expect(browseCalls(api).at(-1)).toMatchObject({ columns: ["user_id", "status", "total_cents", "notes", "created_at", "id"] }));
    await waitFor(() => expect(headers()).toEqual(["Open row", "user_iduuid", "statustext", "total_centsbigint", "notestext", "created_attimestamptz"]));
    expect(searchOf(within(screen.getAllByTestId("data-row")[0]!).getByTestId("data-row-open").getAttribute("href")).row).toBe("1000");
  });

  it("pages from next_offset when the server cut a page short, skipping nothing", async () => {
    // A cap that fits about 7 order rows of JSON per response.
    const api = createDataFixtureApi({ onlyShop: true, responseByteCap: 1500 });
    const view = renderBrowser(api, { db: "shop", table: "public.orders" });
    await gridReady();
    const shown = screen.getAllByTestId("data-row").length;
    expect(shown).toBeGreaterThan(1);
    expect(shown).toBeLessThan(50);
    expect(screen.getByTestId("data-page-cut")).toHaveTextContent(`The server cut this page at ${shown} of 50 rows`);
    expect(screen.getByTestId("data-page-cut")).toHaveTextContent(`Next continues from row ${shown + 1}`);
    expect(screen.getByTestId("data-range")).toHaveTextContent(`Rows 1–${shown}`);

    fireEvent.click(screen.getByTestId("data-page-next"));
    // offset + rows returned, not offset + limit.
    expect(view.search().offset).toBe(shown);
    await waitFor(() => expect(browseCalls(api).at(-1)).toMatchObject({ offset: shown, limit: 50 }));
    await waitFor(() => expect(within(screen.getAllByTestId("data-row")[0]!).getAllByRole("cell")[1]).toHaveTextContent(String(1000 + shown)));
    expect(screen.getByTestId("data-range")).toHaveTextContent(`Rows ${shown + 1}–`);
  });

  it("opens an old page= link on the rows it meant, and writes it as an offset on the next change", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    const view = renderBrowser(api, { db: "shop", table: "public.orders", page: 3, size: 25 });
    await gridReady();
    expect(browseCalls(api).at(-1)).toMatchObject({ offset: 50, limit: 25 });
    expect(screen.getByTestId("data-range")).toHaveTextContent("Rows 51–75");
    fireEvent.click(screen.getByTestId("data-page-next"));
    expect(view.search()).toEqual({ db: "shop", table: "public.orders", offset: 75, size: 25 });
  });

  it("marks a cell the server cut at 4 KiB by its marker, with truncated_cells still empty", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    renderBrowser(api, { db: "shop", table: "public.users", f: ["email.eq.user2@example.com"] });
    await gridReady();
    const profile = within(screen.getAllByTestId("data-row")[0]!).getAllByRole("cell").find((td) => td.dataset.column === "profile")!;
    expect(profile).toHaveAttribute("data-server-cut", "true");
    expect(within(profile).getByTestId("data-cell-cut")).toHaveTextContent("cut");
    // Only the cut cell: its neighbours are whole.
    expect(screen.getAllByTestId("data-cell-cut")).toHaveLength(1);

    fireEvent.click(within(profile).getByTestId("data-cell"));
    const popover = await screen.findByTestId("data-cell-popover");
    expect(within(popover).getByTestId("data-cell-cut-note")).toHaveTextContent(/Cut by the server: 4,096 of [\d,]+ bytes shown/);
    // The marker is the note's job, not part of the value; a cut json value is shown as the text that arrived.
    expect(within(popover).getByTestId("data-cell-full").textContent).not.toContain("[truncated:");
    expect(within(popover).getByTestId("data-cell-full").textContent!.startsWith('{"plan":')).toBe(true);
    expect(within(popover).getByTestId("data-cell-copy")).toHaveTextContent("Copy what's shown");
  });

  it("marks the cells truncated_cells names, by their index into the result's columns", async () => {
    // A server that names cut cells: here index 0 of the result (email), a cell with no marker in its text.
    const fixture = createDataFixtureApi({ onlyShop: true });
    const api = {
      ...fixture,
      browseTable: async (...args: Parameters<typeof fixture.browseTable>) => {
        const page = await fixture.browseTable(...args);
        return { ...page, truncatedCells: page.rows.map(() => [0]) };
      },
    };
    renderBrowser(api, { db: "shop", table: "public.users", f: ["email.eq.user5@example.com"], cols: ["email", "name"] });
    await gridReady();
    const cut = screen.getAllByTestId("data-cell-cut");
    expect(cut).toHaveLength(1);
    expect(cut[0]!.closest("td")).toHaveAttribute("data-column", "email");
    fireEvent.click(within(cut[0]!.closest("td")!).getByTestId("data-cell"));
    expect(await screen.findByTestId("data-cell-cut-note")).toHaveTextContent("Cut by the server at 4 KiB: the rest of this value is not readable here.");
  });

  it("gives a view no row links, and says why", async () => {
    renderBrowser(createDataFixtureApi({ onlyShop: true }), { db: "shop", table: "public.order_totals" });
    await gridReady();
    expect(screen.getByTestId("data-no-key")).toBeInTheDocument();
    expect(screen.queryByTestId("data-row-open")).not.toBeInTheDocument();
  });

  it("shows the database's refusal of a filter value in its own words, with a way out", async () => {
    const view = renderBrowser(createDataFixtureApi({ onlyShop: true }), { db: "shop", table: "public.orders", f: ["total_cents.eq.abc"] });
    const error = await screen.findByTestId("data-grid-error");
    expect(error).toHaveAttribute("data-kind", "invalid");
    expect(error).toHaveTextContent('invalid input syntax for type bigint: "abc"');
    fireEvent.click(within(error).getByRole("button", { name: "Clear filters" }));
    expect(view.search().f).toBeUndefined();
  });

  it("designs its empty states: no match, and a page past the end", async () => {
    const view = renderBrowser(createDataFixtureApi({ onlyShop: true }), { db: "shop", table: "public.orders", f: ["status.eq.lost"] });
    expect(await screen.findByTestId("data-empty-filtered")).toHaveTextContent("No rows match these filters");
    view.go({ db: "shop", table: "public.orders", offset: 4900 });
    expect(await screen.findByTestId("data-empty-page")).toHaveTextContent("Nothing past row 4,900");
  });

  it("names the db:query grant when the caller may not read", async () => {
    renderBrowser(createDataFixtureApi({ fail: { method: "listBrowsableDatabases", code: Code.PermissionDenied } }));
    const error = await screen.findByTestId("data-databases-error");
    expect(error).toHaveAttribute("data-kind", "denied");
    expect(error).toHaveTextContent("You can't read staging's data");
    expect(error).toHaveTextContent("db:query");
  });
});
