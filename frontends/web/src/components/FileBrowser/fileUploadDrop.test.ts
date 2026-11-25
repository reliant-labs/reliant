import { filesFromDrop, unreadableMessage, uploadGroups } from "./fileUploadDrop";

// Fake FileSystemEntry trees, as webkitGetAsEntry() hands them over.
type Entry = { name: string; isFile: boolean; isDirectory: boolean };

function fileEntry(file: File, fail = false): Entry {
  return {
    name: file.name, isFile: true, isDirectory: false,
    file: (ok: (f: File) => void, err: (e: unknown) => void) => (fail ? err(new Error("unreadable")) : ok(file)),
  } as Entry;
}

/** A folder whose reader answers in batches of `batch` (Chromium: at most 100), then an empty one. */
function dirEntry(name: string, children: Entry[], batch = 100): Entry & { reads: number } {
  const entry = {
    name, isFile: false, isDirectory: true, reads: 0,
    createReader: () => {
      let at = 0;
      return {
        readEntries: (ok: (entries: Entry[]) => void) => {
          entry.reads += 1;
          const next = children.slice(at, at + batch);
          at += next.length;
          // Asynchronous, as a real reader is.
          queueMicrotask(() => ok(next));
        },
      };
    },
  };
  return entry;
}

function drop(items: Array<{ file?: File; entry?: Entry }>): DataTransfer {
  return {
    files: items.flatMap(({ file }) => (file ? [file] : [])),
    items: items.map(({ file, entry }) => ({
      kind: "file",
      getAsFile: () => file ?? null,
      webkitGetAsEntry: () => entry ?? (file ? { name: file.name, isFile: true, isDirectory: false } : null),
    })),
  } as unknown as DataTransfer;
}

const fileOf = (name: string) => new File(["x"], name);

describe("filesFromDrop", () => {
  it("walks nested folders, giving each file its folder relative to the drop", async () => {
    const top = fileOf("top.txt");
    const a = fileOf("a.txt");
    const b = fileOf("b.txt");
    const tree = await filesFromDrop(drop([
      { file: top },
      { file: fileOf("src"), entry: dirEntry("src", [fileEntry(a), dirEntry("lib", [fileEntry(b)])]) },
    ]));
    expect(tree.files).toEqual([
      { file: top, relativeDir: "" },
      { file: a, relativeDir: "src" },
      { file: b, relativeDir: "src/lib" },
    ]);
    expect(tree.emptyFolders).toEqual([]);
    expect(tree.unreadable).toEqual([]);
  });

  it("reads a folder's entries until the reader answers an empty batch", async () => {
    const files = Array.from({ length: 250 }, (_, i) => fileOf(`f${i}.txt`));
    const folder = dirEntry("many", files.map((file) => fileEntry(file)), 100);
    const tree = await filesFromDrop(drop([{ entry: folder }]));
    expect(tree.files.map(({ file }) => file)).toEqual(files);
    // 100 + 100 + 50, then the empty batch that ends it.
    expect(folder.reads).toBe(4);
  });

  it("names empty folders (only the deepest of a chain) and never enters .git", async () => {
    const kept = fileOf("kept.txt");
    const tree = await filesFromDrop(drop([
      {
        entry: dirEntry("proj", [
          dirEntry(".git", [fileEntry(fileOf("HEAD"))]),
          dirEntry("empty", []),
          dirEntry("outer", [dirEntry("inner", [])]),
          fileEntry(kept),
        ]),
      },
    ]));
    expect(tree.files).toEqual([{ file: kept, relativeDir: "proj" }]);
    expect(tree.emptyFolders).toEqual(["proj/empty", "proj/outer/inner"]);
  });

  it("drops a dropped empty folder in as one empty folder", async () => {
    const tree = await filesFromDrop(drop([{ entry: dirEntry("blank", []) }]));
    expect(tree.files).toEqual([]);
    expect(tree.emptyFolders).toEqual(["blank"]);
  });

  it("lists what it could not read instead of failing the drop", async () => {
    const ok = fileOf("ok.txt");
    const tree = await filesFromDrop(drop([
      { entry: dirEntry("d", [fileEntry(fileOf("locked.bin"), true), fileEntry(ok)]) },
    ]));
    expect(tree.files).toEqual([{ file: ok, relativeDir: "d" }]);
    expect(tree.unreadable).toEqual(["d/locked.bin"]);
    expect(unreadableMessage(tree.unreadable)).toEqual({
      title: "An item wasn't uploaded",
      description: "Couldn't read “d/locked.bin” from this device.",
    });
  });

  it("takes the plain file list from a browser without entries", async () => {
    const file = fileOf("plain.txt");
    const tree = await filesFromDrop({ files: [file], items: [] } as unknown as DataTransfer);
    expect(tree.files).toEqual([{ file, relativeDir: "" }]);
  });
});

describe("uploadGroups", () => {
  it("groups the files by destination under the drop folder", () => {
    const [a, b, c] = [fileOf("a"), fileOf("b"), fileOf("c")];
    expect(uploadGroups("docs", [
      { file: a, relativeDir: "" },
      { file: b, relativeDir: "src" },
      { file: c, relativeDir: "src" },
    ])).toEqual([
      { directory: "docs", files: [a] },
      { directory: "docs/src", files: [b, c] },
    ]);
    expect(uploadGroups("", [{ file: b, relativeDir: "src" }])).toEqual([{ directory: "src", files: [b] }]);
  });
});

describe("unreadableMessage", () => {
  it("is null when everything was read, and shortens a long list", () => {
    expect(unreadableMessage([])).toBeNull();
    expect(unreadableMessage(["a", "b", "c", "d", "e"])?.description).toBe(
      "Couldn't read “a”, “b”, “c” and 2 more from this device.",
    );
  });
});
