// Copyright (c) 2025 Reliant Labs

/**
 * A RELEASE VERSION ON ONE LINE, however long it is.
 *
 * forge's versions are timestamp-plus-sha (`20261005.202711-1f9bf6721d7d`).
 * Rendered as a plain inline string in a fixed-width column they wrapped onto
 * two lines and the "current" pill beside them was drawn on top of the second
 * — and on the next row the wrapped half ran into the date. That is a class of
 * bug, not one row: any free-text identifier in a flex row with a neighbour
 * does it as soon as the viewport narrows.
 *
 * So the version never wraps. It is split into a HEAD that truncates and a
 * TAIL that never does: the end of a version is the sha, which is the part
 * people compare, so a narrow screen shows `20261005.2…1f9bf672` rather than
 * `20261005.202711-1f…`. Pure CSS, so it truncates only when it has to and
 * re-flows with the container. The full string is in `title`, the DOM text is
 * whole (screen readers and find-in-page see all of it), and a copy button
 * puts it on the clipboard.
 */

import { useEffect, useState } from "react";
import { Check, Copy } from "lucide-react";

import { cn } from "@/lib/utils";

/** How much of the end is pinned visible. A short sha. */
const TAIL = 8;

export function ReleaseVersion({
  version,
  copyable = true,
  className,
  testId,
}: {
  version: string;
  copyable?: boolean;
  className?: string;
  testId?: string;
}) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  // A short version is one span: splitting "v12" would only add a node.
  const split = version.length > TAIL * 2;
  const head = split ? version.slice(0, -TAIL) : version;
  const tail = split ? version.slice(-TAIL) : "";

  return (
    <span className={cn("inline-flex min-w-0 max-w-full items-center gap-1", className)} data-testid={testId}>
      {/* overflow-hidden: if the container is narrower than even the pinned
          tail, the tail is CLIPPED here rather than painted under whatever
          sits beside this component. */}
      <span
        className="flex min-w-0 overflow-hidden whitespace-nowrap font-mono text-foreground"
        title={version}
        data-release-version={version}
      >
        <span className="truncate">{head}</span>
        {tail !== "" && <span className="shrink-0">{tail}</span>}
      </span>
      {copyable && (
        <button
          type="button"
          onClick={() => {
            void navigator.clipboard?.writeText(version).then(
              () => setCopied(true),
              () => undefined
            );
          }}
          aria-label={copied ? "Copied release version" : `Copy release version ${version}`}
          title={copied ? "Copied" : "Copy version"}
          className="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {copied ? <Check className="h-3 w-3" aria-hidden="true" /> : <Copy className="h-3 w-3" aria-hidden="true" />}
        </button>
      )}
    </span>
  );
}
