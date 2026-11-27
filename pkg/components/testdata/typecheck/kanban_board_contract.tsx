// The KanbanBoard's public type contract, checked by `tsc` in
// TestComponentTypeContracts (library_type_contract_test.go). Every
// `@ts-expect-error` below is a misuse the type must REJECT.
import React from "react";

import KanbanBoard, { type KanbanColumn } from "./kanban_board";

const columns: KanbanColumn[] = [
  { id: "todo", title: "To do", cards: [{ id: "1", title: "Draft", tags: ["launch"] }] },
  { id: "done", title: "Done", tone: "success", cards: [{ id: "2", title: "Draft" }] },
];

export function Accepted() {
  return (
    <>
      <KanbanBoard columns={columns} />
      <KanbanBoard
        columns={columns}
        className="h-full"
        onCardClick={(card, column) => void [card.id, column.id]}
        onCardMove={(cardId: string, from: string, to: string) => void [cardId, from, to]}
      />
    </>
  );
}

export function Rejected() {
  return (
    <>
      {/* @ts-expect-error a card needs an id: titles are not identities */}
      <KanbanBoard columns={[{ id: "todo", title: "To do", cards: [{ title: "Draft" }] }]} />
      {/* @ts-expect-error a column needs an id */}
      <KanbanBoard columns={[{ title: "To do", cards: [] }]} />
      {/* @ts-expect-error there is no "info" tone */}
      <KanbanBoard columns={[{ id: "todo", title: "To do", tone: "info", cards: [] }]} />
    </>
  );
}
