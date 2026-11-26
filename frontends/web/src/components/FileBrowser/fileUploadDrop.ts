/**
 * Reading OS files out of a drag-and-drop onto the file tree.
 *
 * A drag carries files only when its `types` include "Files"; anything else
 * (text, a link, an in-app drag) is left to whoever handles it today. A
 * dropped folder arrives as a File too, with no way to read its contents
 * through `files`, so each item is read as an entry (`webkitGetAsEntry()`)
 * and a folder entry is walked: every file inside comes back with the folder
 * it sits in, relative to the drop, and every empty folder by name (the
 * machine creates a file's missing parent folders when it lands, but an empty
 * folder has no file to bring it along). `.git` folders are left out.
 *
 * A browser without entries gives the plain `files` list.
 */

/** Whether a drag carries files from the operating system. */
export function dragHasFiles(dataTransfer: DataTransfer | null): boolean {
  if (!dataTransfer) return false;
  return Array.from(dataTransfer.types ?? []).includes("Files");
}

/** One dropped file, and the folder it sits in relative to the drop ("" for the top). */
export interface DroppedFile {
  file: File;
  relativeDir: string;
}

export interface DroppedTree {
  files: DroppedFile[];
  /** Folders with nothing to upload inside, relative to the drop. */
  emptyFolders: string[];
  /** Names of what could not be read (a broken link, no permission), which are not uploaded. */
  unreadable: string[];
}

/** The folders a walk never enters. */
const SKIPPED_FOLDERS: ReadonlySet<string> = new Set([".git"]);

// The parts of FileSystemEntry the walk reads, so tests can build fakes.
interface EntryLike {
  readonly name: string;
  readonly isFile: boolean;
  readonly isDirectory: boolean;
}
interface FileEntryLike extends EntryLike {
  file(success: (file: File) => void, failure?: (err: unknown) => void): void;
}
interface DirectoryReaderLike {
  readEntries(success: (entries: EntryLike[]) => void, failure?: (err: unknown) => void): void;
}
interface DirectoryEntryLike extends EntryLike {
  createReader(): DirectoryReaderLike;
}

function readFile(entry: FileEntryLike): Promise<File> {
  return new Promise((resolve, reject) => entry.file(resolve, reject));
}

/** Every entry of a folder: readEntries answers in batches (at most 100 in Chromium) until an empty one. */
async function readAllEntries(entry: DirectoryEntryLike): Promise<EntryLike[]> {
  const reader = entry.createReader();
  const all: EntryLike[] = [];
  for (;;) {
    const batch = await new Promise<EntryLike[]>((resolve, reject) => reader.readEntries(resolve, reject));
    if (batch.length === 0) return all;
    all.push(...batch);
  }
}

function joinDir(parent: string, name: string): string {
  return parent ? `${parent}/${name}` : name;
}

async function walk(entry: EntryLike, relativeDir: string, tree: DroppedTree): Promise<void> {
  if (entry.isFile) {
    try {
      tree.files.push({ file: await readFile(entry as FileEntryLike), relativeDir });
    } catch (err) {
      console.debug("[upload] could not read a dropped file", entry.name, err);
      tree.unreadable.push(joinDir(relativeDir, entry.name));
    }
    return;
  }
  if (!entry.isDirectory || SKIPPED_FOLDERS.has(entry.name)) return;
  const dir = joinDir(relativeDir, entry.name);
  let children: EntryLike[];
  try {
    children = await readAllEntries(entry as DirectoryEntryLike);
  } catch (err) {
    console.debug("[upload] could not read a dropped folder", dir, err);
    tree.unreadable.push(dir);
    return;
  }
  const before = tree.files.length;
  const foldersBefore = tree.emptyFolders.length;
  for (const child of children) await walk(child, dir, tree);
  // Empty when nothing below it is uploaded and no empty folder below it
  // already brings it along (creating a/b creates a).
  if (tree.files.length === before && tree.emptyFolders.length === foldersBefore) {
    tree.emptyFolders.push(dir);
  }
}

/**
 * Everything a drop carries, folders walked. The entries are taken from the
 * drop before the first `await`: a DataTransfer's items are emptied when the
 * drop event's handler returns.
 */
export async function filesFromDrop(dataTransfer: DataTransfer): Promise<DroppedTree> {
  const items = dataTransfer.items ? Array.from(dataTransfer.items) : [];
  const tree: DroppedTree = { files: [], emptyFolders: [], unreadable: [] };
  if (items.length === 0) {
    tree.files = Array.from(dataTransfer.files ?? []).map((file) => ({ file, relativeDir: "" }));
    return tree;
  }
  const roots: (EntryLike | File)[] = [];
  for (const item of items) {
    if (item.kind !== "file") continue;
    const entry = typeof item.webkitGetAsEntry === "function" ? (item.webkitGetAsEntry() as EntryLike | null) : null;
    if (entry?.isDirectory) {
      roots.push(entry);
      continue;
    }
    // A top-level file is read now, while the drop still holds it.
    const file = item.getAsFile();
    if (file) roots.push(file);
  }
  for (const root of roots) {
    if (root instanceof File) tree.files.push({ file: root, relativeDir: "" });
    else await walk(root, "", tree);
  }
  return tree;
}

/** The dropped files grouped by destination folder under `dropDir` ("" is the workspace root). */
export function uploadGroups(dropDir: string, files: DroppedFile[]): { directory: string; files: File[] }[] {
  const groups = new Map<string, File[]>();
  for (const { file, relativeDir } of files) {
    const directory = relativeDir ? joinDir(dropDir, relativeDir) : dropDir;
    const group = groups.get(directory);
    if (group) group.push(file);
    else groups.set(directory, [file]);
  }
  return Array.from(groups, ([directory, groupFiles]) => ({ directory, files: groupFiles }));
}

/** The message for what a drop could not read, or null when it read everything. */
export function unreadableMessage(unreadable: string[]): { title: string; description: string } | null {
  if (unreadable.length === 0) return null;
  const shown = unreadable.slice(0, 3).map((name) => `“${name}”`).join(", ");
  const more = unreadable.length > 3 ? ` and ${unreadable.length - 3} more` : "";
  return {
    title: unreadable.length === 1 ? "An item wasn't uploaded" : "Some items weren't uploaded",
    description: `Couldn't read ${shown}${more} from this device.`,
  };
}
