// Copyright (c) 2026 Reliant Labs

/**
 * A short markdown string an agent wrote (an evidence summary), as React
 * elements: **bold** / __bold__, *emphasis* / _emphasis_, `code` and
 * [text](url) links, nothing else. It builds elements, never HTML, so the
 * text cannot inject markup; a link goes through OutboundLink (http(s) only,
 * a new tab). Line breaks are the caller's (whitespace-pre-wrap). Markers
 * that do not pair up stay as typed.
 */

import type { ReactNode } from "react";

import { OutboundLink } from "../shared/ui";

const TOKEN = /\*\*(.+?)\*\*|__(.+?)__|`([^`]+)`|\[([^\]]+)\]\(([^)\s]+)\)|\*([^*\s][^*]*?)\*|(?<![\w])_([^_\s][^_]*?)_(?![\w])/g;

export function InlineMarkdown({ text }: { text: string }) {
  return <>{renderInline(text)}</>;
}

export function renderInline(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of text.matchAll(TOKEN)) {
    const at = m.index ?? 0;
    if (at > last) out.push(text.slice(last, at));
    const key = `${at}`;
    const [, bold1, bold2, code, linkText, linkUrl, em1, em2] = m;
    if (bold1 ?? bold2) out.push(<strong key={key}>{renderInline(bold1 ?? bold2 ?? "")}</strong>);
    else if (code) out.push(<code key={key} className="rounded bg-surface-sunken px-1 font-mono text-[0.9em]">{code}</code>);
    else if (linkText && linkUrl)
      out.push(
        <OutboundLink key={key} url={linkUrl} className="underline underline-offset-2">
          {linkText}
        </OutboundLink>,
      );
    else out.push(<em key={key}>{renderInline(em1 ?? em2 ?? "")}</em>);
    last = at + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}
