import React from "react";

interface InboxLayoutProps {
  /** Top bar slot — title, search, compose button. */
  header?: React.ReactNode;
  /** Left pane — typically a scrolling list of items. */
  list: React.ReactNode;
  /** Right pane — typically a preview of the selected item. Empty state if nothing selected is the parent's responsibility. */
  preview: React.ReactNode;
  /** List width as a fraction of total width (0-1). Default 0.35. */
  listRatio?: number;
}

/**
 * Two-pane list + preview layout (Gmail / Linear / Slack pattern).
 *
 * The component is presentational — the parent owns selection state and
 * decides what the preview renders. On narrow screens (< md), the layout
 * collapses to single-column with the list above the preview.
 */
export default function InboxLayout({ header, list, preview, listRatio = 0.35 }: InboxLayoutProps) {
  const listPercent = Math.round(Math.max(0.2, Math.min(0.6, listRatio)) * 100);
  const previewPercent = 100 - listPercent;
  return (
    <div className="flex h-screen w-full flex-col bg-surface">
      {header && (
        <div className="flex shrink-0 items-center gap-3 border-b border-border px-4 py-3">
          {header}
        </div>
      )}
      <div
        className="grid min-h-0 flex-1 grid-cols-1 md:grid-cols-[var(--inbox-list)_minmax(0,1fr)]"
        style={
          {
            "--inbox-list": `${listPercent}fr`,
            "--inbox-preview": `${previewPercent}fr`,
          } as React.CSSProperties
        }
      >
        <aside
          aria-label="Item list"
          className="min-h-0 relative overflow-y-auto border-b border-border md:border-b-0 md:border-r"
        >
          {list}
        </aside>
        <main aria-label="Item preview" className="min-h-0 relative overflow-y-auto">
          {preview}
        </main>
      </div>
    </div>
  );
}
