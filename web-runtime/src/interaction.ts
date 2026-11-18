// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Interactions: one span for an operation the USER perceives, from intent to
// usable.
//
// The SDK records a fetch span per RPC, and the server records its own. What
// neither records is "the user clicked a chat, and N ms later it was on
// screen": several RPCs, a stream's first frame, a React commit and a paint.
// An interaction is that span:
//
//   const op = interaction("chat.open", { "chat.id": chatId }); // on intent
//   // ...RPCs started while op is open become its children...
//   op.endAfterPaint({ outcome: "ok" }); // when the screen is usable
//
// gives one ROOT span `chat.open` (kind INTERNAL) whose children are the
// browser fetch spans of the RPCs made while it was open, which parent the
// server spans through the traceparent the trace interceptor writes.
//
// ── The foreground model ─────────────────────────────────────────────────
//
// A browser has no async context, so "made while it was open" cannot ride
// context.with across awaits. Instead the runtime holds the open
// interactions, at most one per `scope` (default "foreground"), and the trace
// interceptor parents each RPC on the most recently started one at the moment
// the call goes out (interaction-context.ts has the precise rule and how the
// SDK's fetch span is reached). Starting an interaction in a scope that
// already has one ends the old one with outcome "superseded": the user moved
// on, and what they abandoned is not a sample of how long it takes.
//
// ── Ending ───────────────────────────────────────────────────────────────
//
// end() ends now. endAfterPaint() ends when the next frame has painted (two
// animation frames: the first callback runs before the paint, the second
// after it); when the page is hidden or there is no requestAnimationFrame it
// ends at once with `interaction.paint: "skipped"`. Both are idempotent: the
// first call wins. An interaction nobody ends ends itself after `timeoutMs`
// (default 60s) with outcome "timeout", so a leaked one cannot pile up.
// A page that goes hidden while one is open stamps `page.hidden: true`: a
// background tab's duration measures the tab, not the app.
//
// ── Telemetry off ────────────────────────────────────────────────────────
//
// With no SDK running (no endpoint, SSR, React Native) the OTel API hands out
// a no-op tracer. The interaction then carries a synthesized span context
// instead, so traceId is still real and every RPC it parents still shares one
// trace id on the server. After disableTelemetry() nothing records either.
// Nothing here throws: telemetry must never break the app.
import {
  context,
  SpanKind,
  SpanStatusCode,
  trace,
  type AttributeValue,
  type Context,
  type Span,
} from "@opentelemetry/api";

import { registerOpenInteraction } from "./interaction-context.js";
import { isTelemetryDisabled } from "./track.js";
import { randomSpanId, randomTraceId, synthesizedTraceFlags } from "./trace.js";

/** The scope an interaction joins when none is named. */
export const DEFAULT_INTERACTION_SCOPE = "foreground";
/** How long an interaction may stay open before it ends with outcome "timeout". */
export const DEFAULT_INTERACTION_TIMEOUT_MS = 60_000;

const TRACER_NAME = "@reliantlabs/forge-web-runtime";
const ATTR_NAME = "interaction.name";
const ATTR_SCOPE = "interaction.scope";
const ATTR_OUTCOME = "interaction.outcome";
const ATTR_PAINT = "interaction.paint";
const ATTR_HIDDEN = "page.hidden";

/** Attributes on an interaction. `null` and `undefined` values are dropped. */
export type InteractionAttributes = Record<
  string,
  AttributeValue | null | undefined
>;

/**
 * Attributes an interaction ends with. `outcome` becomes
 * `interaction.outcome` (default "ok"; "error" also sets the span's status to
 * ERROR). The runtime itself ends with "superseded" and "timeout".
 */
export interface InteractionEndAttributes extends InteractionAttributes {
  outcome?: string;
}

export interface InteractionOptions {
  /**
   * At most one interaction is open per scope; starting another ends the
   * previous one as "superseded". Default "foreground". Give an operation
   * that runs beside the main one (a side panel loading while a page does)
   * its own scope.
   */
  scope?: string;
  /** Milliseconds until it ends itself with outcome "timeout". Default 60000. */
  timeoutMs?: number;
}

/** A user-perceived operation, open from interaction() until it ends. */
export interface Interaction {
  readonly name: string;
  readonly scope: string;
  /** The trace this interaction and its RPCs share. Never empty while open. */
  readonly traceId: string;
  /** True once end() or endAfterPaint() was called, or it ended itself. */
  readonly ended: boolean;
  setAttributes(attributes: InteractionAttributes): void;
  /** A timestamped phase marker (a span event), e.g. "snapshot_received". */
  mark(name: string, attributes?: InteractionAttributes): void;
  /** End now. A second end is a no-op. */
  end(attributes?: InteractionEndAttributes): void;
  /** End once the next frame has painted. A second end is a no-op. */
  endAfterPaint(attributes?: InteractionEndAttributes): void;
  /**
   * Run `fn` with this interaction as the active context, for the
   * synchronous part of `fn`: a manual span started there is its child. Only
   * needed to parent on an interaction that is not the most recent one.
   */
  run<T>(fn: () => T): T;
}

type State = "open" | "painting" | "ended";

const byScope = new Map<string, InteractionImpl>();
const live = new Set<InteractionImpl>();
let visibilityHooked = false;

function pageHidden(): boolean {
  try {
    return (
      typeof document !== "undefined" && document.visibilityState === "hidden"
    );
  } catch {
    return false;
  }
}

function hookVisibility(): void {
  if (visibilityHooked || typeof document === "undefined") {
    return;
  }
  if (typeof document.addEventListener !== "function") {
    return;
  }
  visibilityHooked = true;
  document.addEventListener("visibilitychange", () => {
    if (pageHidden()) {
      for (const op of [...live]) {
        op.onHidden();
      }
    }
  });
}

function clean(attributes: InteractionAttributes | undefined): {
  [key: string]: AttributeValue;
} {
  const out: { [key: string]: AttributeValue } = {};
  if (!attributes || typeof attributes !== "object") {
    return out;
  }
  for (const [k, v] of Object.entries(attributes)) {
    if (v !== null && v !== undefined) {
      out[k] = v;
    }
  }
  return out;
}

class InteractionImpl implements Interaction {
  readonly name: string;
  readonly scope: string;
  private readonly span: Span;
  private readonly ctx: Context;
  private readonly leave: () => void;
  private readonly timer: ReturnType<typeof setTimeout> | undefined;
  private state: State = "open";
  private pending: InteractionEndAttributes | undefined;

  constructor(
    name: string,
    scope: string,
    attributes: InteractionAttributes | undefined,
    timeoutMs: number,
  ) {
    this.name = name;
    this.scope = scope;
    let span: Span | undefined;
    if (!isTelemetryDisabled()) {
      span = trace.getTracer(TRACER_NAME).startSpan(name, {
        kind: SpanKind.INTERNAL,
        // A new trace: the operation starts here, not under whatever span
        // (the SDK's click span, a render) happened to be active.
        root: true,
        attributes: {
          ...clean(attributes),
          [ATTR_NAME]: name,
          [ATTR_SCOPE]: scope,
        },
      });
    }
    if (!span || !trace.isSpanContextValid(span.spanContext())) {
      // No SDK (or disabled): a non-recording span over a real context, so
      // the traceparent of every RPC it parents still shares one trace id.
      span = trace.wrapSpanContext({
        traceId: randomTraceId(),
        spanId: randomSpanId(),
        traceFlags: synthesizedTraceFlags(),
      });
    }
    this.span = span;
    this.ctx = trace.setSpan(context.active(), span);
    this.leave = registerOpenInteraction(this.ctx);
    live.add(this);
    hookVisibility();
    if (pageHidden()) {
      span.setAttribute(ATTR_HIDDEN, true);
    }
    this.timer = setTimeout(() => this.onTimeout(), timeoutMs);
    // Never hold a Node process (SSR, tests) open for a span.
    (this.timer as unknown as { unref?: () => void } | undefined)?.unref?.();
  }

  get traceId(): string {
    return this.span.spanContext().traceId;
  }

  get ended(): boolean {
    return this.state !== "open";
  }

  setAttributes(attributes: InteractionAttributes): void {
    if (this.state === "ended") {
      return;
    }
    try {
      this.span.setAttributes(clean(attributes));
    } catch {
      // Telemetry must never break the app.
    }
  }

  mark(name: string, attributes?: InteractionAttributes): void {
    if (this.state === "ended") {
      return;
    }
    try {
      this.span.addEvent(String(name), clean(attributes));
    } catch {
      // Telemetry must never break the app.
    }
  }

  end(attributes?: InteractionEndAttributes): void {
    if (this.state !== "open") {
      return;
    }
    this.finish(attributes);
  }

  endAfterPaint(attributes?: InteractionEndAttributes): void {
    if (this.state !== "open") {
      return;
    }
    // Done as far as the app is concerned: later RPCs are not part of it, and
    // a new interaction in this scope no longer supersedes it.
    this.state = "painting";
    this.pending = attributes;
    this.leaveForeground();
    try {
      if (pageHidden() || typeof requestAnimationFrame !== "function") {
        this.finishSkippingPaint();
        return;
      }
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          this.finish({ ...this.pending, [ATTR_PAINT]: "painted" });
        });
      });
    } catch {
      this.finishSkippingPaint();
    }
  }

  run<T>(fn: () => T): T {
    return context.with(this.ctx, fn);
  }

  /** The page went hidden. */
  onHidden(): void {
    if (this.state === "ended") {
      return;
    }
    try {
      this.span.setAttribute(ATTR_HIDDEN, true);
    } catch {
      // Telemetry must never break the app.
    }
    // A hidden page paints no frames: waiting would only measure the tab.
    if (this.state === "painting") {
      this.finishSkippingPaint();
    }
  }

  private onTimeout(): void {
    if (this.state === "open") {
      this.finish({ outcome: "timeout" });
    } else if (this.state === "painting") {
      this.finishSkippingPaint();
    }
  }

  private finishSkippingPaint(): void {
    this.finish({ ...this.pending, [ATTR_PAINT]: "skipped" });
  }

  private leaveForeground(): void {
    if (byScope.get(this.scope) === this) {
      byScope.delete(this.scope);
    }
    this.leave();
  }

  private finish(attributes: InteractionEndAttributes | undefined): void {
    if (this.state === "ended") {
      return;
    }
    this.state = "ended";
    live.delete(this);
    this.leaveForeground();
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
    }
    try {
      const { outcome, ...rest } = attributes ?? {};
      const resolved = typeof outcome === "string" && outcome ? outcome : "ok";
      this.span.setAttributes({ ...clean(rest), [ATTR_OUTCOME]: resolved });
      if (resolved === "error") {
        this.span.setStatus({ code: SpanStatusCode.ERROR });
      }
      this.span.end();
    } catch {
      // Telemetry must never break the app.
    }
  }
}

/** What interaction() returns if building the real one failed. */
function inert(name: string, scope: string): Interaction {
  return {
    name,
    scope,
    traceId: "",
    ended: true,
    setAttributes: () => undefined,
    mark: () => undefined,
    end: () => undefined,
    endAfterPaint: () => undefined,
    run: <T>(fn: () => T): T => fn(),
  };
}

/**
 * Start an interaction: a root span named `name` that the RPCs made while it
 * is open join. Call it on the user's intent (the click, the route change),
 * and end it when the result is usable. Safe anywhere, never throws.
 */
export function interaction(
  name: string,
  attributes?: InteractionAttributes,
  options?: InteractionOptions,
): Interaction {
  const label = String(name);
  const scope =
    typeof options?.scope === "string" && options.scope
      ? options.scope
      : DEFAULT_INTERACTION_SCOPE;
  try {
    const timeout = options?.timeoutMs;
    const timeoutMs =
      typeof timeout === "number" && Number.isFinite(timeout) && timeout > 0
        ? timeout
        : DEFAULT_INTERACTION_TIMEOUT_MS;
    byScope.get(scope)?.end({ outcome: "superseded" });
    const op = new InteractionImpl(label, scope, attributes, timeoutMs);
    byScope.set(scope, op);
    return op;
  } catch {
    return inert(label, scope);
  }
}

/**
 * The open interaction in `scope` (default "foreground"), for code that
 * marks or ends an operation it did not start — a stream hook marking
 * "snapshot_received" on the chat open a click began.
 */
export function currentInteraction(
  scope: string = DEFAULT_INTERACTION_SCOPE,
): Interaction | undefined {
  return byScope.get(scope);
}

/** End every open interaction and forget the page hooks. For tests. */
export function resetInteractions(): void {
  for (const op of [...live]) {
    op.end({ outcome: "reset" });
  }
  for (const op of [...live]) {
    // One still waiting on a paint.
    op.onHidden();
  }
  byScope.clear();
  live.clear();
  visibilityHooked = false;
}
