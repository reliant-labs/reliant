import { useCallback, useEffect, useState } from "react";
import { getFileExtension } from "../../lib/fileUtils";

/**
 * A file range attached to the message.
 *
 * These arrive from the file viewer as `[[path:start-end]]` marker text. They
 * are held as structured state rather than spliced into the message body so the
 * composer's contents stay pure text — which is what lets the composer be a
 * plain <textarea> instead of a contentEditable div.
 *
 * Nothing is lost by keeping them out of the text: the send path already
 * stripped markers out of the body and appended the file contents at the end,
 * so their position within the message was never meaningful.
 */
export interface CodeContext {
  id: string;
  filePath: string;
  fileName: string;
  startLine: number;
  endLine: number;
  language?: string;
}

export const MARKER_PATTERN = /\[\[([^\]]+):(\d+)-(\d+)\]\]/g;

/** Build a context from the parts of a `[[path:start-end]]` marker. */
export function markerToContext(
  filePath: string,
  startLine: number,
  endLine: number,
): CodeContext {
  const fileName = filePath.split("/").pop() || filePath;
  return {
    id: `${filePath}:${startLine}-${endLine}`,
    filePath,
    fileName,
    startLine,
    endLine,
    language: getFileExtension(fileName),
  };
}

/**
 * Pull `[[path:start-end]]` markers out of text.
 *
 * Text arriving from outside the composer — a restored draft written before
 * this change, or a marker inserted while the composer was unmounted — may
 * still carry marker syntax. Lifting it into chips on the way in means the user
 * never sees the raw form.
 */
export function extractMarkers(text: string): {
  text: string;
  contexts: CodeContext[];
} {
  const contexts: CodeContext[] = [];
  MARKER_PATTERN.lastIndex = 0;

  const stripped = text.replace(
    MARKER_PATTERN,
    (_match, filePath: string, startStr: string, endStr: string) => {
      contexts.push(
        markerToContext(filePath, parseInt(startStr, 10), parseInt(endStr, 10)),
      );
      return "";
    },
  );

  // Tidy the gap a removed marker leaves behind, without touching the user's
  // own line structure.
  return { text: stripped.replace(/[ \t]{2,}/g, " ").trim(), contexts };
}

/**
 * Owns the code contexts attached to the next message.
 *
 * Lives in the sending component rather than in the composer: the send path
 * needs to read these, and the composer only renders them.
 */
export function useCodeContexts() {
  const [contexts, setContexts] = useState<CodeContext[]>([]);

  const addContext = useCallback((context: CodeContext) => {
    setContexts((current) =>
      current.some((c) => c.id === context.id) ? current : [...current, context],
    );
  }, []);

  const removeContext = useCallback((id: string) => {
    setContexts((current) => current.filter((c) => c.id !== id));
  }, []);

  const clearContexts = useCallback(() => setContexts([]), []);

  // The file viewer announces a new reference by dispatching a marker.
  useEffect(() => {
    const handleAddMarker = (e: Event) => {
      const marker = (e as CustomEvent<{ marker: string }>).detail?.marker;
      if (!marker) return;

      MARKER_PATTERN.lastIndex = 0;
      const match = MARKER_PATTERN.exec(marker);
      if (!match) return;

      const [, filePath, startStr, endStr] = match;
      addContext(
        markerToContext(filePath, parseInt(startStr, 10), parseInt(endStr, 10)),
      );

      // Put the user back in the composer, ready to type about what they added.
      window.dispatchEvent(new CustomEvent("focus-chat-input"));
    };

    window.addEventListener("add-context-marker", handleAddMarker);
    return () =>
      window.removeEventListener("add-context-marker", handleAddMarker);
  }, [addContext]);

  return { contexts, addContext, removeContext, clearContexts };
}
