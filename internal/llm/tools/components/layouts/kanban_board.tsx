import React from "react";

interface KanbanBoardProps {
  columns: Array<{
    title: string;
    cards: Array<{ title: string; description?: string; tags?: string[] }>;
  }>;
}

export default function KanbanBoard({ columns }: KanbanBoardProps) {
  return (
    <div className="flex h-screen gap-4 overflow-x-auto bg-gray-100 p-6">
      {columns.map((column, i) => (
        <div
          key={i}
          className="flex w-72 shrink-0 flex-col rounded-xl bg-gray-200/60"
        >
          <div className="flex items-center justify-between px-4 py-3">
            <h3 className="text-sm font-semibold text-gray-900">
              {column.title}
            </h3>
            <span className="rounded-full bg-gray-300 px-2 py-0.5 text-xs font-medium text-gray-700">
              {column.cards.length}
            </span>
          </div>

          <div className="flex-1 space-y-2 overflow-y-auto px-3 pb-3">
            {column.cards.map((card, j) => (
              <div key={j} className="rounded-lg bg-white p-3 shadow-sm">
                <p className="text-sm font-medium text-gray-900">
                  {card.title}
                </p>
                {card.description && (
                  <p className="mt-1 text-xs text-gray-500">
                    {card.description}
                  </p>
                )}
                {card.tags && card.tags.length > 0 && (
                  <div className="mt-2 flex flex-wrap gap-1">
                    {card.tags.map((tag, k) => (
                      <span
                        key={k}
                        className="inline-flex rounded bg-indigo-50 px-1.5 py-0.5 text-xs font-medium text-indigo-700"
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
