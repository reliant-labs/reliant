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
import { describe, expect, it } from "vitest";

import { Resource, type ResourceColumn } from "./resource.js";

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
