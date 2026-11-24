// Copyright (c) 2025 Reliant Labs

/**
 * A page in fixture mode asks search engines not to index it. The hosted
 * preview serves prod's own build, whose index.html is shared with
 * app.reliantlabs.io (which must stay indexable), and a hosted site cannot set
 * an `X-Robots-Tag` header. So the tag is written at runtime, by install.ts,
 * only when config.js turns fixture mode on. A live page never loads this
 * module.
 *
 * Crawlers that run the page's JavaScript (Google, Bing) read a robots meta
 * tag the page adds. One that reads only the raw HTML sees prod's index.html:
 * an empty shell titled "Reliant", with no sample data in it.
 */

/** Add `<meta name="robots" content="noindex, nofollow">` once; returns it. */
export function markNoIndex(doc: Document = document): HTMLMetaElement {
  const existing = doc.head.querySelector<HTMLMetaElement>('meta[name="robots"]');
  const meta = existing ?? doc.createElement("meta");
  meta.name = "robots";
  meta.content = "noindex, nofollow";
  if (!existing) doc.head.prepend(meta);
  return meta;
}
