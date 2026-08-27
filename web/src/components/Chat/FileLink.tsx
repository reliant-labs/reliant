/**
 * FileLink component - clickable file path that opens files context-aware
 * Opens in command center if enabled, otherwise in the viewer panel
 * 
 * Supports path classification for visual indication:
 * - current: Normal clickable link
 * - other-worktree: Clickable with badge showing target workspace
 * - project-only: Clickable, slightly muted
 * - external: Non-clickable, muted styling
 */

import { memo, useCallback } from "react";
import { FileText, Ban } from "lucide-react";
import { cn } from "../../lib/utils";
import { parseFilePath, type ParsedFilePath } from "../../lib/filePath";
import { useFileOpener, usePathClassification, type PathClassification } from "../../lib/fileOpener";

interface FileLinkProps {
  /** File path string or parsed file path object */
  path: string | ParsedFilePath;
  /** Optional display text (defaults to filename) */
  children?: React.ReactNode;
  /** Optional CSS classes */
  className?: string;
  /** Show file icon */
  showIcon?: boolean;
  /** Worktree ID (optional, uses current worktree if not provided) */
  worktreeId?: string;
  /** Inline variant (no padding, smaller text) */
  inline?: boolean;
}

/**
 * Get styling classes based on path classification
 */
function getClassificationStyles(classification: PathClassification, inline: boolean): string {
  const baseStyles = inline
    ? "inline-flex items-center gap-1"
    : "inline-flex items-center gap-1 px-1 py-0.5 rounded";

  switch (classification) {
    case 'current':
      return cn(
        baseStyles,
        "text-primary hover:text-primary/80",
        "hover:underline cursor-pointer",
        "transition-colors duration-200",
        "focus:outline-none focus:ring-2 focus:ring-primary/50 focus:ring-offset-1"
      );
    case 'other-worktree':
      return cn(
        baseStyles,
        "text-primary hover:text-primary/80",
        "hover:underline cursor-pointer",
        "transition-colors duration-200",
        "focus:outline-none focus:ring-2 focus:ring-primary/50 focus:ring-offset-1"
      );
    case 'project-only':
      return cn(
        baseStyles,
        "text-primary/70 hover:text-primary/60",
        "hover:underline cursor-pointer",
        "transition-colors duration-200",
        "focus:outline-none focus:ring-2 focus:ring-primary/50 focus:ring-offset-1"
      );
    case 'external':
      return cn(
        baseStyles,
        "text-muted-foreground",
        "cursor-not-allowed"
      );
  }
}

function FileLinkComponent({
  path,
  children,
  className,
  showIcon = false,
  worktreeId,
  inline = false,
}: FileLinkProps) {
  const openFile = useFileOpener();

  // Parse path if it's a string
  const parsedPath = typeof path === "string" ? parseFilePath(path) : path;

  // Get path classification for visual styling
  const { classification, targetWorktreeId, matchedWorktree, isClickable, tooltipMessage } = 
    usePathClassification(parsedPath, worktreeId);

  // Hook must be called unconditionally, even if parsedPath is null
  const handleClick = useCallback(
    (e: React.MouseEvent) => {
      e.preventDefault();
      e.stopPropagation();
      if (parsedPath && isClickable) {
        // Use the targetWorktreeId from classification for other-worktree case
        openFile(parsedPath, targetWorktreeId || worktreeId);
      }
    },
    [parsedPath, isClickable, targetWorktreeId, worktreeId, openFile]
  );

  if (!parsedPath) {
    // If path is invalid, render as plain text
    return (
      <span className={className}>
        {children || (typeof path === "string" ? path : "")}
      </span>
    );
  }

  // Default display text
  const displayText =
    children || parsedPath.path.split("/").pop() || parsedPath.path;

  // Don't append :line suffix if custom children were provided (they likely include line info already)
  const shouldShowLineSuffix = !children && parsedPath.line;

  // Get worktree badge for other-worktree classification
  const worktreeBadge = classification === 'other-worktree' && matchedWorktree ? (
    <span className="text-3xs px-1 py-0.5 bg-muted rounded text-muted-foreground font-medium ml-1">
      {matchedWorktree.name}
    </span>
  ) : null;

  // External files render as non-clickable span
  if (!isClickable) {
    return (
      <span
        className={cn(
          "file-link",
          getClassificationStyles(classification, inline),
          className
        )}
        title={tooltipMessage}
      >
        {showIcon && <FileText className="w-3 h-3 flex-shrink-0 opacity-50" />}
        <span className={cn(inline && "text-xs font-mono")}>{displayText}</span>
        {shouldShowLineSuffix && (
          <span className="text-xs text-muted-foreground font-mono">
            :{parsedPath.line}
          </span>
        )}
        <Ban className="w-2.5 h-2.5 opacity-40" />
      </span>
    );
  }

  return (
    <button
      onClick={handleClick}
      className={cn(
        "file-link",
        getClassificationStyles(classification, inline),
        className
      )}
      title={tooltipMessage}
      type="button"
    >
      {showIcon && <FileText className="w-3 h-3 flex-shrink-0" />}
      <span className={cn(inline && "text-xs font-mono")}>{displayText}</span>
      {shouldShowLineSuffix && (
        <span className="text-xs text-muted-foreground font-mono">
          :{parsedPath.line}
        </span>
      )}
      {worktreeBadge}
    </button>
  );
}

export const FileLink = memo(FileLinkComponent);
