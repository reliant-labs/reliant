// The Tooltip's public type contract, checked by `tsc` in
// TestComponentTypeContracts (library_type_contract_test.go). Every
// `@ts-expect-error` below is a misuse the type must REJECT.
import React from "react";

import Tooltip, { type TooltipSide } from "./tooltip";

const sides: TooltipSide[] = ["top", "bottom", "left", "right"];

export function Accepted() {
  const [open, setOpen] = React.useState(false);
  return (
    <>
      {sides.map((side) => (
        <Tooltip key={side} content="Rename" side={side}>
          <button type="button">Edit</button>
        </Tooltip>
      ))}
      <Tooltip content="Copy" type="label" shortcut={["⌘", "C"]} delay={0}>
        {(props) => (
          <button type="button" {...props}>
            <svg aria-hidden="true" />
          </button>
        )}
      </Tooltip>
      <Tooltip
        content={<span>Rich hint</span>}
        open={open}
        onOpenChange={setOpen}
        triggerClassName="flex"
      >
        <span>Truncated text</span>
      </Tooltip>
      <Tooltip content="Off" disabled focusable={false} tooltipClassName="text-left">
        <button type="button" disabled>
          Save
        </button>
      </Tooltip>
    </>
  );
}

export function Rejected() {
  return (
    <>
      {/* @ts-expect-error side is one of the four sides */}
      <Tooltip content="x" side="middle">
        <button type="button">A</button>
      </Tooltip>
      {/* @ts-expect-error type is description or label */}
      <Tooltip content="x" type="name">
        <button type="button">B</button>
      </Tooltip>
      {/* @ts-expect-error a shortcut is text */}
      <Tooltip content="x" shortcut={3}>
        <button type="button">C</button>
      </Tooltip>
      {/* @ts-expect-error a render-function trigger returns an element */}
      <Tooltip content="x">{() => "text"}</Tooltip>
    </>
  );
}
