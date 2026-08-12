/**
 * Renderer for file tools (write, edit, patch)
 * Shows text diffs inline, but routes previewable/binary files through the shared file preview UI.
 */

import { memo, useEffect, useMemo, useState } from 'react';
import { Loader2 } from 'lucide-react';
import type { ToolContentProps, ToolResultData } from './types';
import { LightweightDiffViewer } from '../LightweightDiffViewer';
import { getFilePreviewInfo, type FilePreviewInfo } from '../../../api/fileSystem';
import { isBinaryFile, isImageFile } from '../../../lib/fileUtils';
import { FileViewerTab } from '../../FileBrowser/FileViewerTab';
import type { FileNode } from '../../FileBrowser';
import { useSurface } from '../../../lib/surfaceContext';

// On a phone viewport a diff this tall pushes the Approve/Deny buttons below
// the fold, turning "decide from your phone" into "scroll to find the
// decision." Desktop keeps the taller default since it has room to spare.
const MOBILE_DIFF_MAX_HEIGHT = 220;

const EXTRA_PREVIEW_EXTENSIONS = /\.(svg|mp3|m4a|ogg|oga|wav|flac|aac|mp4|mov|avi|mkv|webm)$/i;

function shouldCheckNonTextPreview(filePath?: string): boolean {
  if (!filePath) return false;
  return isImageFile(filePath) || isBinaryFile(filePath) || EXTRA_PREVIEW_EXTENSIONS.test(filePath);
}

function toPreviewFileNode(filePath: string): FileNode {
  return {
    name: filePath.split('/').pop() || filePath,
    path: filePath,
    type: 'file',
  };
}

function ToolErrorBanner({ result }: { result?: ToolResultData }) {
  if (!result?.is_error) {
    return null;
  }

  return (
    <div className="px-2 py-1.5 text-[11px] text-warning bg-warning/5 border-t border-border/50">
      {result.content}
    </div>
  );
}

interface PreviewAwareFileMutationProps {
  filePath?: string;
  originalContent: string;
  modifiedContent: string;
  worktreeId?: string;
  maxHeight?: number;
  disablePreview?: boolean;
}

function PreviewAwareFileMutation({
  filePath,
  originalContent,
  modifiedContent,
  worktreeId,
  maxHeight,
  disablePreview = false,
}: PreviewAwareFileMutationProps) {
  const surface = useSurface();
  const effectiveMaxHeight = maxHeight ?? (surface === "desktop" ? 250 : MOBILE_DIFF_MAX_HEIGHT);
  const shouldAttemptPreview = useMemo(
    () => !disablePreview && shouldCheckNonTextPreview(filePath),
    [disablePreview, filePath]
  );
  const [previewInfo, setPreviewInfo] = useState<FilePreviewInfo | null>(null);
  const [previewResolved, setPreviewResolved] = useState(() => !shouldAttemptPreview);

  useEffect(() => {
    let cancelled = false;

    if (!shouldAttemptPreview || !filePath) {
      setPreviewInfo(null);
      setPreviewResolved(true);
      return () => {
        cancelled = true;
      };
    }

    setPreviewResolved(false);
    setPreviewInfo(null);

    void getFilePreviewInfo(filePath, worktreeId)
      .then((info) => {
        if (cancelled) {
          return;
        }
        setPreviewInfo(info.viewerKind !== 'text' ? info : null);
      })
      .catch(() => {
        if (cancelled) {
          return;
        }
        setPreviewInfo(null);
      })
      .finally(() => {
        if (cancelled) {
          return;
        }
        setPreviewResolved(true);
      });

    return () => {
      cancelled = true;
    };
  }, [filePath, shouldAttemptPreview, worktreeId]);

  if (!previewResolved) {
    return (
      <div className="flex items-center justify-center min-h-[160px] rounded-md border border-border/50 bg-muted/10">
        <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (previewInfo && filePath) {
    return (
      <div className="h-[420px] overflow-hidden rounded-2xl bg-[hsl(var(--muted)/0.16)] shadow-[0_10px_24px_-22px_rgba(15,23,42,0.45)] ring-1 ring-border/25">
        <FileViewerTab file={toPreviewFileNode(filePath)} worktreeId={worktreeId} embedded />
      </div>
    );
  }

  if (!originalContent && !modifiedContent) {
    return null;
  }

  return (
    <LightweightDiffViewer
      original={originalContent}
      modified={modifiedContent}
      filename={filePath}
      maxHeight={effectiveMaxHeight}
      showLineNumbers={false}
      noBorder
    />
  );
}

function FileToolRendererComponent({ ctx }: ToolContentProps) {
  const { toolName, input, result, worktreeId } = ctx;

  // Parse input data
  let data = typeof input === 'string' ? {} : input;

  // Handle command property with JSON string
  if (data?.command && typeof data.command === 'string') {
    try {
      data = JSON.parse(data.command);
    } catch {
      // Keep original
    }
  }

  const isWrite = toolName.toLowerCase() === 'write';
  const isEdit = toolName.toLowerCase() === 'edit';
  const isPatch = toolName.toLowerCase() === 'patch';
  const disablePreview = !!result?.is_error;

  // Handle write tool
  if (isWrite) {
    const filePath = (data?.file_path || data?.FilePath) as string | undefined;
    let content = data?.content as string;
    const oldString = data?.old_string as string;
    const newString = data?.new_string as string;

    if (!content) {
      content = data?.Content as string;
    }

    const originalContent = oldString || '';
    const modifiedContent = newString || content || '';

    return (
      <div className="tool-content-file">
        <PreviewAwareFileMutation
          filePath={filePath}
          originalContent={originalContent}
          modifiedContent={modifiedContent}
          worktreeId={worktreeId}
          disablePreview={disablePreview}
        />
        <ToolErrorBanner result={result} />
      </div>
    );
  }

  // Handle edit/patch tools
  if (isEdit || isPatch) {
    // Check for MultiEdit format - handle both array and stringified array
    let edits = data?.edits as Record<string, unknown>[] | string | undefined;

    // If edits is a string, try to parse it
    if (typeof edits === 'string') {
      try {
        edits = JSON.parse(edits) as Record<string, unknown>[];
      } catch {
        edits = undefined;
      }
    }

    // Type guard: verify edits is a valid array before using
    const validEdits = edits && Array.isArray(edits) && edits.length > 0 ? edits : null;

    if (validEdits) {
      return (
        <div className="tool-content-file">
          {validEdits.map((edit, index) => {
            const editFilePath = (edit.file_path || edit.FilePath) as string | undefined;
            const editOldString = (edit.old_string as string) || '';
            const editNewString = (edit.new_string as string) || '';

            return (
              <div key={index} className={index > 0 ? 'mt-2' : ''}>
                {editFilePath && (
                  <div className="px-2 py-0.5 text-[10px] text-muted-foreground font-mono bg-muted/30 border-b border-border/30 truncate">
                    {editFilePath}
                  </div>
                )}
                <PreviewAwareFileMutation
                  filePath={editFilePath}
                  originalContent={editOldString}
                  modifiedContent={editNewString}
                  worktreeId={worktreeId}
                  disablePreview={disablePreview}
                />
              </div>
            );
          })}

          <ToolErrorBanner result={result} />
        </div>
      );
    }

    // Single edit format
    const filePath = (data?.file_path || data?.FilePath) as string | undefined;
    const oldString = (data?.old_string as string) || '';
    const newString = (data?.new_string as string) || '';

    return (
      <div className="tool-content-file">
        <PreviewAwareFileMutation
          filePath={filePath}
          originalContent={oldString}
          modifiedContent={newString}
          worktreeId={worktreeId}
          disablePreview={disablePreview}
        />
        <ToolErrorBanner result={result} />
      </div>
    );
  }

  return null;
}

export const FileToolRenderer = memo(FileToolRendererComponent);