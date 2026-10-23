import { afterEach, describe, expect, it } from "vitest";
import {
  MAX_ENTRIES,
  clearClientErrors,
  getClientErrors,
  recordClientError,
  resetClientDiagnosticsForTests,
} from "../clientDiagnostics";
import { formatDiagnosticsReport, type DiagnosticsFacts } from "../diagnosticsReport";

afterEach(() => {
  resetClientDiagnosticsForTests();
  sessionStorage.clear();
});

describe("clientDiagnostics", () => {
  it("records an error with its source, message, stack head and route", () => {
    recordClientError("boundary", new TypeError("x is undefined"));
    const [entry] = getClientErrors();
    expect(entry).toMatchObject({ source: "boundary", message: "TypeError: x is undefined", count: 1, path: "/" });
    expect(entry!.stack?.split("\n").length).toBeLessThanOrEqual(6);
  });

  it("folds consecutive duplicates into one counted entry", () => {
    recordClientError("console", "[gRPC Client] Request failed");
    recordClientError("console", "[gRPC Client] Request failed");
    recordClientError("console", "[gRPC Client] Request failed");
    expect(getClientErrors()).toHaveLength(1);
    expect(getClientErrors()[0]!.count).toBe(3);
  });

  it("keeps only the most recent entries, mirrored to sessionStorage for a reload", () => {
    for (let i = 0; i < MAX_ENTRIES + 5; i += 1) recordClientError("console", `error ${i}`);
    expect(getClientErrors()).toHaveLength(MAX_ENTRIES);
    expect(getClientErrors()[0]!.message).toBe("error 5");
    const stored = JSON.parse(sessionStorage.getItem("reliant-client-errors")!) as unknown[];
    expect(stored).toHaveLength(MAX_ENTRIES);
  });

  it("joins console arguments and truncates a runaway message", () => {
    recordClientError("console", "Stream error", { code: 13 });
    expect(getClientErrors()[0]!.message).toBe('Stream error {"code":13}');
    recordClientError("console", "y".repeat(5000));
    expect(getClientErrors()[1]!.message.length).toBe(500);
  });

  it("clears", () => {
    recordClientError("uncaught", new Error("boom"));
    clearClientErrors();
    expect(getClientErrors()).toHaveLength(0);
  });
});

describe("formatDiagnosticsReport", () => {
  const facts: DiagnosticsFacts = {
    app: "v1.9.0",
    server: "v1.9.0",
    release: null,
    url: "https://app.reliantlabs.io/m/chats/c1",
    userAgent: "iPhone",
    viewport: "390×844 @3x",
    online: true,
    surface: "mobile",
    stream: { status: "error", lastSequence: 42, subscribedChatId: "c1", daemonLastSeen: 1_000 },
    userId: "u1",
    projectId: "p1",
    generatedAt: new Date(1_030_000).toISOString(),
  };

  it("names the build, the stream state and the errors, newest first", () => {
    recordClientError("console", "first");
    recordClientError("rejection", "second");
    const text = formatDiagnosticsReport(facts, getClientErrors());
    expect(text).toContain("App: v1.9.0");
    expect(text).toContain("Update stream: error");
    expect(text).toContain("last sequence: 42");
    expect(text).toContain("machine heartbeat: 30s ago");
    expect(text).toContain("Recent errors (2):");
    expect(text.indexOf("second")).toBeLessThan(text.indexOf("first"));
  });

  it("says so when there are no errors", () => {
    expect(formatDiagnosticsReport(facts, [])).toContain("Recent errors (0):\n  none");
  });
});
