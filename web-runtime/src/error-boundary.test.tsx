// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// DefaultErrorFallback follows the same display rule as <Resource>'s error
// rung (see resource.test.tsx): it SHOWS an error, so it shows
// userMessage(error) as body copy — never the raw, code-framed
// `error.message` in a monospace debug style. A query run with throwOnError
// lands its error here, so the framing is as reachable as it is in a list.
import { Code, ConnectError } from "@connectrpc/connect";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import { DefaultErrorFallback } from "./error-boundary.js";

function renderFallback(error: Error): string {
  return renderToStaticMarkup(
    <DefaultErrorFallback error={error} reset={() => undefined} />,
  );
}

describe("DefaultErrorFallback", () => {
  it("shows userMessage(error), not the code-framed ConnectError.message", () => {
    const html = renderFallback(new ConnectError("no such job", Code.NotFound));

    expect(html).toContain("no such job");
    expect(html).not.toContain("[not_found]");
  });

  it("renders the message as body copy, not monospace debug output", () => {
    const html = renderFallback(new Error("no such job"));

    expect(html).not.toContain("font-mono");
  });
});

describe("RuntimeErrorBoundary.componentDidCatch", () => {
  it("records the caught error with the telemetry SDK before calling onError", async () => {
    const { RuntimeErrorBoundary } = await import("./error-boundary.js");
    const { setExceptionReporter } = await import("./error-reporter.js");
    const reporter = vi.fn();
    const onError = vi.fn();
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    setExceptionReporter(reporter);
    try {
      const boundary = new RuntimeErrorBoundary({
        children: null,
        onError,
      });
      const err = new Error("render crash");
      boundary.componentDidCatch(err, { componentStack: "\n at Widget" });

      expect(reporter).toHaveBeenCalledWith(err, {
        "react.component_stack": "\n at Widget",
        "error.handled_by": "RuntimeErrorBoundary",
      });
      expect(onError).toHaveBeenCalledTimes(1);
    } finally {
      setExceptionReporter(null);
      consoleError.mockRestore();
    }
  });
});
