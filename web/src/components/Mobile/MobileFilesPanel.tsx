/**
 * Read-only Files drill-in: browse via the existing `FileTree`, preview via
 * `MobileFilePreview` instead of the desktop `FileViewerTab` (Monaco — see
 * that component's module comment for why it's excluded here).
 *
 * `FileTreeToolbar` (new file/new folder) is deliberately not rendered, and
 * `creatingType` is never set, so the create flow this panel would otherwise
 * expose never mounts. `FileTree`'s per-item context menu is a desktop-only
 * right-click; touch devices have no equivalent gesture wired to it, so it
 * stays effectively unreachable without extra work.
 */

import { useState } from "react";
import { ChevronLeft, FileText } from "lucide-react";
import { FileTree } from "../FileBrowser/FileTree";
import type { FileNode } from "../FileBrowser/index";
import { MobileFilePreview } from "./MobileFilePreview";

interface MobileFilesPanelProps {
  worktreeId?: string;
}

export function MobileFilesPanel({ worktreeId }: MobileFilesPanelProps) {
  const [selectedFile, setSelectedFile] = useState<FileNode | null>(null);
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set());

  if (selectedFile) {
    return (
      <div className="flex h-full min-h-0 flex-col">
        <div className="flex min-h-[44px] items-center gap-2 border-b border-border px-2">
          <button
            type="button"
            onClick={() => setSelectedFile(null)}
            className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
            aria-label="Back to file list"
          >
            <ChevronLeft className="h-5 w-5" />
          </button>
          <FileText className="h-4 w-4 flex-shrink-0 text-muted-foreground" />
          <span className="truncate text-sm font-medium">{selectedFile.name}</span>
        </div>
        <MobileFilePreview path={selectedFile.path} worktreeId={worktreeId} />
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col overflow-y-auto">
      <FileTree
        searchQuery=""
        onFileSelect={(file) => {
          if (file.type === "file") setSelectedFile(file);
        }}
        onPathChange={() => {}}
        selectedFile={selectedFile}
        showHidden={false}
        onRefresh={() => {}}
        collapseKey={0}
        worktreeId={worktreeId}
        expandedPaths={expandedPaths}
        onExpandedPathsChange={setExpandedPaths}
      />
    </div>
  );
}
