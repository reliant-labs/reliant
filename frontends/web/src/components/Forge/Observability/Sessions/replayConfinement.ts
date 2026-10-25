// Copyright (c) 2025 Reliant Labs

/**
 * What the replay document is allowed to do, and the one place it is imposed.
 *
 * A recording is only as trustworthy as whoever uploaded it. The browser
 * recorder masks text and blocks media, but the ingest key is public and the
 * server never inspects event bodies, so anyone can upload events the recorder
 * never saw: an `<img src="https://attacker/…">`, a `<link rel=stylesheet>`, a
 * CSS `url()`. Rebuilt into the player's iframe, each of those is a request
 * from the viewer's browser (IP, user agent, and the moment they looked).
 *
 * Scripts are already out: the iframe is sandboxed without `allow-scripts`.
 * The sandbox keeps `allow-same-origin` and cannot lose it: rrweb rebuilds the
 * recording through `iframe.contentDocument`, which is null for an opaque-origin
 * frame, and its own rebuild refuses any sandbox other than exactly
 * `allow-same-origin`. That combination is safe only because `allow-scripts` is
 * absent (same-origin plus scripts is what lets a frame remove its own sandbox),
 * so the player pins `UNSAFE_replayCanvas: false` and a test pins the sandbox.
 *
 * REPLAY_CSP closes the other half: no network at all. The iframe starts as
 * about:blank, and rrweb reuses that document for every full snapshot, so a
 * policy delivered as a `<meta>` in it is in force before any recorded node is
 * built and is still in force after the next snapshot replaces the tree (checked
 * against rrweb 2.1.7 in Chromium: the second snapshot's image and stylesheet
 * were never requested). It is applied again after every rebuild anyway, for a
 * browser that clears the policy with the tree.
 */

/**
 * The policy. `default-src 'none'` covers script, connect, frame, media, object
 * and worker; images may only be inline (`data:`) or `blob:`, styles only inline,
 * fonts only inline. `base-uri` and `form-action` have no `default-src`
 * fallback, so they are named.
 */
export const REPLAY_CSP = "default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; font-src data:; base-uri 'none'; form-action 'none'";

/** Marks the meta this module owns, so applying twice leaves one. */
const MARK = "data-replay-csp";

/**
 * Put REPLAY_CSP into the replay iframe's document. It must run before any
 * recorded node is built (right after the Replayer is constructed) and again
 * after each full-snapshot rebuild. A document without a head gets one: the
 * browser honours a CSP meta only inside `<head>`.
 */
export function confineReplayDocument(doc: Document | null | undefined): void {
  const root = doc?.documentElement;
  if (!doc || !root) return;
  let head = doc.head;
  if (!head) {
    head = doc.createElement("head");
    root.insertBefore(head, root.firstChild);
  }
  if (head.querySelector(`meta[${MARK}]`)) return;
  const meta = doc.createElement("meta");
  meta.setAttribute("http-equiv", "Content-Security-Policy");
  meta.setAttribute("content", REPLAY_CSP);
  meta.setAttribute(MARK, "");
  head.insertBefore(meta, head.firstChild);
}

/** rrweb's event after a full snapshot has been rebuilt into the iframe. */
export const FULL_SNAPSHOT_REBUILT = "fullsnapshot-rebuilded";
