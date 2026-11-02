// Copyright (c) 2025 Reliant Labs

/**
 * A workflow's one-line summary, for a list row or a page subtitle: the first
 * sentence of its description. Descriptions are written for whoever reads
 * the definition, and run to paragraphs about its nodes and payloads; a
 * person deciding whether to use it needs what it does, which is the first
 * sentence. The whole description stays one disclosure away.
 */

const LIMIT = 160;

export function workflowSummary(description: string | undefined): string {
  const text = (description ?? "").trim();
  if (!text) return "";
  // The first paragraph, as one line.
  const paragraph = text.split(/\n\s*\n/, 1)[0]!.replace(/\s+/g, " ").trim();
  // Its first sentence: up to a full stop followed by a space and a capital
  // or the end. "e.g. this" and "v1.2" are not sentence ends.
  const match = /^(.+?[.!?])(?=\s+[A-Z(]|$)/.exec(paragraph);
  const sentence = match ? match[1] : paragraph;
  if (sentence.length <= LIMIT) return sentence;
  const cut = sentence.slice(0, LIMIT);
  const space = cut.lastIndexOf(" ");
  return `${(space > LIMIT / 2 ? cut.slice(0, space) : cut).replace(/[,;:]$/, "")}…`;
}

/** Whether the description says more than its summary does. */
export function descriptionHasMore(description: string | undefined): boolean {
  const text = (description ?? "").replace(/\s+/g, " ").trim();
  return text.length > 0 && text !== workflowSummary(description);
}
