import { cn } from "../../lib/utils";
import { getFileExtension } from "../../lib/fileUtils";
import { useFileOpener } from "../../lib/fileOpener";
import { parseFilePath } from "../../lib/filePath";

export interface CodeContextPillContext {
  filePath: string;
  fileName: string;
  startLine: number;
  endLine: number;
  language?: string;
}

interface CodeContextPillProps {
  context: CodeContextPillContext;
  worktreeId?: string;
  className?: string;
}

function getLanguageColor(lang?: string): string {
  const colors: Record<string, string> = {
    ts: "bg-blue-500",
    js: "bg-yellow-500",
    tsx: "bg-blue-600",
    jsx: "bg-yellow-600",
    py: "bg-green-500",
    go: "bg-cyan-500",
    rs: "bg-orange-500",
    java: "bg-red-500",
    cpp: "bg-purple-500",
    c: "bg-gray-500",
    html: "bg-orange-400",
    css: "bg-blue-400",
    json: "bg-green-400",
    yaml: "bg-purple-400",
    yml: "bg-purple-400",
    md: "bg-gray-400",
  };
  return colors[lang?.toLowerCase() || ""] || "bg-gray-500";
}

function getChipBgColor(lang?: string): string {
  const colors: Record<string, string> = {
    ts: "bg-blue-500/10 border-blue-500/30",
    js: "bg-yellow-500/10 border-yellow-500/30",
    tsx: "bg-blue-600/10 border-blue-600/30",
    jsx: "bg-yellow-600/10 border-yellow-600/30",
    py: "bg-green-500/10 border-green-500/30",
    go: "bg-cyan-500/10 border-cyan-500/30",
    rs: "bg-orange-500/10 border-orange-500/30",
    java: "bg-red-500/10 border-red-500/30",
    cpp: "bg-purple-500/10 border-purple-500/30",
    c: "bg-gray-500/10 border-gray-500/30",
    html: "bg-orange-400/10 border-orange-400/30",
    css: "bg-blue-400/10 border-blue-400/30",
    json: "bg-green-400/10 border-green-400/30",
    yaml: "bg-purple-400/10 border-purple-400/30",
    yml: "bg-purple-400/10 border-purple-400/30",
    md: "bg-gray-400/10 border-gray-400/30",
  };
  return colors[lang?.toLowerCase() || ""] || "bg-gray-500/10 border-gray-500/30";
}

export function CodeContextPill({ context, worktreeId, className }: CodeContextPillProps) {
  const openFile = useFileOpener();
  const language = context.language || getFileExtension(context.fileName);
  const lineRange =
    context.startLine === context.endLine
      ? `${context.startLine}`
      : `${context.startLine}-${context.endLine}`;
  const languageLabel = (language || "??").toUpperCase();
  const badgeText = languageLabel.length <= 3 ? languageLabel : languageLabel.slice(0, 2);

  const handleClick = (e: React.MouseEvent) => {
    e.stopPropagation();
    const parsedPath = parseFilePath(context.filePath);
    if (parsedPath) {
      parsedPath.line = context.startLine;
      parsedPath.lineEnd = context.endLine !== context.startLine ? context.endLine : undefined;
      parsedPath.column = 1;
      openFile(parsedPath, worktreeId);
      return;
    }
    openFile(
      {
        fullPath: context.filePath,
        path: context.filePath,
        line: context.startLine,
        lineEnd: context.endLine !== context.startLine ? context.endLine : undefined,
        column: 1,
        isAbsolute: context.filePath.startsWith("/"),
      },
      worktreeId
    );
  };

  return (
    <span
      className={cn(
        // Match the chat input marker token styling (not fully rounded).
        // IMPORTANT: box-border ensures the 1px border doesn't change height.
        "inline-flex items-center gap-1.5 h-5 px-2 py-0 rounded-md border box-border",
        getChipBgColor(language),
        "text-sm font-medium cursor-pointer select-none align-middle",
        "hover:opacity-80 transition-opacity",
        className
      )}
      onClick={handleClick}
      title={`Open ${context.fileName}:${context.startLine}-${context.endLine}`}
    >
      <span
        className={cn(
          "h-4 px-1.5 rounded flex items-center justify-center",
          "text-white text-[9px] font-bold leading-none",
          getLanguageColor(language)
        )}
      >
        {badgeText}
      </span>
      <span className="text-foreground">
        {context.fileName} ({lineRange})
      </span>
    </span>
  );
}

