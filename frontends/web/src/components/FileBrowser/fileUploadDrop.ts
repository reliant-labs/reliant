/**
 * Reading OS files out of a drag-and-drop onto the file tree.
 *
 * A drag carries files only when its `types` include "Files"; anything else
 * (text, a link, an in-app drag) is left to whoever handles it today. A
 * dropped folder arrives as a File too, with no way to read its contents
 * through `files`, so folders are told apart through `webkitGetAsEntry()`
 * and skipped: uploading a whole folder is a later phase.
 */

/** Whether a drag carries files from the operating system. */
export function dragHasFiles(dataTransfer: DataTransfer | null): boolean {
  if (!dataTransfer) return false;
  return Array.from(dataTransfer.types ?? []).includes("Files");
}

export interface DroppedFiles {
  files: File[];
  /** Names of the folders that were dropped, which are not uploaded. */
  skippedFolders: string[];
}

export function filesFromDrop(dataTransfer: DataTransfer): DroppedFiles {
  const items = dataTransfer.items ? Array.from(dataTransfer.items) : [];
  if (items.length === 0) {
    return { files: Array.from(dataTransfer.files ?? []), skippedFolders: [] };
  }
  const files: File[] = [];
  const skippedFolders: string[] = [];
  for (const item of items) {
    if (item.kind !== "file") continue;
    const entry = typeof item.webkitGetAsEntry === "function" ? item.webkitGetAsEntry() : null;
    const file = item.getAsFile();
    if (entry?.isDirectory) {
      skippedFolders.push(entry.name || file?.name || "folder");
      continue;
    }
    if (file) files.push(file);
  }
  return { files, skippedFolders };
}

/** The message for folders a drop skipped, or null when there were none. */
export function skippedFoldersMessage(skippedFolders: string[]): { title: string; description: string } | null {
  if (skippedFolders.length === 0) return null;
  const names = skippedFolders.map((name) => `“${name}”`).join(", ");
  return {
    title: skippedFolders.length === 1 ? "Folder not uploaded" : "Folders not uploaded",
    description: `Uploading folders isn't supported yet. Skipped ${names}; drop the files inside instead.`,
  };
}
