// Copyright (c) 2025 Reliant Labs

/**
 * A text panel: the Markdown a dashboard author wrote (forge dashspec
 * `kind: "text"`), with no query behind it — a heading over a row of panels,
 * what the dashboard is for, a runbook link.
 *
 * The same safety as an issue comment (Errors/CommentBody): react-markdown
 * builds React elements, never an HTML string; raw HTML is dropped
 * (`skipHtml`); unsafe link schemes are removed by its default urlTransform;
 * no remote images (a tracking pixel in a viewer's console). Headings stay
 * below the panel's own title in weight, so a text panel never outranks the
 * dashboard's heading.
 */

import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";

import { cn } from "@/lib/utils";

const COMPONENTS: Components = {
  a: ({ href, children }) => (
    <a href={href} target="_blank" rel="noopener noreferrer nofollow" className="text-ink underline decoration-ink-muted/50 underline-offset-2 hover:decoration-ink">
      {children}
    </a>
  ),
  img: ({ alt }) => <span className="text-ink-muted">{alt ? `[image: ${alt}]` : "[image]"}</span>,
  code: ({ className, children }) =>
    className ? <code className="font-mono text-xs">{children}</code> : <code className="rounded bg-surface-sunken px-1 py-0.5 font-mono text-xs">{children}</code>,
  pre: ({ children }) => <pre className="my-2 overflow-x-auto rounded-md border border-border/60 bg-surface-sunken p-2 font-mono text-xs">{children}</pre>,
  p: ({ children }) => <p className="my-1.5 first:mt-0 last:mb-0">{children}</p>,
  ul: ({ children }) => <ul className="my-1.5 list-disc pl-5">{children}</ul>,
  ol: ({ children }) => <ol className="my-1.5 list-decimal pl-5">{children}</ol>,
  li: ({ children }) => <li className="my-0.5">{children}</li>,
  blockquote: ({ children }) => <blockquote className="my-1.5 border-l-2 border-border pl-3 text-ink-muted">{children}</blockquote>,
  h1: ({ children }) => <h3 className="mb-1 mt-3 text-sm font-semibold text-ink first:mt-0">{children}</h3>,
  h2: ({ children }) => <h3 className="mb-1 mt-3 text-sm font-semibold text-ink first:mt-0">{children}</h3>,
  h3: ({ children }) => <h4 className="mb-1 mt-2 text-xs font-semibold uppercase tracking-wide text-ink-muted first:mt-0">{children}</h4>,
  h4: ({ children }) => <h4 className="mb-1 mt-2 text-xs font-semibold text-ink first:mt-0">{children}</h4>,
  h5: ({ children }) => <h4 className="mb-1 mt-2 text-xs font-semibold text-ink first:mt-0">{children}</h4>,
  h6: ({ children }) => <h4 className="mb-1 mt-2 text-xs font-semibold text-ink first:mt-0">{children}</h4>,
  hr: () => <hr className="my-3 border-border/60" />,
  table: ({ children }) => (
    <div className="my-2 overflow-x-auto">
      <table className="w-full border-collapse text-xs">{children}</table>
    </div>
  ),
  th: ({ children }) => <th className="border-b border-border px-2 py-1 text-left font-medium text-ink-muted">{children}</th>,
  td: ({ children }) => <td className="border-b border-border/40 px-2 py-1">{children}</td>,
};

export function TextPanel({ markdown, compact = false }: { markdown: string; compact?: boolean }) {
  return (
    <div className={cn("min-w-0 break-words text-sm text-ink", compact && "line-clamp-2 text-xs text-ink-muted")} data-testid="obs-panel-text">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={COMPONENTS} skipHtml>
        {markdown}
      </ReactMarkdown>
    </div>
  );
}
