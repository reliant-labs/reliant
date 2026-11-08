import React, { useEffect, useRef, useState } from "react";

/**
 * KanbanBoard — columns of cards, for work moving through stages.
 *
 * It fills its container (give the parent a height) rather than the
 * viewport, so it sits inside a page beside a header or a sidebar. It
 * renders what it is given: moving a card is reported through `onCardMove`,
 * and the board shows the move once the caller passes the new columns.
 *
 * Moving cards, when `onCardMove` is set:
 *
 *   - with a pointer, drag a card onto another column;
 *   - from the keyboard, focus a card and press Alt+ArrowLeft or
 *     Alt+ArrowRight to move it one column. Focus follows the card, and the
 *     move is announced to screen readers.
 *
 * `tone` colours a column's marker from the theme tokens (accent, success,
 * warning, danger), so it follows light and dark mode.
 *
 * Usage:
 * ```tsx
 * <div className="h-[calc(100vh-8rem)]">
 *   <KanbanBoard
 *     columns={[
 *       { id: "todo", title: "To do", cards: [{ id: "1", title: "Draft launch post" }] },
 *       { id: "done", title: "Done", tone: "success", cards: [] },
 *     ]}
 *     onCardMove={(cardId, from, to) => move(cardId, to)}
 *     onCardClick={(card) => open(card.id)}
 *   />
 * </div>
 * ```
 */

export type KanbanTone = "neutral" | "accent" | "success" | "warning" | "danger";

export interface KanbanCard {
  /**
   * Stable identity, unique across the board: the React key, the drag
   * payload, and what onCardMove reports. Titles are not identities — two
   * cards may share one.
   */
  id: string;
  title: string;
  description?: string;
  tags?: string[];
}

export interface KanbanColumn {
  /** Stable identity, unique across the board, reported by onCardMove. */
  id: string;
  title: string;
  cards: KanbanCard[];
  /** Colour of the column's marker. Defaults to "neutral". */
  tone?: KanbanTone;
}

interface KanbanBoardProps {
  columns: KanbanColumn[];
  /** Makes each card a button. */
  onCardClick?: (card: KanbanCard, column: KanbanColumn) => void;
  /** Enables drag and keyboard moves between columns. */
  onCardMove?: (cardId: string, fromColumnId: string, toColumnId: string) => void;
  /** Classes on the board (height, padding). */
  className?: string;
}

const toneDot: Record<KanbanTone, string> = {
  neutral: "bg-ink-subtle",
  accent: "bg-accent",
  success: "bg-success",
  warning: "bg-warning",
  danger: "bg-danger",
};

const DRAG_TYPE = "application/x-kanban-card";

export default function KanbanBoard({
  columns,
  onCardClick,
  onCardMove,
  className,
}: KanbanBoardProps) {
  const boardRef = useRef<HTMLDivElement>(null);
  const [dragOver, setDragOver] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");
  // The card a keyboard move should refocus once it renders in its new column.
  const refocus = useRef<string | null>(null);

  useEffect(() => {
    const id = refocus.current;
    if (!id) return;
    // Compared through dataset rather than interpolated into a selector, so
    // an id holding quotes or backslashes cannot break the query.
    const el = Array.from(
      boardRef.current?.querySelectorAll<HTMLElement>("[data-kanban-card]") ?? [],
    ).find((card) => card.dataset.kanbanCard === id);
    if (el) {
      el.focus();
      refocus.current = null;
    }
  }, [columns]);

  function move(card: KanbanCard, from: KanbanColumn, to: KanbanColumn) {
    if (!onCardMove || from.id === to.id) return;
    onCardMove(card.id, from.id, to.id);
    setAnnouncement(`Moved ${card.title} to ${to.title}`);
  }

  // Drops land on a column's zone (data-kanban-column). Native listeners,
  // delegated from the board: a column is a region, not a control, and the
  // keyboard path is the card's own Alt+Arrow keys. They read the latest
  // columns and move through a ref, so they attach once.
  const dropRef = useRef({ columns, move });
  useEffect(() => {
    dropRef.current = { columns, move };
  });
  useEffect(() => {
    const board = boardRef.current;
    if (!board || !onCardMove) return;
    const zoneOf = (e: DragEvent) =>
      e.target instanceof Element ? e.target.closest<HTMLElement>("[data-kanban-column]") : null;
    function onDragOver(e: DragEvent) {
      const zone = zoneOf(e);
      if (!zone || !e.dataTransfer?.types.includes(DRAG_TYPE)) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";
      setDragOver(zone.dataset.kanbanColumn ?? null);
    }
    function onDragLeave(e: DragEvent) {
      if (!board?.contains(e.relatedTarget as Node | null)) setDragOver(null);
    }
    function onDrop(e: DragEvent) {
      setDragOver(null);
      const zone = zoneOf(e);
      const raw = e.dataTransfer?.getData(DRAG_TYPE);
      if (!zone || !raw) return;
      e.preventDefault();
      let payload: { card?: unknown; from?: unknown };
      try {
        payload = JSON.parse(raw) as typeof payload;
      } catch {
        return;
      }
      const { columns: cols, move: doMove } = dropRef.current;
      const from = cols.find((c) => c.id === payload.from);
      const to = cols.find((c) => c.id === zone.dataset.kanbanColumn);
      const card = from?.cards.find((c) => c.id === payload.card);
      if (from && to && card) doMove(card, from, to);
    }
    function onDragEnd() {
      setDragOver(null);
    }
    board.addEventListener("dragover", onDragOver);
    board.addEventListener("dragleave", onDragLeave);
    board.addEventListener("drop", onDrop);
    board.addEventListener("dragend", onDragEnd);
    return () => {
      board.removeEventListener("dragover", onDragOver);
      board.removeEventListener("dragleave", onDragLeave);
      board.removeEventListener("drop", onDrop);
      board.removeEventListener("dragend", onDragEnd);
    };
  }, [onCardMove]);

  function onCardKeyDown(e: React.KeyboardEvent, card: KanbanCard, colIndex: number) {
    if (!onCardMove || !e.altKey) return;
    const step = e.key === "ArrowLeft" ? -1 : e.key === "ArrowRight" ? 1 : 0;
    const from = columns[colIndex];
    const to = columns[colIndex + step];
    if (step === 0 || !from || !to) return;
    e.preventDefault();
    refocus.current = card.id;
    move(card, from, to);
  }

  return (
    <div
      ref={boardRef}
      className={`relative flex h-full min-h-0 gap-4 overflow-x-auto ${className ?? ""}`}
    >
      {columns.map((column, colIndex) => {
        const id = column.id;
        return (
          <section key={id} aria-label={column.title} className="flex w-72 shrink-0 flex-col">
            <div
              data-kanban-column={id}
              className={`flex min-h-0 flex-1 flex-col rounded-xl bg-surface-muted ${
                dragOver === id ? "outline-2 -outline-offset-2 outline-accent" : ""
              }`}
            >
              <div className="flex items-center justify-between gap-2 px-4 py-3">
                <h3 className="flex min-w-0 items-center gap-2 text-sm font-semibold text-ink">
                  <span
                    aria-hidden="true"
                    className={`size-2 shrink-0 rounded-full ${toneDot[column.tone ?? "neutral"]}`}
                  />
                  <span className="truncate">{column.title}</span>
                </h3>
                <span className="rounded-full bg-surface px-2 py-0.5 text-xs font-medium text-ink-muted">
                  {column.cards.length}
                </span>
              </div>

              <ul className="relative min-h-16 flex-1 space-y-2 overflow-y-auto px-3 pb-3">
                {column.cards.map((card) => {
                  const body = (
                    <>
                      <span className="block text-sm font-medium text-ink">{card.title}</span>
                      {card.description && (
                        <span className="mt-1 block text-xs text-ink-muted">
                          {card.description}
                        </span>
                      )}
                      {card.tags && card.tags.length > 0 && (
                        <span className="mt-2 flex flex-wrap gap-1">
                          {card.tags.map((tag) => (
                            <span
                              key={tag}
                              className="inline-flex rounded bg-accent-surface px-1.5 py-0.5 text-xs font-medium text-accent-ink"
                            >
                              {tag}
                            </span>
                          ))}
                        </span>
                      )}
                    </>
                  );
                  const cardClass =
                    "block w-full rounded-lg border border-border bg-surface p-3 text-left";
                  const interactive = onCardClick || onCardMove;
                  return (
                    <li key={card.id}>
                      {interactive ? (
                        <button
                          type="button"
                          data-kanban-card={card.id}
                          draggable={Boolean(onCardMove)}
                          aria-keyshortcuts={
                            onCardMove ? "Alt+ArrowLeft Alt+ArrowRight" : undefined
                          }
                          onClick={onCardClick ? () => onCardClick(card, column) : undefined}
                          onKeyDown={(e) => onCardKeyDown(e, card, colIndex)}
                          onDragStart={(e) => {
                            e.dataTransfer.setData(
                              DRAG_TYPE,
                              JSON.stringify({ card: card.id, from: id }),
                            );
                            e.dataTransfer.effectAllowed = "move";
                          }}
                          className={`${cardClass} transition-colors hover:border-border-strong focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent ${
                            onCardMove ? "cursor-grab active:cursor-grabbing" : ""
                          }`}
                        >
                          {body}
                        </button>
                      ) : (
                        <div className={cardClass}>{body}</div>
                      )}
                    </li>
                  );
                })}
              </ul>
            </div>
          </section>
        );
      })}
      <span className="sr-only" aria-live="polite">
        {announcement}
      </span>
    </div>
  );
}
