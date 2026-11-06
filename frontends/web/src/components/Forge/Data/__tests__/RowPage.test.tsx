// Copyright (c) 2025 Reliant Labs

/**
 * The row page: one row by its key (composite keys too), its foreign keys as
 * links, and Related — one entry per table pointing at it, each counted with
 * a `limit 1, count ESTIMATED` read and linked to its filtered grid.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";

import { createDataFixtureApi, fixtureUserId } from "../__fixtures__/fixtureDataApi";
import { RELATED_CONCURRENCY } from "../dataLinks-model";
import { browseCalls, renderBrowser, searchOf } from "./dataTestUtils";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

const ready = async () => waitFor(() => expect(screen.getByTestId("data-row-page")).toHaveAttribute("data-state", "ready"));

describe("the row page", () => {
  it("reads the row by its key and shows every column, its foreign key a link", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    renderBrowser(api, { db: "shop", table: "public.orders", row: "1004", f: ["status.eq.pending"] });
    await ready();
    expect(screen.getByTestId("data-row-title")).toHaveTextContent("id 1004");
    expect(browseCalls(api)[0]).toMatchObject({ table: "public.orders", columns: [], filters: [{ column: "id", op: "eq", values: ["1004"] }], limit: 1, count: "none" });

    const fields = screen.getByTestId("data-row-fields");
    expect(within(fields).getAllByRole("term").map((dt) => dt.querySelector(".font-mono")?.textContent)).toEqual([
      "id",
      "user_id",
      "status",
      "total_cents",
      "notes",
      "metadata",
      "created_at",
    ]);
    const user = within(screen.getByTestId("data-field-user_id")).getByTestId("data-field-link");
    expect(user).toHaveTextContent(fixtureUserId(4));
    expect(searchOf(user.getAttribute("href"))).toEqual({ db: "shop", table: "public.users", row: fixtureUserId(4) });
    expect(within(screen.getByTestId("data-field-user_id")).getByTestId("data-field-ref")).toHaveTextContent("references public.users");
    // jsonb pretty-printed in full; NULL as a mark.
    expect(within(screen.getByTestId("data-field-metadata")).getByText(/"channel": "mobile"/)).toBeInTheDocument();
    expect(screen.getByTestId("data-field-notes").querySelector("[data-null]")).toHaveTextContent("NULL");

    // Back keeps the grid's state.
    expect(searchOf(screen.getByTestId("data-row-back").getAttribute("href"))).toEqual({ db: "shop", table: "public.orders", f: ["status.eq.pending"] });
  });

  it("lists Related: each referencing table, its key columns, a count and a filtered link", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    renderBrowser(api, { db: "shop", table: "public.orders", row: "1004" });
    await ready();
    const items = await screen.findByTestId("data-related-public.order_items.order_items_order_id_fkey");
    const invoices = screen.getByTestId("data-related-billing.invoices.invoices_order_id_fkey");
    expect(items).toHaveTextContent("order_id → id");
    // Order 1004 has two items, counted exactly, and, still pending, no invoice.
    await waitFor(() => expect(within(items).getByTestId("data-related-count")).toHaveTextContent("2 rows"));
    expect(within(items).getByTestId("data-related-count")).not.toHaveTextContent("~");
    await waitFor(() => expect(within(invoices).getByTestId("data-related-count")).toHaveTextContent("none"));
    expect(searchOf(within(items).getByTestId("data-related-link").getAttribute("href"))).toEqual({ db: "shop", table: "public.order_items", f: ["order_id.eq.1004"] });

    const counts = browseCalls(api).filter((req) => req.limit === 1 && req.count === "exact");
    expect(counts.map((req) => req.table).sort()).toEqual(["billing.invoices", "public.order_items"]);
    expect(counts[0]).toMatchObject({ limit: 1, count: "exact" });
  });

  it("says a Related count past the server's cap as at least that many", async () => {
    // A cap of 10 stands in for the server's 10,000; user 3 has 625 events.
    renderBrowser(createDataFixtureApi({ onlyShop: true, exactCap: 10 }), { db: "shop", table: "public.users", row: fixtureUserId(3) });
    await ready();
    const events = await screen.findByTestId("data-related-public.events.events_user_id_fkey");
    await waitFor(() => expect(within(events).getByTestId("data-related-count")).toHaveTextContent("10+ rows"));
  });

  it("fires the Related counts at most RELATED_CONCURRENCY at a time", async () => {
    // Twelve tables point at a user, each count a read that takes a moment.
    const api = createDataFixtureApi({ onlyShop: true, delayMs: 20, extraReferences: 10 });
    renderBrowser(api, { db: "shop", table: "public.users", row: fixtureUserId(3) });
    await ready();
    expect(screen.getAllByTestId("data-related-count")).toHaveLength(12);
    await waitFor(() => expect(screen.getAllByTestId("data-related-count").every((el) => !el.querySelector("[aria-label='Counting']"))).toBe(true), { timeout: 3000 });
    expect(browseCalls(api).filter((req) => req.count === "exact")).toHaveLength(12);
    expect(api.maxInFlight).toBe(RELATED_CONCURRENCY);
  });

  it("addresses a composite key in key order", async () => {
    const api = createDataFixtureApi({ onlyShop: true });
    renderBrowser(api, { db: "shop", table: "public.order_items", row: "1004,2" });
    await ready();
    expect(screen.getByTestId("data-row-title")).toHaveTextContent("order_id 1004 · line 2");
    expect(browseCalls(api)[0]).toMatchObject({
      filters: [
        { column: "order_id", op: "eq", values: ["1004"] },
        { column: "line", op: "eq", values: ["2"] },
      ],
    });
    expect(searchOf(within(screen.getByTestId("data-field-order_id")).getByTestId("data-field-link").getAttribute("href"))).toEqual({
      db: "shop",
      table: "public.orders",
      row: "1004",
    });
  });

  it("says when the row is gone, and when the link does not fit the key", async () => {
    const view = renderBrowser(createDataFixtureApi({ onlyShop: true }), { db: "shop", table: "public.orders", row: "999999" });
    expect(await screen.findByTestId("data-row-missing")).toHaveTextContent("No row in public.orders has id 999999");
    view.go({ db: "shop", table: "public.order_items", row: "1004" });
    expect(await screen.findByTestId("data-row-bad-address")).toHaveTextContent("(order_id, line)");
  });
});
