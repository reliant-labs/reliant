import { Fragment } from "react";
import { linkifySegments } from "../../lib/linkify";
import { openLink } from "../../lib/open-link";

/**
 * A link in chat text. Accent text is --primary-ink, which every scheme holds
 * at AA on every surface (web-styling, "Accent text"); the underline is what
 * says "link" in a scheme whose primary is not blue. MarkdownRenderer's links
 * use it too, so a link looks the same in a reply and in a question.
 */
export const CHAT_LINK_CLASS =
  "cursor-pointer text-primary-ink underline decoration-primary-ink/40 underline-offset-2 transition-colors duration-200 hover:decoration-primary-ink";

interface LinkifiedTextProps {
  /** Plain text, shown as-is except that its http(s) URLs are links. */
  text: string;
  /** Worktree whose embedded browser opens a dev URL (see openLink). */
  worktreeId?: string;
}

/**
 * Plain text with its http(s) URLs as links: an ask_user question, an option's
 * label or description. Not markdown — an agent's `*` or `#` stays as typed.
 *
 * A click opens the URL the way MarkdownRenderer's links do (openLink: the
 * system browser in Electron, a new tab on the web, the embedded browser for a
 * dev URL), and stops there: a link inside a clickable option opens the link
 * without selecting the option.
 */
export function LinkifiedText({ text, worktreeId }: LinkifiedTextProps) {
  const segments = linkifySegments(text);
  if (segments.length === 1 && segments[0]!.type === "text") return <>{text}</>;
  return (
    <>
      {segments.map((segment, i) =>
        segment.type === "link" ? (
          <a
            key={i}
            href={segment.href}
            target="_blank"
            rel="noopener noreferrer"
            className={CHAT_LINK_CLASS}
            onClick={(e) => {
              e.stopPropagation();
              e.preventDefault();
              void openLink(segment.href, worktreeId);
            }}
          >
            {segment.text}
          </a>
        ) : (
          <Fragment key={i}>{segment.text}</Fragment>
        ),
      )}
    </>
  );
}
