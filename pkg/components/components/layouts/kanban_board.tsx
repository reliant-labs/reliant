interface KanbanBoardProps {
  columns: Array<{
    title: string;
    cards: Array<{ title: string; description?: string; tags?: string[] }>;
  }>;
}

export default function KanbanBoard({ columns }: KanbanBoardProps) {
  return (
    <div className="flex h-screen gap-4 relative overflow-x-auto bg-surface-muted p-6">
      {columns.map((column, i) => (
        <div key={i} className="flex w-72 shrink-0 flex-col rounded-xl bg-surface-muted/60">
          <div className="flex items-center justify-between px-4 py-3">
            <h3 className="text-sm font-semibold text-ink">{column.title}</h3>
            <span className="rounded-full bg-surface-muted px-2 py-0.5 text-xs font-medium text-ink-muted">
              {column.cards.length}
            </span>
          </div>

          <div className="flex-1 space-y-2 relative overflow-y-auto px-3 pb-3">
            {column.cards.map((card, j) => (
              <div key={j} className="rounded-lg border border-border bg-surface p-3">
                <p className="text-sm font-medium text-ink">{card.title}</p>
                {card.description && (
                  <p className="mt-1 text-xs text-ink-muted">{card.description}</p>
                )}
                {card.tags && card.tags.length > 0 && (
                  <div className="mt-2 flex flex-wrap gap-1">
                    {card.tags.map((tag, k) => (
                      <span
                        key={k}
                        className="inline-flex rounded bg-accent-surface px-1.5 py-0.5 text-xs font-medium text-accent-ink"
                      >
                        {tag}
                      </span>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}
