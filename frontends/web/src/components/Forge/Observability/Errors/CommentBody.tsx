// Copyright (c) 2025 Reliant Labs

/**
 * A comment's body: plain text or markdown, stored verbatim by the server and
 * rendered here without ever becoming HTML we did not build.
 *
 * react-markdown turns markdown into React elements, not an HTML string, and
 * with no rehype-raw it does not parse raw HTML at all (`skipHtml` drops it
 * rather than printing it). Its default urlTransform removes javascript: and
 * other unsafe schemes from links. Images are not allowed: a comment is text,
 * and a remote image is a tracking pixel in someone else's console. Links open
 * in a new tab with no opener.
 *
 * Deliberately not Chat/MarkdownRenderer: that one resolves file paths against
 * a worktree and opens the embedded browser, neither of which a comment has.
 */

import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";

const COMPONENTS: Components = {
  a: ({ href, children }) => (
    <a href={href} target="_blank" rel="noopener noreferrer nofollow" className="text-ink underline underline-offset-2 hover:no-underline">
      {children}
    </a>
  ),
  img: ({ alt }) => <span className="text-ink-muted">{alt ? `[image: ${alt}]` : "[image]"}</span>,
  code: ({ className, children }) =>
    className ? (
      <code className="font-mono text-xs">{children}</code>
    ) : (
      <code className="rounded bg-surface-sunken px-1 py-0.5 font-mono text-xs">{children}</code>
    ),
  pre: ({ children }) => <pre className="my-1.5 overflow-x-auto rounded-md border border-border/60 bg-surface-sunken p-2 font-mono text-xs">{children}</pre>,
  p: ({ children }) => <p className="my-1 first:mt-0 last:mb-0">{children}</p>,
  ul: ({ children }) => <ul className="my-1 list-disc pl-5">{children}</ul>,
  ol: ({ children }) => <ol className="my-1 list-decimal pl-5">{children}</ol>,
  blockquote: ({ children }) => <blockquote className="my-1 border-l-2 border-border pl-3 text-ink-muted">{children}</blockquote>,
  h1: ({ children }) => <p className="my-1 font-semibold">{children}</p>,
  h2: ({ children }) => <p className="my-1 font-semibold">{children}</p>,
  h3: ({ children }) => <p className="my-1 font-semibold">{children}</p>,
  h4: ({ children }) => <p className="my-1 font-semibold">{children}</p>,
  h5: ({ children }) => <p className="my-1 font-semibold">{children}</p>,
  h6: ({ children }) => <p className="my-1 font-semibold">{children}</p>,
};

export function CommentBody({ body }: { body: string }) {
  return (
    <div className="min-w-0 break-words text-sm text-ink" data-testid="obs-comment-body">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={COMPONENTS} skipHtml>
        {body}
      </ReactMarkdown>
    </div>
  );
}
