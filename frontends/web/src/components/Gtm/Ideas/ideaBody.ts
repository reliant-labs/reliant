// Copyright (c) 2026 Reliant Labs

/**
 * An idea's `body_md` read as the pages show it: its first paragraph is why
 * (the rationale: why this, why now), and the rest is the draft it carries.
 * One field on the wire (GtmIdea.body_md), two cards on the page; editing the
 * draft writes the two back together, so the rationale is never lost.
 */

export interface IdeaBody {
  why: string;
  draft: string;
}

export function splitIdeaBody(bodyMd: string): IdeaBody {
  const text = bodyMd.replace(/\r\n/g, "\n").trim();
  const gap = text.search(/\n\s*\n/);
  if (gap < 0) return { why: text, draft: "" };
  return { why: text.slice(0, gap).trim(), draft: text.slice(gap).trim() };
}

export function joinIdeaBody({ why, draft }: IdeaBody): string {
  const w = why.trim();
  const d = draft.trim();
  return w && d ? `${w}\n\n${d}` : w || d;
}
