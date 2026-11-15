// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// <Resource>'s error rung. This package's rule for SHOWING an error is
// userMessage(err) — never err.message — because connect-es frames
// `ConnectError.message` with the gRPC code ("[not_found] no such job"), and
// that framing is transport noise a user must never read. <Resource> is the
// list container every scaffolded page renders, so it is the one place that
// rule matters most; it used to print the raw message, framing and all, in a
// monospace "debug" style.
//
// Rendered with react-dom/server rather than a DOM: the error rung is static
// markup — no effect, no interaction — so the server renderer produces the
// whole of it, and the package still carries no jsdom (see
// service-hooks.test.ts).
import { Code, ConnectError } from "@connectrpc/connect";
import { renderToStaticMarkup } from "react-dom/server";
import type { KeyboardEvent, MouseEvent } from "react";
import { describe, expect, it, vi } from "vitest";

import { Resource, rowInteraction, type ResourceColumn } from "./resource.js";

interface Row {
  id: string;
}

const columns: ResourceColumn<Row>[] = [
  { header: "Name", cell: (row) => row.id },
];

function renderErrorState(error: Error | null | undefined): string {
  return renderToStaticMarkup(
    <Resource<Row>
      status="error"
      data={undefined}
      columns={columns}
      rowKey={(row) => row.id}
      error={error}
    />,
  );
}

describe("<Resource> error state", () => {
  it("shows userMessage(error), not the code-framed ConnectError.message", () => {
    const err = new ConnectError("no such job", Code.NotFound);
    // The premise: the raw message really does carry the framing.
    expect(err.message).toBe("[not_found] no such job");

    const html = renderErrorState(err);

    expect(html).toContain("no such job");
    expect(html).not.toContain("[not_found]");
  });

  it("strips framing from a plain Error too", () => {
    const html = renderErrorState(new Error("[internal] database unavailable"));

    expect(html).toContain("database unavailable");
    expect(html).not.toContain("[internal]");
  });

  it("falls back to presentable copy when the error has no message", () => {
    const html = renderErrorState(new Error(""));

    expect(html).toContain("Something went wrong. Please try again.");
  });

  it("renders the message as body copy, not monospace debug output", () => {
    const html = renderErrorState(new Error("no such job"));

    expect(html).not.toContain("font-mono");
  });

  it("shows only the heading when no error is passed", () => {
    const html = renderErrorState(undefined);

    expect(html).toContain("Couldn&#x27;t load data");
    expect(html).not.toContain("Something went wrong");
  });
});

describe("<Resource> table, toolbar and footer", () => {
  const rows: Row[] = [{ id: "a" }, { id: "b" }];

  function render(
    props: Partial<Parameters<typeof Resource<Row>>[0]> = {},
  ): string {
    return renderToStaticMarkup(
      <Resource<Row>
        status="success"
        data={rows}
        columns={columns}
        rowKey={(row) => row.id}
        {...props}
      />,
    );
  }

  it("keeps table-row semantics on rows that link to their record", () => {
    // A <tr role="button"> is announced as a button, not a row of data. When
    // the page renders a link in the primary cell, the row click is a
    // pointer convenience and the link is the accessible action.
    const html = render({ onRowClick: () => {}, rowsHaveLinks: true });
    expect(html).not.toContain('role="button"');
    expect(html).not.toContain("tabindex");
  });

  it("keeps a row with onRowClick and no link keyboard operable", () => {
    // A page written before rowsHaveLinks existed (the scaffold-once list
    // pages of earlier releases) has no cell link: the row is its only way
    // to the record, so it must stay a tab stop.
    const html = render({ onRowClick: () => {} });
    expect(html).toContain('role="button"');
    expect(html).toContain('tabindex="0"');
  });

  it("renders the toolbar in its own row, not in the header", () => {
    const html = render({
      title: "Jobs",
      actions: <a href="/jobs/new">New job</a>,
      toolbar: <select aria-label="Filter by status" />,
    });
    const header = html.slice(
      html.indexOf("<header"),
      html.indexOf("</header>"),
    );
    expect(header).toContain("New job");
    expect(header).not.toContain("Filter by status");
    expect(html).toContain("Filter by status");
  });

  it("shows the record range and total in the footer", () => {
    expect(render({ totalCount: 12, pageStart: 10 })).toContain("11–12 of 12");
    expect(render({ totalCount: 2 })).toContain("2 results");
  });

  it("hides the pager when there is only one page", () => {
    const single = render({
      onNextPage: () => {},
      onPrevPage: () => {},
      hasNextPage: false,
      hasPrevPage: false,
    });
    expect(single).not.toContain(">Next<");
    const paged = render({
      onNextPage: () => {},
      onPrevPage: () => {},
      hasNextPage: true,
      hasPrevPage: false,
    });
    expect(paged).toContain(">Next<");
  });

  it("marks the sorted column with aria-sort and makes sortable headers buttons", () => {
    const sortable: ResourceColumn<Row>[] = [
      { header: "Name", cell: (row) => row.id, sortKey: "name" },
      { header: "Plain", cell: (row) => row.id },
    ];
    const html = render({
      columns: sortable,
      sort: { key: "name", descending: true },
      onSortChange: () => {},
    });
    expect(html).toContain('aria-sort="descending"');
    expect(html.match(/<th\s[^>]*>/g)?.[1]).not.toContain("aria-sort");
    expect(html).toMatch(/<button type="button"[^>]*>Name/);
  });
});

// The row's event handlers, driven with hand-built events: this package
// carries no jsdom, and rowInteraction is the whole of a row's behaviour.
describe("rowInteraction", () => {
  const row: Row = { id: "a" };

  function keyEvent(key: string, onRow = true) {
    const target = {};
    let prevented = false;
    return {
      event: {
        key,
        target,
        currentTarget: onRow ? target : {},
        preventDefault: () => {
          prevented = true;
        },
      } as unknown as KeyboardEvent<HTMLTableRowElement>,
      prevented: () => prevented,
    };
  }

  // A body row holding one control (the cell link or a menu button).
  const control = {} as Element;
  const tr = {
    contains: (el: Element) => el === (tr as unknown) || el === control,
  } as unknown as Element;

  // clickOn is a click on the row whose target's closest() interactive
  // match is `closest`: null for a plain cell, `control` for the row's own
  // control, `tr` itself when the row is role="button".
  function clickOn(closest: Element | null) {
    return {
      target: { closest: () => closest },
      currentTarget: tr,
    } as unknown as MouseEvent<HTMLTableRowElement>;
  }

  const noSelection = { getSelection: () => ({ toString: () => "" }) };

  it("is inert without onRowClick", () => {
    expect(rowInteraction(row, undefined, false)).toEqual({});
    expect(rowInteraction(row, undefined, true)).toEqual({});
  });

  it("opens the record from the keyboard when the row has no link", () => {
    const opened: Row[] = [];
    const props = rowInteraction(row, (r: Row) => opened.push(r), false);
    expect(props.role).toBe("button");
    expect(props.tabIndex).toBe(0);

    for (const key of ["Enter", " "]) {
      const { event, prevented } = keyEvent(key);
      props.onKeyDown?.(event);
      expect(prevented()).toBe(true);
    }
    expect(opened).toEqual([row, row]);

    props.onKeyDown?.(keyEvent("a").event);
    // Enter on a control inside the row belongs to that control.
    props.onKeyDown?.(keyEvent("Enter", false).event);
    expect(opened).toHaveLength(2);
  });

  it("is pointer-only when the row links to its record", () => {
    const opened: Row[] = [];
    const props = rowInteraction(row, (r: Row) => opened.push(r), true);
    expect(props.role).toBeUndefined();
    expect(props.tabIndex).toBeUndefined();
    expect(props.onKeyDown).toBeUndefined();

    vi.stubGlobal("window", noSelection);
    try {
      props.onClick?.(clickOn(null));
      expect(opened).toEqual([row]);
      // A click on the cell link is the link's navigation, not a second one.
      props.onClick?.(clickOn(control));
      expect(opened).toEqual([row]);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("opens the record on a plain-cell click when the row is the button", () => {
    const opened: Row[] = [];
    const props = rowInteraction(row, (r: Row) => opened.push(r), false);

    vi.stubGlobal("window", noSelection);
    try {
      // The row is role="button", so closest() from a plain cell finds the
      // row itself: that is still a row click.
      props.onClick?.(clickOn(tr));
      props.onClick?.(clickOn(null));
      expect(opened).toEqual([row, row]);
      // A control nested in the row keeps its own click.
      props.onClick?.(clickOn(control));
      expect(opened).toEqual([row, row]);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
