// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Pins the URL shape of the generated CRUD routes (src/lib/entity-routes.ts).
// The detail and edit pages live at static paths and read the id from the
// query string; a helper that put the id back in the path would link to a
// page `output: "export"` never wrote.

import { useSearchParams } from "next/navigation";
import { afterEach, describe, expect, it, vi } from "vitest";

import { entityEditHref, entityViewHref, useEntityIdParam } from "./entity-routes";

vi.mock("next/navigation", () => ({ useSearchParams: vi.fn() }));

function searchParams(query: string) {
  vi.mocked(useSearchParams).mockReturnValue(
    new URLSearchParams(query) as unknown as ReturnType<typeof useSearchParams>,
  );
}

describe("entity routes", () => {
  afterEach(() => {
    vi.mocked(useSearchParams).mockReset();
  });

  it("puts the id in the query string of a static route", () => {
    expect(entityViewHref("books", "b-1")).toBe("/books/view?id=b-1");
    expect(entityEditHref("books", "b-1")).toBe("/books/edit?id=b-1");
  });

  it("encodes an id that is not URL-safe", () => {
    expect(entityViewHref("books", "a/b c&d")).toBe("/books/view?id=a%2Fb%20c%26d");
  });

  it("accepts numeric and 64-bit keys", () => {
    expect(entityViewHref("orders", 42)).toBe("/orders/view?id=42");
    expect(entityEditHref("orders", 9007199254740993n)).toBe("/orders/edit?id=9007199254740993");
  });

  it("reads back the id it wrote", () => {
    searchParams(entityViewHref("books", "a/b c&d").split("?")[1] ?? "");
    expect(useEntityIdParam()).toBe("a/b c&d");
  });

  it("reports a missing or empty id as undefined", () => {
    searchParams("");
    expect(useEntityIdParam()).toBeUndefined();
    searchParams("id=");
    expect(useEntityIdParam()).toBeUndefined();
  });
});
