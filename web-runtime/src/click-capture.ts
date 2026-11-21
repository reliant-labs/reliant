// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// One `click` span per real click, from the moment telemetry is asked for.
//
// ── Why the runtime records clicks itself ────────────────────────────────
//
// The SDK's interaction instrumentation (OpenTelemetry's user-interaction,
// wrapped by HyperDX) patches addEventListener and opens a span for every
// LISTENER a DOM event reaches. That is the wrong unit three ways:
//
//   - A listener added before the SDK started is never patched. React attaches
//     its root listeners when the app mounts, before the lazy SDK chunk has
//     loaded, so a click on a React button produced no span at all; what did
//     produce one was whichever listener happened to be added later (the
//     replay recorder's own, a late analytics handler).
//   - One click reaching three patched listeners was three spans: real-user
//     data showed most clicks recorded two or more times within 50 ms.
//   - mousedown and mouseup (on by default) opened empty root spans for every
//     press, which is why the runtime switched the instrumentation off
//     (`interactions: false`). That also stopped every click span, so a site
//     without session replay recorded no clicks anywhere.
//
// So the runtime listens once, on the document, in the capture phase, and
// records one span per trusted click: named `click`, with the attributes the
// SDK's spans carried (event_type, target_element, target_xpath, http.url,
// component) so existing queries keep working. The target is the clicked
// control (the nearest link, button, form field or ARIA control around the
// event's target), not the element a listener happened to sit on. No text
// content is read. http.url is scrubbed of its query and fragment when the
// click is recorded, by the same policy as every other URL (url-scrub.ts,
// keepUrlQueryParams): a held click is recorded before the SDK, and its
// export scrub, run, so the export scrub cannot be what keeps it clean.
//
// The span is a point in time: started and ended at the event's timestamp. It
// is never made the active context, so it parents nothing: an action's RPCs
// stay children of the action that made them (interaction.ts).
//
// Clicks made before the SDK is running (it loads lazily) are held, bounded,
// with their own timestamps, and recorded once it is.

import { type QueryParamFilter, scrubUrl } from "./url-scrub.js";

/** Most clicks held while the SDK is still loading. Later ones are dropped. */
export const MAX_HELD_CLICKS = 50;

/** The span name and `event_type` of a click span. */
export const CLICK_SPAN_NAME = "click";

/** The tracer click spans are recorded on. */
export const CLICK_SCOPE = "@reliantlabs/forge-web-runtime/clicks";

/** Longest target_xpath recorded; a longer path is cut. */
const MAX_XPATH_CHARS = 1024;

/** What one click recorded: when, and the span attributes. */
export interface ClickRecord {
  /** The event's timeStamp: ms since the time origin, as performance.now(). */
  at: number;
  attributes: Record<string, string>;
}

/** The click watch initBrowserTelemetry holds for the page. */
export interface ClickCapture {
  /**
   * Hands every held click, in order, and every later one to `emit`. Called
   * once the SDK runs.
   */
  attach(emit: (click: ClickRecord) => void): void;
  /** Stops listening and drops anything held. */
  stop(): void;
}

// The controls a click is about: the nearest of these around the event's
// target names the click; a click on plain content names the content.
const CONTROL_SELECTOR = [
  "a[href]",
  "button",
  "input",
  "select",
  "textarea",
  "label",
  "summary",
  "[role=button]",
  "[role=link]",
  "[role=menuitem]",
  "[role=menuitemcheckbox]",
  "[role=menuitemradio]",
  "[role=option]",
  "[role=tab]",
  "[role=checkbox]",
  "[role=radio]",
  "[role=switch]",
  "[onclick]",
].join(",");

/**
 * Starts watching the document for clicks. `enabled` is asked on every click
 * and again before each is handed on, so a click made, or held, while it says
 * no is never recorded. Each page URL is scrubbed as it is read, keeping
 * only the query parameters `keep` allows (none by default). Returns null
 * without a DOM.
 */
export function captureClicks(
  enabled: () => boolean,
  keep: QueryParamFilter | null = null,
): ClickCapture | null {
  if (
    typeof document === "undefined" ||
    typeof document.addEventListener !== "function"
  ) {
    return null;
  }
  let held: ClickRecord[] = [];
  let emit: ((click: ClickRecord) => void) | null = null;
  const hand = (click: ClickRecord) => {
    if (!emit || !enabled()) {
      return;
    }
    try {
      emit(click);
    } catch {
      // Telemetry must never break the app.
    }
  };
  const onClick = (event: Event) => {
    try {
      if (!event.isTrusted || !enabled()) {
        return;
      }
      const click = clickRecord(event, keep);
      if (!click) {
        return;
      }
      if (emit) {
        hand(click);
      } else if (held.length < MAX_HELD_CLICKS) {
        held.push(click);
      }
    } catch {
      // Telemetry must never break the app.
    }
  };
  const options = { capture: true, passive: true } as const;
  document.addEventListener("click", onClick, options);
  return {
    attach(next) {
      emit = next;
      const queued = held;
      held = [];
      for (const click of queued) {
        hand(click);
      }
    },
    stop() {
      emit = null;
      held = [];
      document.removeEventListener?.("click", onClick, options);
    },
  };
}

/**
 * The record of one click, or null when it names no element to record. Its
 * http.url has no fragment and no query but what `keep` allows.
 */
export function clickRecord(
  event: Event,
  keep: QueryParamFilter | null = null,
): ClickRecord | null {
  const element = clickedControl(event.target);
  if (!element || element.hasAttribute("disabled")) {
    return null;
  }
  const attributes: Record<string, string> = {
    component: "user-interaction",
    event_type: CLICK_SPAN_NAME,
    target_element: element.tagName,
    target_xpath: elementXPath(element).slice(0, MAX_XPATH_CHARS),
  };
  const href = pageHref();
  if (href) {
    attributes["http.url"] = scrubUrl(href, keep);
  }
  return { at: event.timeStamp, attributes };
}

function clickedControl(target: EventTarget | null): Element | null {
  // A click on text inside a control is targeted at the text's element; one
  // on a text node (some browsers) at the node: start from its parent.
  let node = target as (Node & { parentElement?: Element | null }) | null;
  if (node && node.nodeType !== 1) {
    node = node.parentElement ?? null;
  }
  const element = node as Element | null;
  if (!element || typeof element.getAttribute !== "function") {
    return null;
  }
  const control =
    typeof element.closest === "function"
      ? element.closest(CONTROL_SELECTOR)
      : null;
  return control ?? element;
}

/**
 * The element's XPath as the SDK's spans spelled it (sdk-trace-web
 * getElementXPath, optimised): from the nearest ancestor with an id
 * (`//*[@id="x"]`), else from the document; a position only where the element
 * has same-named siblings.
 */
export function elementXPath(element: Element): string {
  let path = "";
  for (
    let node: Element | null = element;
    node && node.nodeType === 1;
    node = node.parentElement
  ) {
    const id = node.getAttribute("id");
    if (id) {
      return `//*[@id="${id}"]${path}`;
    }
    path = `/${node.localName}${siblingIndex(node)}${path}`;
  }
  return path;
}

function siblingIndex(element: Element): string {
  const parent = element.parentNode;
  if (!parent) {
    return "";
  }
  let index = 0;
  let count = 0;
  for (const sibling of Array.from(parent.childNodes)) {
    if (
      sibling.nodeType === 1 &&
      (sibling as Element).localName === element.localName
    ) {
      count++;
      if (sibling === element) {
        index = count;
      }
    }
  }
  return index > 1 ? `[${index}]` : "";
}

function pageHref(): string | undefined {
  try {
    return typeof window !== "undefined" ? window.location?.href : undefined;
  } catch {
    return undefined;
  }
}
