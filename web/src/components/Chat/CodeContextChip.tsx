/**
 * CodeContextChip - Displays a code context reference in chat input
 * 
 * Shows file name, line range, and allows removal/opening the file
 */

import { X } from 'lucide-react';
import { cn } from '../../lib/utils';
import { getFileExtension } from '../../lib/fileUtils';
import { useFileOpener } from '../../lib/fileOpener';
import { parseFilePath } from '../../lib/filePath';

interface CodeContextChipProps {
  context: {
    id: string;
    filePath: string;
    fileName: string;
    startLine: number;
    endLine: number;
    language?: string;
  };
  onRemove: () => void;
  worktreeId?: string;
  className?: string;
}

export function CodeContextChip({
  context,
  onRemove,
  worktreeId,
  className,
}: CodeContextChipProps) {
  const openFile = useFileOpener();
  const language = context.language || getFileExtension(context.fileName);
  const lineRange =
    context.startLine === context.endLine
      ? `${context.startLine}`
      : `${context.startLine}-${context.endLine}`;
  const languageLabel = (language || "??").toUpperCase();

  const handleClick = (e: React.MouseEvent) => {
    e.stopPropagation();
    
    // Create a proper ParsedFilePath object from the context
    // The filePath should already be absolute from FileViewerTab
    const parsedPath = parseFilePath(context.filePath);
    
    if (parsedPath) {
      // Update with line information from context
      parsedPath.line = context.startLine;
      parsedPath.lineEnd = context.endLine !== context.startLine ? context.endLine : undefined;
      parsedPath.column = 1;
      
      // Open the file (will focus if already open)
      openFile(parsedPath, worktreeId);
    } else {
      // Fallback: try to create a parsed path manually
      const isAbsolute = context.filePath.startsWith('/');
      openFile({
        fullPath: context.filePath,
        path: context.filePath,
        line: context.startLine,
        lineEnd: context.endLine !== context.startLine ? context.endLine : undefined,
        column: 1,
        isAbsolute,
      }, worktreeId);
    }
  };

  const handleRemove = (e: React.MouseEvent) => {
    e.stopPropagation();
    onRemove();
  };

  // Get language color (similar to VS Code/Cursor)
  const getLanguageColor = (lang?: string): string => {
    const colors: Record<string, string> = {
      ts: 'bg-blue-500',
      js: 'bg-yellow-500',
      tsx: 'bg-blue-600',
      jsx: 'bg-yellow-600',
      py: 'bg-green-500',
      go: 'bg-cyan-500',
      rs: 'bg-orange-500',
      java: 'bg-red-500',
      cpp: 'bg-purple-500',
      c: 'bg-gray-500',
      html: 'bg-orange-400',
      css: 'bg-blue-400',
      json: 'bg-green-400',
      yaml: 'bg-purple-400',
      yml: 'bg-purple-400',
      md: 'bg-gray-400',
    };
    return colors[lang?.toLowerCase() || ''] || 'bg-gray-500';
  };

  // Get background color for chip (lighter version of language color)
  const getChipBgColor = (lang?: string): string => {
    const colors: Record<string, string> = {
      ts: 'bg-blue-500/10 border-blue-500/30',
      js: 'bg-yellow-500/10 border-yellow-500/30',
      tsx: 'bg-blue-600/10 border-blue-600/30',
      jsx: 'bg-yellow-600/10 border-yellow-600/30',
      py: 'bg-green-500/10 border-green-500/30',
      go: 'bg-cyan-500/10 border-cyan-500/30',
      rs: 'bg-orange-500/10 border-orange-500/30',
      java: 'bg-red-500/10 border-red-500/30',
      cpp: 'bg-purple-500/10 border-purple-500/30',
      c: 'bg-gray-500/10 border-gray-500/30',
      html: 'bg-orange-400/10 border-orange-400/30',
      css: 'bg-blue-400/10 border-blue-400/30',
      json: 'bg-green-400/10 border-green-400/30',
      yaml: 'bg-purple-400/10 border-purple-400/30',
      yml: 'bg-purple-400/10 border-purple-400/30',
      md: 'bg-gray-400/10 border-gray-400/30',
    };
    return colors[lang?.toLowerCase() || ''] || 'bg-gray-500/10 border-gray-500/30';
  };

  const chipBgColor = getChipBgColor(language);

  return (
    <div
      className={cn(
        'inline-flex items-center gap-1.5 px-2 py-1 rounded-full',
        chipBgColor,
        'text-sm font-medium',
        'hover:opacity-80',
        'transition-colors cursor-pointer group',
        className
      )}
      onClick={handleClick}
      title={`Click to open ${context.fileName} at line ${context.startLine}`}
    >
      {/* Language indicator with remove button on hover */}
      <div className="relative group/badge">
        <div
          className={cn(
            'h-5 px-1.5 rounded-full flex items-center justify-center',
            'text-white text-[10px] font-bold leading-none',
            'transition-opacity group-hover/badge:opacity-0',
            getLanguageColor(language)
          )}
        >
          {languageLabel.length <= 3 ? languageLabel : languageLabel.slice(0, 2)}
        </div>
        {/* Remove button (shown on hover over badge) - uses same color as logo */}
        <button
          onClick={handleRemove}
          className={cn(
            'absolute inset-0 opacity-0 group-hover/badge:opacity-100',
            'h-5 px-1.5 rounded-full flex items-center justify-center',
            'text-white',
            'transition-opacity pointer-events-none group-hover/badge:pointer-events-auto',
            'z-10',
            getLanguageColor(language)
          )}
          title="Remove from chat"
        >
          <X className="w-3.5 h-3.5 stroke-[2.5]" />
        </button>
      </div>

      {/* File name and line range */}
      <span className="text-foreground">
        {context.fileName} ({lineRange})
      </span>
    </div>
  );
}
