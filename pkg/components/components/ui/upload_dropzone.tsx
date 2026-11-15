import React from "react";

/**
 * UploadDropzone — drop files (or click to browse), validate them, and PUT
 * each one straight to object storage through a presigned URL, with a
 * progress bar per file. The browser uploads; your API only signs.
 *
 *   <UploadDropzone
 *     accept="image/*,.pdf"
 *     maxBytes={10 * 1024 * 1024}
 *     getUploadUrl={async (file) => {
 *       const res = await api.createUpload({ name: file.name, type: file.type, size: file.size });
 *       return { url: res.url, headers: res.headers, key: res.key };
 *     }}
 *     onUploaded={(file, target) => attach.mutate({ key: target.key })}
 *   />
 *
 * THE CONTRACT. For each accepted file the component calls
 * `getUploadUrl(file)`, then sends `PUT <url>` with the file as the body and
 * `headers` as request headers, through XMLHttpRequest because fetch()
 * reports no upload progress. Any 2xx is success. Content-Type defaults to
 * the file's type unless `headers` sets it — when the URL was signed with a
 * content type, return that exact header so the signature matches. Whatever
 * else `getUploadUrl` returns (an object key, an id) comes back unchanged in
 * `onUploaded`, so the caller can record the upload without guessing.
 *
 * The bucket must allow PUT from the page's origin (CORS), or every upload
 * fails with a network error.
 *
 * `accept` takes the same list as <input accept>: extensions (".pdf"),
 * MIME types ("image/png") and wildcards ("image/*"). It and `maxBytes` are
 * checked before anything is signed; a rejected file stays in the list with
 * the reason until dismissed.
 */
export interface UploadTarget {
  /** The presigned URL to PUT the file to. */
  url: string;
  /** Request headers the signature requires (Content-Type, x-amz-*, ...). */
  headers?: Record<string, string>;
}

export type UploadStatus = "queued" | "uploading" | "done" | "error" | "rejected";

export interface UploadEntry {
  id: string;
  file: File;
  status: UploadStatus;
  /** Bytes sent so far. */
  loaded: number;
  /** Why it was rejected or failed. */
  error?: string;
}

export interface UploadDropzoneProps<T extends UploadTarget = UploadTarget> {
  /** Sign one upload. Called once per accepted file, right before its PUT. */
  getUploadUrl: (file: File) => Promise<T>;
  /** One file finished (2xx). `target` is what getUploadUrl returned for it. */
  onUploaded?: (file: File, target: T) => void;
  /** One file failed: signing threw, the PUT returned non-2xx, or the network dropped. */
  onError?: (file: File, error: Error) => void;
  /** Every change to the list, e.g. to disable a submit button while uploads run. */
  onEntriesChange?: (entries: UploadEntry[]) => void;
  /** Same syntax as <input accept>. */
  accept?: string;
  /** Largest file accepted, in bytes. */
  maxBytes?: number;
  /** Allow several files. Defaults to true. */
  multiple?: boolean;
  /** Uploads in flight at once. Defaults to 3. */
  maxConcurrent?: number;
  disabled?: boolean;
  /** The drop target's main line. Defaults to "Drop files here or click to browse". */
  label?: React.ReactNode;
  /** The line under it. Defaults to the size limit. */
  hint?: React.ReactNode;
  className?: string;
}

const units = ["B", "KB", "MB", "GB", "TB"];

function formatBytes(bytes: number): string {
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  const digits = unit === 0 || value >= 10 ? 0 : 1;
  return `${value.toFixed(digits)} ${units[unit] ?? "B"}`;
}

function matchesAccept(file: File, accept: string): boolean {
  const name = file.name.toLowerCase();
  const type = file.type.toLowerCase();
  return accept
    .split(",")
    .map((s) => s.trim().toLowerCase())
    .filter(Boolean)
    .some((rule) => {
      if (rule.startsWith(".")) return name.endsWith(rule);
      if (rule.endsWith("/*")) return type.startsWith(rule.slice(0, -1));
      return type === rule;
    });
}

function hasHeader(headers: Record<string, string>, name: string): boolean {
  return Object.keys(headers).some((k) => k.toLowerCase() === name);
}

class UploadAborted extends Error {}

export default function UploadDropzone<T extends UploadTarget = UploadTarget>({
  getUploadUrl,
  onUploaded,
  onError,
  onEntriesChange,
  accept,
  maxBytes,
  multiple = true,
  maxConcurrent = 3,
  disabled,
  label = "Drop files here or click to browse",
  hint,
  className,
}: UploadDropzoneProps<T>) {
  const [entries, setEntries] = React.useState<UploadEntry[]>([]);
  const [dragging, setDragging] = React.useState(false);
  const [announcement, setAnnouncement] = React.useState("");

  // The list is also kept in a ref: uploads finish asynchronously and must
  // read and write the latest list, not the one their closure captured.
  const entriesRef = React.useRef<UploadEntry[]>([]);
  const requests = React.useRef(new Map<string, XMLHttpRequest>());
  const nextId = React.useRef(0);
  const dragDepth = React.useRef(0);
  const mounted = React.useRef(true);
  const inputRef = React.useRef<HTMLInputElement>(null);
  const hintId = React.useId();

  const latest = React.useRef({
    getUploadUrl,
    onUploaded,
    onError,
    onEntriesChange,
    maxConcurrent,
  });
  React.useEffect(() => {
    latest.current = { getUploadUrl, onUploaded, onError, onEntriesChange, maxConcurrent };
  });

  React.useEffect(() => {
    mounted.current = true;
    const inflight = requests.current;
    return () => {
      mounted.current = false;
      for (const xhr of inflight.values()) xhr.abort();
      inflight.clear();
    };
  }, []);

  function publish(next: UploadEntry[]) {
    entriesRef.current = next;
    setEntries(next);
    latest.current.onEntriesChange?.(next);
  }

  function update(id: string, patch: Partial<UploadEntry>) {
    publish(entriesRef.current.map((e) => (e.id === id ? { ...e, ...patch } : e)));
  }

  const exists = (id: string) => entriesRef.current.some((e) => e.id === id);

  function put(entry: UploadEntry, target: T): Promise<void> {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      requests.current.set(entry.id, xhr);
      xhr.open("PUT", target.url);
      const headers = { ...target.headers };
      if (!hasHeader(headers, "content-type") && entry.file.type) {
        headers["Content-Type"] = entry.file.type;
      }
      for (const [name, value] of Object.entries(headers)) xhr.setRequestHeader(name, value);
      xhr.upload.onprogress = (e) => {
        if (exists(entry.id)) update(entry.id, { loaded: e.loaded });
      };
      xhr.onload = () => {
        requests.current.delete(entry.id);
        if (xhr.status >= 200 && xhr.status < 300) resolve();
        else reject(new Error(`Upload failed (HTTP ${xhr.status})`));
      };
      xhr.onerror = () => {
        requests.current.delete(entry.id);
        reject(new Error("Network error — check the bucket's CORS rules"));
      };
      xhr.onabort = () => {
        requests.current.delete(entry.id);
        reject(new UploadAborted("Cancelled"));
      };
      xhr.send(entry.file);
    });
  }

  async function start(entry: UploadEntry) {
    update(entry.id, { status: "uploading", loaded: 0, error: undefined });
    try {
      const target = await latest.current.getUploadUrl(entry.file);
      if (!mounted.current || !exists(entry.id)) return;
      await put(entry, target);
      if (!mounted.current || !exists(entry.id)) return;
      update(entry.id, { status: "done", loaded: entry.file.size });
      setAnnouncement(`${entry.file.name} uploaded`);
      latest.current.onUploaded?.(entry.file, target);
    } catch (err) {
      if (!mounted.current || !exists(entry.id) || err instanceof UploadAborted) return;
      const error = err instanceof Error ? err : new Error(String(err));
      update(entry.id, { status: "error", error: error.message });
      setAnnouncement(`${entry.file.name} failed: ${error.message}`);
      latest.current.onError?.(entry.file, error);
    } finally {
      if (mounted.current) pump();
    }
  }

  /** Start queued uploads while there is a free slot. */
  function pump() {
    let free =
      Math.max(1, latest.current.maxConcurrent) -
      entriesRef.current.filter((e) => e.status === "uploading").length;
    for (const entry of entriesRef.current) {
      if (free <= 0) break;
      if (entry.status !== "queued") continue;
      free--;
      void start(entry);
    }
  }

  function addFiles(list: FileList | File[]) {
    const files = Array.from(list).slice(0, multiple ? undefined : 1);
    if (files.length === 0) return;
    const added = files.map((file): UploadEntry => {
      const id = `upload-${nextId.current++}`;
      if (accept && !matchesAccept(file, accept)) {
        return { id, file, status: "rejected", loaded: 0, error: "This file type isn't accepted" };
      }
      if (maxBytes !== undefined && file.size > maxBytes) {
        return {
          id,
          file,
          status: "rejected",
          loaded: 0,
          error: `${formatBytes(file.size)} is over the ${formatBytes(maxBytes)} limit`,
        };
      }
      return { id, file, status: "queued", loaded: 0 };
    });
    const rejected = added.filter((e) => e.status === "rejected");
    if (rejected.length > 0) {
      setAnnouncement(rejected.map((e) => `${e.file.name}: ${e.error ?? "rejected"}`).join(". "));
    }
    // A single-file zone replaces what it holds rather than growing a list.
    if (!multiple) {
      for (const xhr of requests.current.values()) xhr.abort();
      publish(added);
    } else {
      publish([...entriesRef.current, ...added]);
    }
    pump();
  }

  function remove(id: string) {
    requests.current.get(id)?.abort();
    publish(entriesRef.current.filter((e) => e.id !== id));
  }

  function retry(id: string) {
    update(id, { status: "queued", loaded: 0, error: undefined });
    pump();
  }

  const defaultHint = [
    accept
      ? accept
          .split(",")
          .map((s) => s.trim())
          .join(", ")
      : null,
    maxBytes !== undefined ? `up to ${formatBytes(maxBytes)}` : null,
  ]
    .filter(Boolean)
    .join(", ");
  const shownHint =
    hint ?? (defaultHint ? defaultHint.charAt(0).toUpperCase() + defaultHint.slice(1) : null);

  return (
    <div className={className}>
      <button
        type="button"
        disabled={disabled}
        aria-describedby={shownHint ? hintId : undefined}
        onClick={() => inputRef.current?.click()}
        onDragEnter={(e) => {
          if (disabled || !e.dataTransfer.types.includes("Files")) return;
          e.preventDefault();
          dragDepth.current++;
          setDragging(true);
        }}
        onDragOver={(e) => {
          if (disabled || !e.dataTransfer.types.includes("Files")) return;
          e.preventDefault();
          e.dataTransfer.dropEffect = "copy";
        }}
        onDragLeave={() => {
          dragDepth.current = Math.max(0, dragDepth.current - 1);
          if (dragDepth.current === 0) setDragging(false);
        }}
        onDrop={(e) => {
          e.preventDefault();
          dragDepth.current = 0;
          setDragging(false);
          if (!disabled) addFiles(e.dataTransfer.files);
        }}
        className={`flex w-full flex-col items-center justify-center gap-1.5 rounded-lg border border-dashed px-6 py-8 text-center transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-60 ${
          dragging
            ? "border-accent bg-accent-surface"
            : "border-border-strong bg-surface hover:border-ink-subtle hover:bg-ink/5"
        }`}
      >
        <svg
          aria-hidden="true"
          viewBox="0 0 24 24"
          className={`h-6 w-6 ${dragging ? "text-accent" : "text-ink-subtle"}`}
          fill="none"
          stroke="currentColor"
          strokeWidth={1.75}
          strokeLinecap="round"
          strokeLinejoin="round"
        >
          <path d="M12 16V4m0 0-4 4m4-4 4 4M4 16v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2" />
        </svg>
        <span className="text-sm font-medium text-ink">{label}</span>
        {shownHint && (
          <span id={hintId} className="text-xs text-ink-muted">
            {shownHint}
          </span>
        )}
      </button>
      <input
        ref={inputRef}
        type="file"
        hidden
        aria-label="Choose files"
        accept={accept}
        multiple={multiple}
        disabled={disabled}
        onChange={(e) => {
          if (e.target.files) addFiles(e.target.files);
          // Clear it, so choosing the same file again still fires onChange.
          e.target.value = "";
        }}
      />

      {entries.length > 0 && (
        <ul
          aria-label="Uploads"
          className="mt-3 divide-y divide-border rounded-lg border border-border bg-surface"
        >
          {entries.map((entry) => {
            const size = entry.file.size;
            const pct =
              entry.status === "done"
                ? 100
                : size > 0
                  ? Math.min(100, Math.round((entry.loaded / size) * 100))
                  : 0;
            const failed = entry.status === "error" || entry.status === "rejected";
            const statusText =
              entry.status === "queued"
                ? "Waiting"
                : entry.status === "uploading"
                  ? `${pct}%`
                  : entry.status === "done"
                    ? "Uploaded"
                    : (entry.error ?? "Failed");
            return (
              <li key={entry.id} className="flex items-center gap-3 px-3 py-2.5">
                <div className="min-w-0 flex-1">
                  <div className="flex items-baseline justify-between gap-3 text-sm">
                    <span className="truncate text-ink" title={entry.file.name}>
                      {entry.file.name}
                    </span>
                    <span className="shrink-0 text-xs text-ink-muted tabular-nums">
                      {formatBytes(size)}
                    </span>
                  </div>
                  {(entry.status === "uploading" || entry.status === "queued") && (
                    <div
                      role="progressbar"
                      aria-label={`Uploading ${entry.file.name}`}
                      aria-valuemin={0}
                      aria-valuemax={100}
                      aria-valuenow={pct}
                      className="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-surface-sunken"
                    >
                      <div
                        className="h-full rounded-full bg-accent transition-[width] duration-150 motion-reduce:transition-none"
                        style={{ width: `${pct}%` }}
                      />
                    </div>
                  )}
                  <p
                    className={`mt-1 text-xs ${
                      failed
                        ? "text-danger-ink"
                        : entry.status === "done"
                          ? "text-success-ink"
                          : "text-ink-muted"
                    }`}
                  >
                    {statusText}
                  </p>
                </div>
                {entry.status === "error" && (
                  <button
                    type="button"
                    onClick={() => retry(entry.id)}
                    className="shrink-0 rounded px-2 py-1 text-xs font-medium text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
                  >
                    Retry
                  </button>
                )}
                <button
                  type="button"
                  aria-label={
                    entry.status === "uploading" || entry.status === "queued"
                      ? `Cancel ${entry.file.name}`
                      : `Remove ${entry.file.name}`
                  }
                  onClick={() => remove(entry.id)}
                  className="shrink-0 rounded p-1 text-ink-subtle hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
                >
                  <svg
                    aria-hidden="true"
                    viewBox="0 0 20 20"
                    className="h-4 w-4"
                    fill="currentColor"
                  >
                    <path d="M6.28 5.22a.75.75 0 0 0-1.06 1.06L8.94 10l-3.72 3.72a.75.75 0 1 0 1.06 1.06L10 11.06l3.72 3.72a.75.75 0 1 0 1.06-1.06L11.06 10l3.72-3.72a.75.75 0 0 0-1.06-1.06L10 8.94 6.28 5.22Z" />
                  </svg>
                </button>
              </li>
            );
          })}
        </ul>
      )}
      <span className="sr-only" aria-live="polite">
        {announcement}
      </span>
    </div>
  );
}
