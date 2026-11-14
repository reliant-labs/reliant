import { describe, expect, it } from "vitest";

import {
  CODE_FILE,
  CODE_FUNCTION,
  CODE_LINE,
  EXCEPTION_MESSAGE,
  EXCEPTION_STACKTRACE,
  EXCEPTION_TYPE,
  exceptionAttributes,
  exceptionType,
  isApplicationFrame,
  MAX_EXCEPTION_MESSAGE_CHARS,
  MAX_STACKTRACE_CHARS,
  parseStack,
  scrubPath,
} from "./node-exception.js";

const mac = {
  appRoot: "/Applications/Reliant.app/Contents/Resources/app",
  homeDir: "/Users/alice",
};

function errorWithStack(
  name: string,
  message: string,
  frames: string[],
): Error {
  const e = new Error(message);
  e.name = name;
  e.stack = [`${name}: ${message}`, ...frames.map((f) => `    at ${f}`)].join(
    "\n",
  );
  return e;
}

describe("scrubPath", () => {
  it("reports a file below the app root relative to it", () => {
    expect(
      scrubPath(
        "/Applications/Reliant.app/Contents/Resources/app/src/main.js",
        mac,
      ),
    ).toBe("src/main.js");
    expect(
      scrubPath(
        "file:///Applications/Reliant.app/Contents/Resources/app/src/main.js",
        mac,
      ),
    ).toBe("src/main.js");
  });

  it("starts a file inside an Electron archive after the archive", () => {
    expect(scrubPath("/opt/Reliant/resources/app.asar/src/main.js", mac)).toBe(
      "src/main.js",
    );
    expect(
      scrubPath("/opt/Reliant/resources/app.asar.unpacked/native/x.js", mac),
    ).toBe("native/x.js");
  });

  it("never carries the user's home directory", () => {
    expect(
      scrubPath("/Users/alice/code/reliant/electron/src/main.js", mac),
    ).toBe("~/code/reliant/electron/src/main.js");
    // Windows: case-insensitive, either separator, a drive letter in a file URL.
    const win = { homeDir: "C:\\Users\\Alice", caseInsensitive: true };
    expect(scrubPath("c:\\users\\alice\\AppData\\Local\\x.js", win)).toBe(
      "~/AppData/Local/x.js",
    );
    expect(scrubPath("file:///C:/Users/Alice/x.js", win)).toBe("~/x.js");
  });

  it("leaves internals and unrelated paths alone", () => {
    expect(scrubPath("node:internal/process/task_queues", mac)).toBe(
      "node:internal/process/task_queues",
    );
    expect(scrubPath("/usr/lib/x.js", mac)).toBe("/usr/lib/x.js");
  });
});

describe("parseStack and the application frame", () => {
  it("parses V8's frame shapes and skips the header", () => {
    const frames = parseStack(
      [
        "TypeError: x",
        "    at Object.<anonymous> (/Users/alice/app/a.js:1:2)",
        "    at async load (/Users/alice/app/b.js:3:4)",
        "    at new Window (/Users/alice/app/c.js:5:6)",
        "    at Timeout.tick [as _onTimeout] (/Users/alice/app/t.js:1:1)",
        "    at /Users/alice/app/d.js:7:8",
        "    at C:\\Users\\alice\\app\\e.js:9:10",
      ].join("\n"),
      { appRoot: "/Users/alice/app", homeDir: "/Users/alice" },
    );
    expect(frames).toEqual([
      { fn: "Object.<anonymous>", file: "a.js", line: 1, column: 2 },
      { fn: "load", file: "b.js", line: 3, column: 4 },
      { fn: "Window", file: "c.js", line: 5, column: 6 },
      { fn: "Timeout.tick", file: "t.js", line: 1, column: 1 },
      { fn: "", file: "d.js", line: 7, column: 8 },
      { fn: "", file: "C:/Users/alice/app/e.js", line: 9, column: 10 },
    ]);
  });

  it("is never Node, Electron, a dependency or eval", () => {
    for (const file of [
      "node:internal/timers",
      "internal/process/x",
      "node:electron/js2c/browser_init",
      "~/app/node_modules/electron-log/index.js",
      "/x/resources/electron.asar/browser/init.js",
      "<anonymous>",
    ]) {
      expect(
        isApplicationFrame({ fn: "f", file, line: 1, column: 1 }),
        file,
      ).toBe(false);
    }
    expect(
      isApplicationFrame({ fn: "f", file: "src/main.js", line: 1, column: 1 }),
    ).toBe(true);
  });
});

describe("exceptionType", () => {
  it("is the class for an Error, typeof otherwise", () => {
    class BackendCrashed extends Error {}
    expect(exceptionType(new TypeError("x"))).toBe("TypeError");
    expect(exceptionType(new BackendCrashed("x"))).toBe("BackendCrashed");
    const named = new Error("x");
    named.name = "AbortError";
    expect(exceptionType(named)).toBe("AbortError");
    expect(exceptionType(new Error("x"))).toBe("Error");
    expect(exceptionType("boom")).toBe("string");
    expect(exceptionType(null)).toBe("null");
    expect(exceptionType({ a: 1 })).toBe("object");
  });
});

describe("exceptionAttributes", () => {
  it("groups by the innermost application frame, the way observe groups a Go stack", () => {
    const err = errorWithStack(
      "TypeError",
      "Cannot read properties of undefined (reading 'id')",
      [
        "Timeout._onTimeout (/Applications/Reliant.app/Contents/Resources/app/node_modules/electron-log/x.js:1:1)",
        "createWindow (/Applications/Reliant.app/Contents/Resources/app/src/main.js:120:7)",
        "listOnTimeout (node:internal/timers:581:17)",
      ],
    );
    const attrs = exceptionAttributes(err, mac);
    expect(attrs[EXCEPTION_TYPE]).toBe("TypeError");
    expect(attrs[CODE_FUNCTION]).toBe("createWindow (src/main.js)");
    expect(attrs[CODE_FILE]).toBe("src/main.js");
    expect(attrs[CODE_LINE]).toBe(120);
  });

  it("is the same on two machines: no username, no install path", () => {
    const at = (home: string) =>
      exceptionAttributes(
        errorWithStack("Error", "x", [
          `load (${home}/Library/Reliant/app.asar/src/load.js:2:3)`,
        ]),
        { homeDir: home },
      );
    const a = at("/Users/alice");
    const b = at("/Users/bob");
    expect(a[CODE_FUNCTION]).toBe(b[CODE_FUNCTION]);
    expect(a[EXCEPTION_STACKTRACE]).toBe(b[EXCEPTION_STACKTRACE]);
    expect(String(a[EXCEPTION_STACKTRACE])).not.toContain("alice");
  });

  it("keeps the message's first line, redacted, out of the stack too", () => {
    const err = errorWithStack(
      "Error",
      "connect failed: postgres://admin:hunter2@db password=s3cret\nstderr: the user's file contents",
      ["boot (/app/src/main.js:1:1)"],
    );
    const attrs = exceptionAttributes(err, { appRoot: "/app" });
    expect(attrs[EXCEPTION_MESSAGE]).toBe(
      "connect failed: postgres://[redacted]@db password=[redacted]",
    );
    const stack = String(attrs[EXCEPTION_STACKTRACE]);
    expect(stack).toBe(
      "Error: connect failed: postgres://[redacted]@db password=[redacted]\n    at boot (src/main.js:1:1)",
    );
    expect(stack).not.toContain("file contents");
    expect(stack).not.toContain("hunter2");
  });

  it("bounds the message and the stack", () => {
    const frames = Array.from(
      { length: 64 },
      (_, i) => `f${i} (/app/${"x".repeat(400)}/${i}.js:1:1)`,
    );
    const attrs = exceptionAttributes(
      errorWithStack("Error", "word ".repeat(1000), frames),
      { appRoot: "/app" },
    );
    expect(String(attrs[EXCEPTION_MESSAGE]).length).toBe(
      MAX_EXCEPTION_MESSAGE_CHARS,
    );
    expect(String(attrs[EXCEPTION_STACKTRACE]).length).toBeLessThanOrEqual(
      MAX_STACKTRACE_CHARS,
    );
    expect(
      String(attrs[EXCEPTION_STACKTRACE]).startsWith("Error: word word"),
    ).toBe(true);
  });

  it("never serializes a thrown object", () => {
    const attrs = exceptionAttributes(
      { prompt: "the user's prompt", message: "rejected" },
      {},
    );
    expect(attrs).toEqual({
      [EXCEPTION_TYPE]: "object",
      [EXCEPTION_MESSAGE]: "rejected",
    });
    expect(
      exceptionAttributes({ prompt: "secret prompt" }, {})[EXCEPTION_MESSAGE],
    ).toBe("");
    expect(exceptionAttributes("plain", {})).toEqual({
      [EXCEPTION_TYPE]: "string",
      [EXCEPTION_MESSAGE]: "plain",
    });
  });

  it("falls back to the top frame when no frame is the application's", () => {
    const attrs = exceptionAttributes(
      errorWithStack("Error", "x", [
        "listOnTimeout (node:internal/timers:1:1)",
      ]),
      {},
    );
    expect(attrs[CODE_FUNCTION]).toBe("listOnTimeout (node:internal/timers)");
  });
});
