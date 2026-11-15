// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The URL scrub on its own: which attributes are URLs, what is cut from them,
// what the allowlist keeps, and that the wrapping exporter hands its inner
// exporter a scrubbed VIEW without touching the recorded spans. The scrub in
// the SDK's real export pipeline is pinned in telemetry.test.ts ("URLs in what
// the SDK exports").
import { describe, expect, it, vi } from "vitest";

import {
  installSpanExportScrub,
  isUrlAttribute,
  queryParamFilter,
  replayUrlScrubPlugin,
  scrubbingSpanExporter,
  scrubUrl,
  scrubUrlAttributes,
} from "./url-scrub.js";

describe("scrubUrl", () => {
  it("drops the query and the fragment, whatever the URL's shape", () => {
    for (const [input, want] of [
      ["https://a.example/p?email=j%40x.io#tok", "https://a.example/p"],
      ["https://a.example/p#frag?not=query", "https://a.example/p"],
      ["https://a.example/p?", "https://a.example/p"],
      ["https://a.example/?q=1", "https://a.example/"],
      ["/relative/path?x=1", "/relative/path"],
      ["/clean", "/clean"],
      ["", ""],
    ] as const) {
      expect(scrubUrl(input, null)).toBe(want);
    }
  });

  it("keeps only allowed parameters, in order and as encoded, and never the fragment", () => {
    const keep = queryParamFilter(["utm_*", "ref"]);
    expect(
      scrubUrl(
        "https://a.example/p?email=j%40x.io&utm_source=news&ref=a%20b&utm_medium=mail#tok",
        keep,
      ),
    ).toBe("https://a.example/p?utm_source=news&ref=a%20b&utm_medium=mail");
    // Nothing allowed: no dangling `?`.
    expect(scrubUrl("https://a.example/p?token=x#t", keep)).toBe(
      "https://a.example/p",
    );
    // Names are matched decoded; a malformed one is dropped.
    expect(scrubUrl("/p?utm%5Fsource=a&%E0%A4%A=b&refx=c&ref", keep)).toBe(
      "/p?utm%5Fsource=a&ref",
    );
  });

  it("matches the allowlist case-sensitively, exactly or by `*` prefix", () => {
    const keep = queryParamFilter(["utm_*", "ref"])!;
    expect(keep("utm_source")).toBe(true);
    expect(keep("utm_")).toBe(true);
    expect(keep("ref")).toBe(true);
    expect(keep("UTM_SOURCE")).toBe(false);
    expect(keep("referrer")).toBe(false);
    expect(keep("email")).toBe(false);
  });

  it("keeps nothing by default, for an empty list or one of blanks", () => {
    expect(queryParamFilter(undefined)).toBeNull();
    expect(queryParamFilter([])).toBeNull();
    expect(queryParamFilter(["", 3 as unknown as string])).toBeNull();
  });
});

describe("isUrlAttribute", () => {
  it("knows the semantic-convention and SDK URL keys", () => {
    for (const key of [
      "location.href",
      "http.url",
      "url.full",
      "url.original",
      "http.target",
      "prev.href",
      "page.url",
      "document.referrer",
      "target_src",
      "messaging.url",
      "http.request.header.referer",
      "app.start_url",
      "link-href",
      "nextUrl",
      "baseURI",
      "imgSrc",
      "url",
      "href",
    ]) {
      expect(isUrlAttribute(key), key).toBe(true);
    }
  });

  it("leaves keys that only contain the letters alone", () => {
    for (const key of [
      "url.path",
      "url.scheme",
      "http.method",
      "message",
      "curl",
      "referrer_path",
      "referrer_host",
      "target_xpath",
      "source",
      "hrefs",
      "error.stack",
    ]) {
      expect(isUrlAttribute(key), key).toBe(false);
    }
  });
});

describe("scrubUrlAttributes", () => {
  it("scrubs URL values and string arrays, filters url.query, drops url.fragment", () => {
    const keep = queryParamFilter(["utm_*"]);
    const input = {
      "location.href": "https://a.example/p?email=j%40x.io&utm_source=n#t",
      "http.redirect_url": ["/a?x=1", "/b", 7],
      "url.query": "?email=j%40x.io&utm_source=n",
      "url.fragment": "t",
      "url.path": "/p",
      message: "see https://a.example/p?email=j%40x.io",
      count: 3,
    };
    const before = structuredClone(input);

    expect(scrubUrlAttributes(input, keep)).toEqual({
      "location.href": "https://a.example/p?utm_source=n",
      "http.redirect_url": ["/a", "/b", 7],
      "url.query": "utm_source=n",
      "url.path": "/p",
      message: "see https://a.example/p?email=j%40x.io",
      count: 3,
    });
    expect(input).toEqual(before);
    expect(scrubUrlAttributes({ "url.query": "email=x" }, keep)).toEqual({});
  });

  it("returns the same object when nothing changes", () => {
    const clean = { "http.url": "https://a.example/p", "url.path": "/p" };
    expect(scrubUrlAttributes(clean, null)).toBe(clean);
    expect(scrubUrlAttributes(undefined, null)).toBeUndefined();
  });
});

describe("scrubbingSpanExporter", () => {
  function fakeSpan(
    resource: object,
    attributes: Record<string, unknown>,
    extra: Record<string, unknown> = {},
  ) {
    return Object.assign(Object.create({ spanContext: () => ({ id: 1 }) }), {
      name: "s",
      attributes,
      events: [],
      links: [],
      resource,
      ...extra,
    });
  }

  it("exports a scrubbed view, and never touches the recorded span", () => {
    const resource = { attributes: { "service.name": "web" } };
    const span = fakeSpan(
      resource,
      { "location.href": "https://a.example/p?x=1" },
      {
        events: [{ name: "e", attributes: { "http.url": "/a?code=1" } }],
        links: [{ attributes: { "page.url": "/b#t" } }],
      },
    );
    const exported: unknown[][] = [];
    const inner = {
      export: (spans: unknown[], done: (r: unknown) => void) => {
        exported.push(spans);
        done({ code: 0 });
      },
      shutdown: vi.fn(() => Promise.resolve()),
    };
    const done = vi.fn();

    scrubbingSpanExporter(inner).export([span], done);

    const view = exported[0]![0] as typeof span;
    expect(view.attributes).toEqual({ "location.href": "https://a.example/p" });
    expect(view.events[0].attributes).toEqual({ "http.url": "/a" });
    expect(view.links[0].attributes).toEqual({ "page.url": "/b" });
    expect(view.name).toBe("s");
    expect(view.spanContext()).toEqual({ id: 1 });
    expect(view.resource).toBe(resource);
    expect(span.attributes["location.href"]).toBe("https://a.example/p?x=1");
    expect(span.events[0].attributes["http.url"]).toBe("/a?code=1");
    expect(done).toHaveBeenCalledWith({ code: 0 });
  });

  it("passes a span with nothing to scrub as it is, and keeps one resource per batch", () => {
    const resource = {
      attributes: { "page.url": "/start?email=x", "service.name": "web" },
      schemaUrl: "s",
    };
    const clean = fakeSpan({ attributes: {} }, { "http.url": "/a" });
    const a = fakeSpan(resource, {});
    const b = fakeSpan(resource, {});
    let out: Array<{ resource: { attributes: object; schemaUrl?: string } }> =
      [];
    const inner = {
      export: (spans: never[]) => {
        out = spans;
      },
      shutdown: () => Promise.resolve(),
    };

    scrubbingSpanExporter(inner).export([clean, a, b], () => {});

    expect(out[0]).toBe(clean);
    expect(out[1]!.resource).toBe(out[2]!.resource);
    expect(out[1]!.resource.attributes).toEqual({
      "page.url": "/start",
      "service.name": "web",
    });
    expect(out[1]!.resource.schemaUrl).toBe("s");
    expect(resource.attributes["page.url"]).toBe("/start?email=x");
  });

  it("fails the batch rather than export a span it cannot read", () => {
    const inner = { export: vi.fn(), shutdown: () => Promise.resolve() };
    const done = vi.fn();
    const hostile = {
      get attributes() {
        throw new Error("revoked");
      },
    };

    scrubbingSpanExporter(inner).export([hostile], done);

    expect(inner.export).not.toHaveBeenCalled();
    expect(done).toHaveBeenCalledWith(expect.objectContaining({ code: 1 }));
  });

  it("reports an inner exporter that throws through the callback, once, instead of throwing", () => {
    const done = vi.fn();
    const throwing = {
      export: vi.fn(() => {
        throw new Error("encoder blew up");
      }),
      shutdown: () => Promise.resolve(),
    };

    expect(() =>
      scrubbingSpanExporter(throwing).export([{ attributes: {} }], done),
    ).not.toThrow();
    expect(done).toHaveBeenCalledOnce();
    expect(done).toHaveBeenCalledWith(expect.objectContaining({ code: 1 }));

    // One that reports and then throws is still reported only once.
    const reportsThenThrows = {
      export: vi.fn((_spans: unknown[], cb: (r: unknown) => void) => {
        cb({ code: 0 });
        throw new Error("late");
      }),
      shutdown: () => Promise.resolve(),
    };
    const done2 = vi.fn();
    scrubbingSpanExporter(reportsThenThrows).export(
      [{ attributes: {} }],
      done2,
    );
    expect(done2).toHaveBeenCalledOnce();
    expect(done2).toHaveBeenCalledWith({ code: 0 });
  });

  it("delegates shutdown and forceFlush, and is never wrapped twice", async () => {
    const inner = {
      export: vi.fn(),
      shutdown: vi.fn(() => Promise.resolve()),
      forceFlush: vi.fn(() => Promise.resolve()),
    };
    const wrapped = scrubbingSpanExporter(inner);
    await wrapped.shutdown();
    await wrapped.forceFlush?.();

    expect(inner.shutdown).toHaveBeenCalledOnce();
    expect(inner.forceFlush).toHaveBeenCalledOnce();
    expect(scrubbingSpanExporter(wrapped as never)).toBe(wrapped);
  });
});

describe("installSpanExportScrub", () => {
  it("wraps every processor's exporter behind the API proxy, once", () => {
    const exporter = { export: vi.fn(), shutdown: () => Promise.resolve() };
    const batch = { _exporter: exporter as unknown };
    const attributes = { onStart() {} };
    const provider = {
      _activeSpanProcessor: { _spanProcessors: [attributes, batch] },
    };
    const proxy = { getDelegate: () => provider };

    expect(installSpanExportScrub(proxy)).toBe(1);
    expect(batch._exporter).not.toBe(exporter);
    const once = batch._exporter;
    expect(installSpanExportScrub(proxy)).toBe(1);
    expect(batch._exporter).toBe(once);
  });

  it("reports 0 for a provider without the SDK's shape, and never throws", () => {
    expect(installSpanExportScrub(undefined)).toBe(0);
    expect(installSpanExportScrub({ getDelegate: () => ({}) })).toBe(0);
    expect(
      installSpanExportScrub({
        getDelegate() {
          throw new Error("boom");
        },
      }),
    ).toBe(0);
  });
});

// rrweb's Meta event exactly as its record() builds it (rrweb 2.1, which
// @hyperdx/otel-web-session-recorder 4.0.0 bundles): EventType.Meta is 4, and
// data.href is window.location.href. The plugin inside the real recorder, in
// Chromium, is pinned by scripts/browser-telemetry-smoke.mjs.
const metaEvent = (href: string) => ({
  type: 4,
  data: { href, width: 1280, height: 720 },
  timestamp: 1700000000000,
});

describe("replayUrlScrubPlugin", () => {
  it("drops the query and fragment from a Meta event's href, copying the event", () => {
    const event = metaEvent(
      "https://a.example/reset?token=abc&email=j%40x.io#access_token=t",
    );
    const out = replayUrlScrubPlugin().eventProcessor(event);
    expect(out).toEqual(metaEvent("https://a.example/reset"));
    expect(out).not.toBe(event);
    expect(event.data.href).toBe(
      "https://a.example/reset?token=abc&email=j%40x.io#access_token=t",
    );
  });

  it("keeps only the allowed parameters, never the fragment", () => {
    const plugin = replayUrlScrubPlugin({ keepQueryParams: ["utm_*", "ref"] });
    expect(
      plugin.eventProcessor(
        metaEvent("https://a.example/?utm_source=x&email=e&ref=hn#f"),
      ).data.href,
    ).toBe("https://a.example/?utm_source=x&ref=hn");
  });

  it("passes every other event, and a Meta event it cannot read, as recorded", () => {
    const plugin = replayUrlScrubPlugin();
    const clean = metaEvent("https://a.example/p");
    const others: unknown[] = [
      clean,
      // A full snapshot's DOM URLs are page content the replay renders.
      { type: 2, data: { node: { attributes: { href: "/x?y=1" } } } },
      { type: 3, data: { source: 2, href: "/x?y=1" } },
      { type: 4, data: { href: 42 } },
      { type: 4 },
      null,
      "x",
    ];
    for (const event of others) {
      expect(plugin.eventProcessor(event)).toBe(event);
    }
  });

  it("is a plugin rrweb's record() accepts: a name and an eventProcessor, no observer", () => {
    const plugin = replayUrlScrubPlugin();
    expect(plugin.name).toBe("forge/replay-url-scrub");
    expect(plugin.options).toEqual({});
    expect("observer" in plugin).toBe(false);
  });
});
