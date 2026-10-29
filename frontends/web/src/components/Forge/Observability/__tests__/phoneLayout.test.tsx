// Copyright (c) 2025 Reliant Labs

/**
 * The Observability views at phone width (below `md`), where layout cannot be
 * left to CSS alone: the trace list stacks into cards led by the root
 * operation (DataTable's STACK classes: the row header leads), and the
 * waterfall puts each span's name above its bar, in rows tall enough to tap. The pixel layout is
 * pinned by e2e/mobile-console.spec.ts; this pins the structure.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";

import { NARROW_VIEWPORT_QUERY } from "@/hooks/useNarrowViewport";
import { createFixtureApi, FIXTURE_TRACE_ID } from "../__fixtures__/fixtureApi";
import { renderPanel } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

const realMatchMedia = window.matchMedia;

function phoneWidth(narrow: boolean) {
  window.matchMedia = ((query: string) => ({
    matches: narrow && query === NARROW_VIEWPORT_QUERY,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

beforeEach(() => phoneWidth(true));
afterEach(() => {
  window.matchMedia = realMatchMedia;
});

describe("traces on a phone", () => {
  it("stack, each card led by its root operation and labelled by column", async () => {
    renderPanel(createFixtureApi(), { view: "traces" });
    const table = await screen.findByTestId("obs-trace-table");
    // The header row is for screen readers only below `md`.
    expect(table.querySelector("thead")).toHaveClass("max-md:sr-only");
    const row = (await screen.findAllByTestId("obs-trace-row"))[0]!;
    const title = row.querySelector('th[scope="row"]');
    expect(title).not.toBeNull();
    // The root operation leads the card, wherever its column is.
    expect(title).toHaveClass("max-md:order-first");
    expect(title).not.toHaveAttribute("data-label");
    // Started is the first column but not the title: it is a labelled line.
    expect(row.querySelector('[data-label="Started"]')).not.toBeNull();
    expect(row.querySelector('[data-label="Duration"]')).not.toBeNull();
  });

  it("puts each span's name above its bar, in a 52px row", async () => {
    renderPanel(createFixtureApi(), { view: "traces", trace: FIXTURE_TRACE_ID });
    const tree = await screen.findByRole("tree", { name: "Spans of this trace" });
    const span = within(tree).getAllByRole("treeitem")[0]!;
    expect(span).toHaveClass("grid-cols-1");
    expect(span).not.toHaveClass("grid-cols-[minmax(12rem,2fr)_3fr]");
    expect(span.style.height).toBe("52px");
    // The name column's ruler label goes: there is no name column.
    expect(screen.getByTestId("trace-waterfall").textContent).not.toMatch(/^Span/m);
  });

  it("keeps the desktop waterfall when the viewport is wide", async () => {
    phoneWidth(false);
    renderPanel(createFixtureApi(), { view: "traces", trace: FIXTURE_TRACE_ID });
    const tree = await screen.findByRole("tree", { name: "Spans of this trace" });
    const span = within(tree).getAllByRole("treeitem")[0]!;
    expect(span).toHaveClass("grid-cols-[minmax(12rem,2fr)_3fr]");
    expect(span.style.height).toBe("28px");
  });
});
