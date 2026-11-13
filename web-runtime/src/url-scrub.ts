// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// URLs leave the browser without their query string or fragment.
//
// The query and the fragment are where emails, tokens, OAuth codes, password
// reset links and search terms live (`/welcome?email=jane@example.com`,
// `/callback#access_token=…`), and the browser SDK stamps the page URL on
// EVERY span it records: `location.href` on each span, `http.url` on
// documentLoad/documentFetch/resourceFetch and click spans, `prev.href` on
// routeChange spans, `document.referrer` on documentLoad, `target_src` on a
// resource error, `http.url` / `url.full` on fetch and XHR spans. So this
// module rewrites every URL-valued attribute to the URL without them before a
// span is exported, which is the one point every span passes through
// whichever instrumentation made it.
//
// ── Where it hooks ───────────────────────────────────────────────────────
//
// A span processor's onEnd sees a span after it ended, when its attributes can
// no longer be set. The EXPORTER sees the same spans one step later, and a
// wrapping exporter can hand its inner exporter a scrubbed view of each span
// instead (scrubbingSpanExporter): the recorded span is untouched and only what
// is serialized changes. Span attributes, span-event attributes, link
// attributes and resource attributes are all scrubbed.
//
// The HyperDX browser SDK builds its exporter itself and exposes no option to
// wrap it, so installSpanExportScrub finds it on the running tracer provider
// (the OTel SDK's BasicTracerProvider: its span processors, and each batch or
// simple processor's exporter, as the OTel SDK version the pinned HyperDX
// SDK bundles declares them) and wraps it in place, right after init, before the first
// batch is due (the batch processor exports on a timer). The shape it relies
// on is pinned by telemetry.test.ts against the real SDK classes and by
// scripts/browser-telemetry-smoke.mjs against the real bundle in Chromium.
//
// ── Which attributes are URLs ────────────────────────────────────────────
//
// By NAME, never by value (isUrlAttribute): an attribute is a URL when its
// key is one the OTel semantic conventions or the SDK use for one (url.full,
// url.original, http.url, http.target, location.href, prev.href, page.url,
// document.referrer, target_src, messaging.url, …) or ends in href, url, uri,
// src or referrer (`page.url`, `link_href`, `nextUrl`). Each such string (or
// string-array element) keeps everything before the first `?` or `#`.
// `url.query` keeps only the allowed parameters and `url.fragment` is dropped.
// Every other attribute is exported exactly as recorded: a free-text field
// that happens to contain a URL (a console message, an error stack) is not
// rewritten.
//
// ── Keeping chosen query parameters ──────────────────────────────────────
//
// Off by default. `keepQueryParams` names the parameters to keep, exactly
// (`ref`) or by prefix with a trailing `*` (`utm_*`); matching is
// case-sensitive, on the decoded name. Kept pairs keep their original
// encoding and order. Everything else in the query is dropped, and the
// fragment is always dropped.

/** Options for scrubbing URLs in telemetry. */
export interface TelemetryUrlScrubOptions {
  /**
   * Query parameters kept in exported URLs: exact names (`ref`) or prefixes
   * ending in `*` (`utm_*`). Default none: the whole query is dropped. The
   * fragment is always dropped.
   */
  keepQueryParams?: readonly string[];
}

/** Decides whether a decoded query parameter name is kept. */
export type QueryParamFilter = (name: string) => boolean;

/** A filter for `keepQueryParams`, or null when nothing is kept. */
export function queryParamFilter(
  keep: readonly string[] | undefined,
): QueryParamFilter | null {
  const exact = new Set<string>();
  const prefixes: string[] = [];
  for (const pattern of keep ?? []) {
    if (typeof pattern !== "string" || pattern === "") {
      continue;
    }
    if (pattern.endsWith("*")) {
      prefixes.push(pattern.slice(0, -1));
    } else {
      exact.add(pattern);
    }
  }
  if (exact.size === 0 && prefixes.length === 0) {
    return null;
  }
  return (name) =>
    exact.has(name) || prefixes.some((prefix) => name.startsWith(prefix));
}

/**
 * `url` without its fragment and its query string, except the query
 * parameters `keep` allows. Works on absolute, relative and path-only URLs
 * alike, and changes nothing else about the string.
 */
export function scrubUrl(url: string, keep: QueryParamFilter | null): string {
  const hash = url.indexOf("#");
  const withoutFragment = hash === -1 ? url : url.slice(0, hash);
  const q = withoutFragment.indexOf("?");
  if (q === -1) {
    return withoutFragment;
  }
  const base = withoutFragment.slice(0, q);
  const query = keepQuery(withoutFragment.slice(q + 1), keep);
  return query ? `${base}?${query}` : base;
}

/**
 * The `&`-separated pairs of `query` (no leading `?`) whose decoded names
 * `keep` allows, in order and as they were encoded; empty when none is.
 */
function keepQuery(query: string, keep: QueryParamFilter | null): string {
  if (!keep) {
    return "";
  }
  const kept: string[] = [];
  for (const pair of query.split("&")) {
    if (pair === "") {
      continue;
    }
    const eq = pair.indexOf("=");
    const raw = eq === -1 ? pair : pair.slice(0, eq);
    let name: string;
    try {
      name = decodeURIComponent(raw.replace(/\+/g, " "));
    } catch {
      // A malformed escape is not a name anyone allowed.
      continue;
    }
    if (keep(name)) {
      kept.push(pair);
    }
  }
  return kept.join("&");
}

/** The attribute keys that name a URL but do not end in a URL word. */
const URL_KEYS: ReadonlySet<string> = new Set([
  "url.full",
  "url.original",
  "http.url",
  "http.target",
  "location.href",
  "prev.href",
  "page.url",
  "document.referrer",
  "http.referer",
  "target_src",
  "messaging.url",
]);

/** A key whose last word, after a separator, is a URL word (`page.url`). */
const URL_KEY_SEPARATED = /(?:^|[._-])(?:href|url|uri|src|referr?er)$/i;

/**
 * A camelCase key ending in a URL word (`nextUrl`, `baseURI`). Case-sensitive,
 * so a word that merely ends in the letters (`curl`) is not one.
 */
const URL_KEY_CAMEL = /[a-z0-9](?:Href|Url|URL|Uri|URI|Src|Referr?er)$/;

/** Whether an attribute named `key` holds a URL (see the header). */
export function isUrlAttribute(key: string): boolean {
  return (
    URL_KEYS.has(key) || URL_KEY_SEPARATED.test(key) || URL_KEY_CAMEL.test(key)
  );
}

/** An attribute value as OTel records it. */
type AttributeValue = unknown;
type Attributes = Record<string, AttributeValue>;

/**
 * `attributes` with every URL scrubbed, `url.query` filtered and
 * `url.fragment` dropped; the SAME object when nothing changed, so callers
 * copy only what they must. Never mutates `attributes`.
 */
export function scrubUrlAttributes<T extends Attributes | undefined>(
  attributes: T,
  keep: QueryParamFilter | null,
): T {
  if (!attributes || typeof attributes !== "object") {
    return attributes;
  }
  let out: Attributes | null = null;
  const edit = (): Attributes => (out ??= { ...attributes });
  for (const key of Object.keys(attributes)) {
    const value = attributes[key];
    if (key === "url.fragment") {
      delete edit()[key];
      continue;
    }
    if (key === "url.query") {
      if (typeof value === "string") {
        const kept = keepQuery(value.replace(/^\?/, ""), keep);
        if (kept === "") {
          delete edit()[key];
        } else if (kept !== value) {
          edit()[key] = kept;
        }
      }
      continue;
    }
    if (!isUrlAttribute(key)) {
      continue;
    }
    if (typeof value === "string") {
      const scrubbed = scrubUrl(value, keep);
      if (scrubbed !== value) {
        edit()[key] = scrubbed;
      }
    } else if (Array.isArray(value)) {
      let changed = false;
      const scrubbed = value.map((v: unknown) => {
        if (typeof v !== "string") {
          return v;
        }
        const s = scrubUrl(v, keep);
        changed ||= s !== v;
        return s;
      });
      if (changed) {
        edit()[key] = scrubbed;
      }
    }
  }
  return (out ?? attributes) as T;
}

/** The slice of an OTel ReadableSpan this module reads. */
interface ScrubbableSpan {
  attributes?: Attributes;
  events?: ReadonlyArray<{ attributes?: Attributes }>;
  links?: ReadonlyArray<{ attributes?: Attributes }>;
  resource?: { attributes?: Attributes };
}

/** The slice of an OTel SpanExporter this module calls. */
export interface SpanExporterLike<S = unknown> {
  export(spans: S[], resultCallback: (result: unknown) => void): void;
  shutdown(): Promise<void>;
  forceFlush?(): Promise<void>;
}

/** Marks an exporter scrubbingSpanExporter returned, so it is never wrapped twice. */
const SCRUBBING = Symbol.for("forge.web-runtime.scrubbingSpanExporter");

/**
 * Wraps a span exporter so every span it exports carries no URL query or
 * fragment (see the header). The spans themselves are not modified: the inner
 * exporter is handed a view of each span with scrubbed attributes, events,
 * links and resource, and a span with nothing to scrub is passed as it is.
 * For an app that builds its own OTel provider:
 * `new BatchSpanProcessor(scrubbingSpanExporter(new OTLPTraceExporter()))`.
 */
export function scrubbingSpanExporter<S, E extends SpanExporterLike<S>>(
  inner: E,
  options: TelemetryUrlScrubOptions = {},
): SpanExporterLike<S> {
  if ((inner as { [SCRUBBING]?: boolean })[SCRUBBING]) {
    return inner;
  }
  const keep = queryParamFilter(options.keepQueryParams);
  const wrapper: SpanExporterLike<S> & { [SCRUBBING]: true } = {
    [SCRUBBING]: true,
    export(spans, resultCallback) {
      // One scrubbed resource per resource in the batch: the OTLP encoder
      // groups spans by resource identity, so a copy per span would split
      // the batch into one ResourceSpans each. Built per batch, because the
      // SDK's resource has live attributes (rum.sessionId is a getter).
      const resources = new Map<object, object>();
      let out: S[];
      try {
        out = spans.map((span) => scrubSpan(span, keep, resources));
      } catch {
        // A span this cannot read is not exported unscrubbed.
        resultCallback({ code: 1, error: new Error("url scrub failed") });
        return;
      }
      // An inner exporter that throws instead of reporting must still end the
      // export through the callback: BatchSpanProcessor calls this from its
      // scheduled flush, and an exception would abort that flush. The
      // callback is reported once, whichever happens first.
      let reported = false;
      const report: typeof resultCallback = (result) => {
        if (reported) return;
        reported = true;
        resultCallback(result);
      };
      try {
        inner.export(out, report);
      } catch (error) {
        report({
          code: 1,
          error: error instanceof Error ? error : new Error(String(error)),
        });
      }
    },
    shutdown: () => inner.shutdown(),
    forceFlush: () => inner.forceFlush?.() ?? Promise.resolve(),
  };
  return wrapper;
}

function scrubSpan<S>(
  span: S,
  keep: QueryParamFilter | null,
  resources: Map<object, object>,
): S {
  const s = span as ScrubbableSpan;
  const overrides: PropertyDescriptorMap = {};
  const attributes = scrubUrlAttributes(s.attributes, keep);
  if (attributes !== s.attributes) {
    overrides.attributes = { value: attributes, enumerable: true };
  }
  for (const field of ["events", "links"] as const) {
    const list = s[field];
    if (!Array.isArray(list)) {
      continue;
    }
    let changed = false;
    const scrubbed = list.map((item: { attributes?: Attributes }) => {
      const a = scrubUrlAttributes(item?.attributes, keep);
      if (a === item?.attributes) {
        return item;
      }
      changed = true;
      return { ...item, attributes: a };
    });
    if (changed) {
      overrides[field] = { value: scrubbed, enumerable: true };
    }
  }
  const resource = s.resource;
  if (resource && typeof resource === "object") {
    let scrubbed = resources.get(resource);
    if (!scrubbed) {
      const original = resource.attributes;
      const a = scrubUrlAttributes(original, keep);
      // Same prototype chain, so the encoder's other reads (schemaUrl,
      // asyncAttributesPending) still reach the SDK's resource.
      scrubbed =
        a === original
          ? resource
          : Object.create(resource, {
              attributes: { value: a, enumerable: true },
            });
      resources.set(resource, scrubbed as object);
    }
    if (scrubbed !== resource) {
      overrides.resource = { value: scrubbed, enumerable: true };
    }
  }
  if (Object.keys(overrides).length === 0) {
    return span;
  }
  // A view over the recorded span: every other field and method (the span
  // context, times, status, scope) resolves on the span itself.
  return Object.create(span as object, overrides) as S;
}

/** The OTel SDK tracer provider's private shape installSpanExportScrub reads. */
interface SdkProvider {
  getDelegate?(): unknown;
  _activeSpanProcessor?: { _spanProcessors?: unknown };
}

/**
 * Wraps the exporter of every span processor on the running OTel SDK tracer
 * provider (behind @opentelemetry/api's global proxy) in
 * scrubbingSpanExporter. Returns how many exporters now scrub; 0 means this
 * provider does not have the shape the pinned SDK has, and its spans export
 * unscrubbed. Idempotent.
 */
export function installSpanExportScrub(
  tracerProvider: unknown,
  options: TelemetryUrlScrubOptions = {},
): number {
  try {
    const proxy = tracerProvider as SdkProvider | undefined;
    const provider = (proxy?.getDelegate?.() ?? proxy) as
      | SdkProvider
      | undefined;
    const processors = provider?._activeSpanProcessor?._spanProcessors;
    if (!Array.isArray(processors)) {
      return 0;
    }
    let scrubbing = 0;
    for (const processor of processors as Array<{ _exporter?: unknown }>) {
      const exporter = processor?._exporter as SpanExporterLike | undefined;
      if (
        !exporter ||
        typeof exporter.export !== "function" ||
        typeof exporter.shutdown !== "function"
      ) {
        continue;
      }
      processor._exporter = scrubbingSpanExporter(exporter, options);
      scrubbing++;
    }
    return scrubbing;
  } catch {
    return 0;
  }
}
