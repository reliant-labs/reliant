import { EventEmitter } from "node:events";
import { describe, expect, it, vi } from "vitest";
import type { Plugin } from "vite";
import {
  BROWSER_LOG_ENDPOINT,
  MAX_LINE,
  browserLogSink,
  renderDevLogPost,
} from "./vite-plugin-browser-logs";

/**
 * The pure half of the /__forge/log endpoint. Everything interesting about the
 * protocol lives in renderDevLogPost, so the plugin's middleware needs no test
 * harness of its own: it only drains the body and applies these results.
 */

/** Convenience: the common "small, well-formed body" case. */
function render(body: string) {
  return renderDevLogPost({
    body,
    bytes: Buffer.byteLength(body),
    overCap: false,
  });
}

describe("renderDevLogPost", () => {
  it("accepts a v1 single-entry body", () => {
    expect(render(JSON.stringify({ level: "info", msg: "hello v1" }))).toEqual({
      status: 204,
      lines: ["[browser:info] hello v1"],
    });
  });

  it("accepts a v2 batch and preserves array order", () => {
    const body = JSON.stringify({
      entries: [
        { level: "info", msg: "first" },
        { level: "warn", msg: "second" },
        { level: "error", msg: "third" },
      ],
    });

    expect(render(body)).toEqual({
      status: 204,
      lines: [
        "[browser:info] first",
        "[browser:warn] second",
        "[browser:error] third",
      ],
    });
  });

  it("prints an unknown level as log", () => {
    const body = JSON.stringify({
      entries: [
        { level: "trace", msg: "unknown" },
        { msg: "missing" },
      ],
    });

    expect(render(body).lines).toEqual([
      "[browser:log] unknown",
      "[browser:log] missing",
    ]);
  });

  it("truncates a msg longer than MAX_LINE", () => {
    const msg = "x".repeat(MAX_LINE + 50);
    const { status, lines } = render(JSON.stringify({ level: "debug", msg }));

    expect(status).toBe(204);
    expect(lines).toHaveLength(1);
    expect(lines[0]).toBe(
      `[browser:debug] ${"x".repeat(MAX_LINE)}… (truncated)`,
    );
  });

  it("leaves a msg of exactly MAX_LINE untouched", () => {
    const msg = "y".repeat(MAX_LINE);
    expect(render(JSON.stringify({ msg })).lines[0]).toBe(
      `[browser:log] ${msg}`,
    );
  });

  it("rejects malformed JSON with 400 and one warn line", () => {
    expect(render("{not json")).toEqual({
      status: 400,
      lines: ["[browser:warn] [forge-devlog] dropped malformed post"],
    });
  });

  it("rejects an over-cap body with 413 and reports the byte count", () => {
    expect(
      renderDevLogPost({ body: "", bytes: 2_000_000, overCap: true }),
    ).toEqual({
      status: 413,
      lines: [
        "[browser:warn] [forge-devlog] dropped oversized post (2000000 bytes)",
      ],
    });
  });

  it("accepts an empty body without emitting a line", () => {
    expect(render("")).toEqual({ status: 204, lines: [] });
  });
});

/**
 * Drive the middleware itself for the things that are not pure: the response
 * header, the method guard, and the one-console.log-per-batch rule.
 */
function mountMiddleware() {
  const handlers: Array<(req: unknown, res: unknown) => void> = [];
  const server = {
    middlewares: {
      use(path: string, handler: (req: unknown, res: unknown) => void) {
        expect(path).toBe(BROWSER_LOG_ENDPOINT);
        handlers.push(handler);
      },
    },
  };

  const plugin = browserLogSink() as Plugin & {
    configureServer: (s: unknown) => void;
  };
  plugin.configureServer(server);

  return function post(method: string, body: string) {
    const req = Object.assign(new EventEmitter(), { method });
    const res = {
      statusCode: 0,
      headers: {} as Record<string, string>,
      setHeader(name: string, value: string) {
        this.headers[name] = value;
      },
      ended: false,
      end() {
        this.ended = true;
      },
    };

    handlers[0](req, res);
    if (body) req.emit("data", Buffer.from(body));
    req.emit("end");
    return res;
  };
}

describe("browserLogSink middleware", () => {
  it("sets X-Forge-Devlog: 2 on an accepted post", () => {
    const post = mountMiddleware();
    const res = post("POST", JSON.stringify({ level: "info", msg: "hi" }));

    expect(res.statusCode).toBe(204);
    expect(res.headers["X-Forge-Devlog"]).toBe("2");
  });

  it("emits a whole batch in one console.log call", () => {
    const spy = vi.spyOn(console, "log").mockImplementation(() => {});
    try {
      const post = mountMiddleware();
      post(
        "POST",
        JSON.stringify({
          entries: [
            { level: "info", msg: "a" },
            { level: "warn", msg: "b" },
          ],
        }),
      );

      expect(spy).toHaveBeenCalledTimes(1);
      expect(spy).toHaveBeenCalledWith("[browser:info] a\n[browser:warn] b");
    } finally {
      spy.mockRestore();
    }
  });

  it("rejects a non-POST with 405", () => {
    const post = mountMiddleware();
    expect(post("GET", "").statusCode).toBe(405);
  });
});
