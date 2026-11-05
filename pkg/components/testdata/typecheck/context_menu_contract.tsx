// The ContextMenu's public type contract, checked by `tsc` in
// TestComponentTypeContracts (library_type_contract_test.go). Every
// `@ts-expect-error` below is a misuse the type must REJECT.
import React from "react";

import ContextMenu from "./context_menu";
import DropdownMenu from "./dropdown_menu";
import Popover from "./popover";

const groups = [{ items: [{ label: "Rename", onClick: () => undefined }] }];

export function Accepted() {
  const [point, setPoint] = React.useState<{ x: number; y: number } | null>(null);
  return (
    <>
      <ContextMenu label="Row actions" groups={groups}>
        <div>Right-click me</div>
      </ContextMenu>
      <ContextMenu
        label="Chat actions"
        groups={groups}
        open={point !== null}
        position={point ?? undefined}
        openVia="keyboard"
        onOpenChange={(open) => !open && setPoint(null)}
        density="compact"
      />
      <DropdownMenu anchor={{ x: 10, y: 20 }} open groups={groups} label="At a point" />
      <Popover anchor={{ x: 10, y: 20 }} open label="Preview">
        Body
      </Popover>
    </>
  );
}

export function Rejected() {
  return (
    <>
      {/* @ts-expect-error a context menu needs an accessible name */}
      <ContextMenu groups={groups} />
      {/* @ts-expect-error a position is a point */}
      <ContextMenu label="x" groups={groups} position={{ left: 1, top: 2 }} />
      {/* @ts-expect-error openVia is pointer, keyboard or keyboard-last */}
      <ContextMenu label="x" groups={groups} openVia="mouse" />
    </>
  );
}
