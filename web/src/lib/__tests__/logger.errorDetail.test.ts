import { describe, expect, it, beforeEach, afterEach, vi } from "vitest";

// The console override shrinks every argument before it is printed (and so
// before the dev forwarder writes it to frontend_reliant-web.log). Two of those
// reductions erased exactly what an error needs to be attributable:
//
//  - an Error has no own enumerable keys, so the object summary turned it into
//    `{}` — message and stack gone;
//  - React logs "Maximum update depth exceeded" as console.error(message,
//    componentStack), and the 200-character string cap cut both.
//
// 109 such errors reached the log with no way to tell which component threw.
// Error-level lines now keep the detail; everything else stays capped.

describe("logger keeps error detail", () => {
  let printed: unknown[][];
  let pristineConsole: Record<string, unknown>;

  beforeEach(() => {
    pristineConsole = {
      log: console.log,
      error: console.error,
      warn: console.warn,
      info: console.info,
      debug: console.debug,
    };
    vi.resetModules();
    printed = [];
    // The override captures console.error at module load as its "original";
    // record what it finally prints.
    console.error = (...args: unknown[]) => {
      printed.push(args);
    };
  });

  afterEach(() => {
    Object.assign(console, pristineConsole);
    vi.restoreAllMocks();
  });

  it("prints an Error's message and stack instead of an empty summary", async () => {
    await import("../logger");
    const err = new Error("boom in WidgetPanel");

    console.error("[Test] render failed", err);

    const line = printed.at(-1)!.map(String).join(" ");
    expect(line).toContain("boom in WidgetPanel");
    expect(line).toContain(err.stack!.split("\n")[1].trim());
  });

  it("keeps React's component stack on an error line", async () => {
    await import("../logger");
    const message =
      "Maximum update depth exceeded. This can happen when a component calls setState inside useEffect, but useEffect either doesn't have a dependency array, or one of the dependencies changes on every render.";
    const componentStack =
      "\n    at TerminalRestorer (http://localhost/src/components/Terminal/TerminalRestorer.tsx:42:7)\n    at AppShell (http://localhost/src/App.tsx:10:3)";

    console.error(message, componentStack);

    const line = printed.at(-1)!.map(String).join(" ");
    expect(line).toContain("every render.");
    expect(line).toContain("TerminalRestorer");
  });

  it("still bounds an error line, just far above 200 characters", async () => {
    const { logger } = await import("../logger");
    const long = "x".repeat(20000);

    logger.error("[Test] huge payload", long);

    const line = printed.at(-1)!.map(String).join(" ");
    expect(line.length).toBeGreaterThan(1000);
    expect(line.length).toBeLessThan(20000);
  });
});
