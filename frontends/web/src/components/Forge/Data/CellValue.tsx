// Copyright (c) 2025 Reliant Labs

/**
 * One cell, and the popover that shows all of it.
 *
 * The grid holds ONE popover, anchored to whichever cell was opened, rather
 * than one per cell: a page is up to 500 rows by dozens of columns, and a
 * Popover's state and listeners per cell would cost more than the rows.
 */

import { useState } from "react";
import { Check, Copy, ExternalLink } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Link from "@/components/forge-ui/link";
import Popover from "@/components/forge-ui/popover";
import { cn } from "@/lib/utils";

import { cellDisplay, typeLabel, type CellDisplay } from "./dataCells-model";
import type { DbCell } from "@/services/controlPlane/observability/appDatabaseApi";

/** SQL NULL: a mark, never text that could be a value. */
export function NullMark() {
  return (
    <span className="inline-flex items-center rounded border border-dashed border-border px-1 font-sans text-2xs font-medium leading-4 text-ink-subtle" data-null="true">
      NULL
    </span>
  );
}

/** A cell's one-line form, in the grid. */
export function CellPreview({ display, numeric }: { display: CellDisplay; numeric?: boolean }) {
  if (display.kind === "null") return <NullMark />;
  return (
    <span
      className={cn(
        "block min-w-0 flex-1 truncate font-mono text-xs leading-5",
        display.kind === "json" ? "text-ink-muted" : "text-ink",
        numeric && "text-right tabular-nums"
      )}
    >
      {display.short === "" ? <span className="font-sans italic text-ink-subtle">empty</span> : display.short}
    </span>
  );
}

export function CopyButton({ text, label = "Copy", testId }: { text: string; label?: string; testId?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      variant="ghost"
      size="xs"
      leftIcon={copied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
      onClick={() => {
        void navigator.clipboard?.writeText(text).then(() => {
          setCopied(true);
          window.setTimeout(() => setCopied(false), 1500);
        });
      }}
      data-testid={testId}
    >
      {copied ? "Copied" : label}
    </Button>
  );
}

/** The whole value: pretty JSON, wrapped text, scrolled when long. */
export function FullValue({ display, testId }: { display: CellDisplay; testId?: string }) {
  if (display.kind === "null") return <NullMark />;
  return (
    <pre
      className="max-h-80 overflow-auto whitespace-pre-wrap break-all rounded-md border border-border/60 bg-surface-sunken p-2 font-mono text-xs text-ink"
      data-testid={testId}
    >
      {display.full === "" ? <span className="font-sans italic text-ink-subtle">empty string</span> : display.full}
    </pre>
  );
}

export interface OpenCell {
  key: string;
  column: string;
  pgType: string;
  value: DbCell;
  /** Where the referenced row is, for a foreign-key cell. */
  link?: { href: string; label: string };
  anchor: { x: number; y: number };
}

/** The grid's one popover, over the open cell. */
export function CellPopover({ cell, onClose }: { cell: OpenCell | null; onClose: () => void }) {
  if (!cell) return null;
  const display = cellDisplay(cell.value, cell.pgType);
  return (
    <Popover
      open
      onOpenChange={(open) => !open && onClose()}
      anchor={cell.anchor}
      label={`${cell.column} value`}
      panelClassName="w-[28rem] max-w-[calc(100vw-2rem)] space-y-2 p-3"
    >
      <div data-testid="data-cell-popover">
        <div className="mb-2 flex items-center justify-between gap-2">
          <p className="min-w-0 truncate text-xs text-ink-muted">
            <span className="font-mono font-medium text-ink">{cell.column}</span> · {typeLabel(cell.pgType)}
          </p>
          {display.kind !== "null" && <CopyButton text={display.full} testId="data-cell-copy" />}
        </div>
        <FullValue display={display} testId="data-cell-full" />
        {cell.link && (
          <Link href={cell.link.href} className="mt-2 inline-flex items-center gap-1 text-xs text-primary-ink hover:underline" data-testid="data-cell-link">
            <ExternalLink className="h-3 w-3" aria-hidden="true" />
            {cell.link.label}
          </Link>
        )}
      </div>
    </Popover>
  );
}
