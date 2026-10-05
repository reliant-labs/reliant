import { forwardRef, type MouseEvent, type ReactNode } from "react";
import { Loader2 } from "lucide-react";
import { FileIcon } from "../../ui/FileIcon";
import { cn } from "../../../lib/utils";
import { STATUS_LETTER_META, lineCountsOf, splitPath, statusLetterOf } from "./fileStatus";
import type { FileChange } from "./types";

interface FileChangeRowProps {
  file: FileChange;
  selected: boolean;
  processing: boolean;
  /** Keep the actions visible (the selected / keyboard-cursor row), so they
   *  are reachable without a mouse. Otherwise they appear on hover. */
  pinActions: boolean;
  onClick: (e: MouseEvent<HTMLDivElement>) => void;
  /** Icon buttons. Absent in read-only mode (project checkout). */
  actions?: ReactNode;
}

/**
 * One changed file: icon, name, dimmed directory, +/- counts and a status
 * letter. Clicking anywhere on the row opens its diff; the action buttons
 * replace the line counts on hover so the row never gets wider.
 */
export const FileChangeRow = forwardRef<HTMLDivElement, FileChangeRowProps>(function FileChangeRow(
  { file, selected, processing, pinActions, onClick, actions },
  ref,
) {
  const { name, dir } = splitPath(file.path);
  const letter = statusLetterOf(file);
  const letterMeta = STATUS_LETTER_META[letter];
  const counts = lineCountsOf(file);

  return (
    <div
      ref={ref}
      role="listitem"
      aria-current={selected || undefined}
      data-selected={selected || undefined}
      onClick={onClick}
      className={cn(
        "group/row flex h-7 cursor-pointer items-center gap-2 rounded-md pl-2 pr-1.5 text-sm transition-colors",
        selected ? "bg-primary/10 text-foreground" : "text-foreground/80 hover:bg-muted/60 hover:text-foreground",
        processing && "opacity-60",
      )}
    >
      <div title={file.path} className="flex min-w-0 flex-1 items-center gap-2">
        <FileIcon fileName={file.path} className="h-4 w-4 shrink-0" />
        <span className={cn("truncate text-xs", letter === "D" && "line-through decoration-muted-foreground/60")}>
          {name}
        </span>
        {dir && <span className="min-w-0 flex-1 truncate text-2xs text-muted-foreground">{dir}</span>}
      </div>

      {processing ? (
        <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-muted-foreground" aria-label="Working" />
      ) : (
        <>
          {counts && (counts.added > 0 || counts.removed > 0) && (
            <span
              className={cn(
                "shrink-0 font-mono text-2xs tabular-nums",
                actions && (pinActions ? "hidden" : "group-hover/row:hidden group-focus-within/row:hidden"),
              )}
            >
              {counts.added > 0 && <span className="text-success">+{counts.added}</span>}
              {counts.added > 0 && counts.removed > 0 && " "}
              {counts.removed > 0 && <span className="text-destructive">−{counts.removed}</span>}
            </span>
          )}
          {actions && (
            <div
              className={cn(
                "shrink-0 items-center gap-0.5",
                pinActions ? "flex" : "hidden group-hover/row:flex group-focus-within/row:flex",
              )}
              onClick={(e) => e.stopPropagation()}
            >
              {actions}
            </div>
          )}
        </>
      )}

      <span
        className={cn("w-3 shrink-0 text-center font-mono text-xs font-semibold", letterMeta.className)}
        title={letterMeta.label}
      >
        {letter}
        <span className="sr-only"> ({letterMeta.label})</span>
      </span>
    </div>
  );
});
