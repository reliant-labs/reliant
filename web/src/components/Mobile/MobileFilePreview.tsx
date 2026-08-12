/**
 * Read-only file preview for the mobile Files drill-in.
 *
 * Deliberately NOT `FileViewerTab` — that component pulls in
 * `@monaco-editor/react`, which `main.tsx` explicitly excludes from the
 * `/m/*` bundle (see `shouldPreloadMonaco`). Importing it here would drag
 * Monaco back into the mobile bundle by a side door. This uses the same
 * Prism-based `LightweightCodeViewer` the chat timeline already renders code
 * with, plus a plain `<img>` for images — both well under Monaco's footprint.
 */

import { useEffect, useState } from "react";
import { Loader2, AlertCircle, FileWarning } from "lucide-react";
import {
  getFileContent,
  getFilePreviewInfo,
  getFilePreviewBlob,
  type FilePreviewInfo,
} from "../../api/fileSystem";
import { LightweightCodeViewer } from "../Chat/LightweightCodeViewer";
import { cn } from "../../lib/utils";

const EXTENSION_LANGUAGE: Record<string, string> = {
  ts: "typescript",
  tsx: "typescript",
  js: "javascript",
  jsx: "javascript",
  py: "python",
  sh: "bash",
  bash: "bash",
  json: "json",
  yaml: "yaml",
  yml: "yaml",
  md: "markdown",
  css: "css",
  scss: "css",
  go: "go",
  rs: "rust",
  sql: "sql",
  html: "markup",
  xml: "markup",
};

function languageForPath(path: string): string {
  const ext = path.split(".").pop()?.toLowerCase() ?? "";
  return EXTENSION_LANGUAGE[ext] ?? "plaintext";
}

interface MobileFilePreviewProps {
  path: string;
  worktreeId?: string;
}

type PreviewState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "text"; content: string }
  | { status: "image"; url: string }
  | { status: "unsupported"; info: FilePreviewInfo };

export function MobileFilePreview({ path, worktreeId }: MobileFilePreviewProps) {
  const [state, setState] = useState<PreviewState>({ status: "loading" });

  useEffect(() => {
    let cancelled = false;
    let objectUrl: string | null = null;
    setState({ status: "loading" });

    (async () => {
      try {
        const info = await getFilePreviewInfo(path, worktreeId);
        if (cancelled) return;

        if (info.viewerKind === "text") {
          const content = await getFileContent(path, worktreeId);
          if (cancelled) return;
          setState({ status: "text", content });
        } else if (info.viewerKind === "image") {
          const blob = await getFilePreviewBlob(path, worktreeId);
          if (cancelled) return;
          objectUrl = URL.createObjectURL(blob);
          setState({ status: "image", url: objectUrl });
        } else {
          setState({ status: "unsupported", info });
        }
      } catch (err) {
        if (cancelled) return;
        setState({
          status: "error",
          message: err instanceof Error ? err.message : "Failed to load file",
        });
      }
    })();

    return () => {
      cancelled = true;
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [path, worktreeId]);

  if (state.status === "loading") {
    return (
      <div className="flex flex-1 items-center justify-center py-10 text-muted-foreground">
        <Loader2 className="h-5 w-5 animate-spin" />
      </div>
    );
  }

  if (state.status === "error") {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-2 py-10 text-center text-sm text-destructive">
        <AlertCircle className="h-5 w-5" />
        <span>{state.message}</span>
      </div>
    );
  }

  if (state.status === "unsupported") {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-2 py-10 text-center text-sm text-muted-foreground">
        <FileWarning className="h-5 w-5" />
        <span>Preview unavailable for this file type ({state.info.viewerKind}).</span>
      </div>
    );
  }

  if (state.status === "image") {
    return (
      <div className="flex flex-1 items-center justify-center overflow-auto p-4">
        <img
          src={state.url}
          alt={path}
          className={cn("max-w-full rounded-md border border-border")}
        />
      </div>
    );
  }

  return (
    <div className="flex-1 overflow-auto p-2">
      <LightweightCodeViewer
        content={state.content}
        language={languageForPath(path)}
        maxHeight={Number.MAX_SAFE_INTEGER}
        wordWrap
        noBorder
      />
    </div>
  );
}
