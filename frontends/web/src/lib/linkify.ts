/**
 * Split plain text into runs of text and http(s) URLs, for text that is shown
 * as-is rather than as markdown (an ask_user question and its options), where
 * a URL should still be a link.
 *
 * Only explicit http(s) URLs are links: scheme-less candidates such as
 * `example.com` or `localhost:3000` are ambiguous in prose (see isHttpUrl).
 * Punctuation that ends the sentence rather than the URL stays text:
 * "see https://example.com." links `https://example.com`, and a closing
 * bracket stays text unless the URL opened it, so "(https://example.com)"
 * links the URL alone while "https://en.wikipedia.org/wiki/Go_(game)" keeps
 * its parenthesis.
 */
import { isHttpUrl } from "./url";

export type LinkifySegment =
  | { type: "text"; text: string }
  | { type: "link"; text: string; href: string };

const URL_CANDIDATE = /https?:\/\/[^\s<>"'`]+/gi;
const TRAILING_PUNCTUATION = /[.,;:!?*_~]+$/;
const CLOSERS: Record<string, string> = { ")": "(", "]": "[", "}": "{" };

function count(text: string, char: string): number {
  let n = 0;
  for (const c of text) if (c === char) n++;
  return n;
}

/** The candidate without the sentence punctuation and unopened brackets after it. */
function trimUrl(candidate: string): string {
  let url = candidate;
  for (;;) {
    const before = url;
    url = url.replace(TRAILING_PUNCTUATION, "");
    const last = url.at(-1);
    const opener = last ? CLOSERS[last] : undefined;
    if (opener && count(url, opener) < count(url, last!)) url = url.slice(0, -1);
    if (url === before) return url;
  }
}

export function linkifySegments(text: string): LinkifySegment[] {
  const segments: LinkifySegment[] = [];
  let textStart = 0;
  for (const match of text.matchAll(URL_CANDIDATE)) {
    const url = trimUrl(match[0]);
    // A word-internal "http://" (e.g. "xhttp://") is not a URL's start.
    const start = match.index;
    if (start > 0 && /\w/.test(text[start - 1]!)) continue;
    if (!isHttpUrl(url)) continue;
    if (start > textStart) segments.push({ type: "text", text: text.slice(textStart, start) });
    segments.push({ type: "link", text: url, href: url });
    textStart = start + url.length;
  }
  if (textStart < text.length) segments.push({ type: "text", text: text.slice(textStart) });
  return segments;
}
