/**
 * `@monaco-editor/react`'s `<Editor>`, mounted only once the app's own Monaco
 * has loaded (`lib/monacoManager`).
 *
 * `<Editor>` calls `loader.init()` as it mounts, and the loader reads its
 * config only on that first call: an editor that mounted before
 * `monacoManager` had handed it the bundled Monaco would make the loader
 * inject its default CDN script instead. So no component renders `<Editor>`
 * directly (`lib/__tests__/monacoImports.guard.test.ts`); they render this,
 * which shows `loading` until the bundled Monaco is ready. It also keeps
 * `@monaco-editor/react` itself out of the entry chunk.
 */

import { useEffect, useState, type ComponentType, type ReactNode } from "react";
import type { EditorProps } from "@monaco-editor/react";

import { monacoManager } from "../../lib/monacoManager";
import { logger } from "../../lib/logger";

type EditorComponent = ComponentType<EditorProps>;

let loadedEditor: EditorComponent | null = null;

async function loadEditor(): Promise<EditorComponent> {
  await monacoManager.getMonaco();
  const { Editor } = await import("@monaco-editor/react");
  loadedEditor = Editor;
  return Editor;
}

export type MonacoEditorProps = EditorProps & { loading?: ReactNode };

export function MonacoEditor({ loading = null, ...props }: MonacoEditorProps) {
  const [Editor, setEditor] = useState<EditorComponent | null>(() => loadedEditor);

  useEffect(() => {
    if (Editor) return;
    let mounted = true;
    loadEditor()
      .then((component) => {
        if (mounted) setEditor(() => component);
      })
      .catch((error: unknown) => {
        // The placeholder stays: the editor is unavailable, the page is not.
        logger.error("[MonacoEditor] The editor could not load:", error);
      });
    return () => {
      mounted = false;
    };
  }, [Editor]);

  if (!Editor) return <>{loading}</>;
  return <Editor loading={loading} {...props} />;
}
