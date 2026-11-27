import { analyticsTier, type Interaction } from "@reliantlabs/forge-web-runtime";
import type { ChatStreamEvent } from "../gen/reliant/v1/streaming_pb";

const MAX_ATTEMPTS = 3;
const EARLY_FRAMES = 4;
const attempts = new WeakMap<Interaction, number>();

// CORS can hide the header. Never record a header's actual value or call an
// unseen encoding "identity". The body passed to Connect is already decoded
// at the HTTP Content-Encoding layer.
function contentEncoding(headers: Headers): string {
  switch (headers.get("content-encoding")?.trim().toLowerCase()) {
    case undefined: return "not_visible";
    case "gzip": return "gzip";
    case "br": return "br";
    case "deflate": return "deflate";
    case "zstd": return "zstd";
    case "identity": return "identity";
    default: return "other";
  }
}

/** Observe only the header and decoded-read boundaries of one active open. */
export function chatStreamDeliveryProbe(open: Interaction | undefined, signal?: AbortSignal) {
  if (!open || signal?.aborted || analyticsTier() === "off") return;
  const attempt = (attempts.get(open) ?? 0) + 1;
  if (attempt > MAX_ATTEMPTS) return;
  attempts.set(open, attempt);
  const active = () => !open.ended && !signal?.aborted;
  // A recording span already in flight is not ended by disableTelemetry().
  // Recheck the runtime's public off state before each bounded mark.
  const canMark = () => active() && analyticsTier() !== "off";
  let headersAt: number | undefined;

  return {
    onHeader(headers: Headers) {
      if (headersAt !== undefined || !canMark()) return;
      headersAt = performance.now();
      open.mark("stream_headers_received", { attempt, content_encoding: contentEncoding(headers) });
    },
    observe(source: AsyncIterable<ChatStreamEvent>): AsyncIterable<ChatStreamEvent> {
      return {
        [Symbol.asyncIterator]() {
          const iterator = source[Symbol.asyncIterator]();
          let frame = 0;
          const next = iterator.next.bind(iterator);
          const observed: AsyncIterator<ChatStreamEvent> = {
            next(...args) {
              if (!active()) {
                observed.next = next;
                return next(...args);
              }
              const startedAt = performance.now();
              if (frame < EARLY_FRAMES) {
                if (!canMark()) {
                  observed.next = next;
                  return next(...args);
                }
                open.mark("stream_frame_read_started", { attempt, frame: frame + 1 });
              }
              return next(...args).then((result) => {
                if (result.done || !active()) {
                  observed.next = next;
                } else {
                  frame++;
                  const caughtUp = result.value.event.case === "caughtUp";
                  if ((frame <= EARLY_FRAMES || caughtUp) && canMark()) {
                    open.mark(caughtUp ? "stream_caught_up_frame_delivered" : "stream_frame_delivered", {
                      attempt,
                      frame,
                      // A first read can start before headers; report only
                      // the wait after headers were available in that case.
                      read_wait_ms: Math.round(performance.now() - Math.max(startedAt, headersAt ?? startedAt)),
                    });
                  }
                  // Not an async generator: preserve the original iterator's
                  // next() promise on the live stream, and never read ahead.
                  if (caughtUp) observed.next = next;
                }
                return result;
              }, async (error: unknown) => {
                // A rejected read does not make for-await close the iterator.
                // Forward the failure ourselves, preserving the read's error
                // even if cleanup resolves or fails with a different error.
                try {
                  if (iterator.throw) await iterator.throw(error);
                  else await iterator.return?.();
                } catch {
                  // Cleanup must not mask the original stream failure.
                }
                throw error;
              });
            },
          };
          if (iterator.return) observed.return = iterator.return.bind(iterator);
          if (iterator.throw) observed.throw = iterator.throw.bind(iterator);
          return observed;
        },
      };
    },
  };
}
