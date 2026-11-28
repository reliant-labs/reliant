// Copyright (c) 2025 Reliant Labs

/**
 * How long a saved blob's object URL stays alive. Revoking it in the same
 * turn as the click can cancel a download the browser has not started
 * reading yet (Electron shows its native Save dialog first), so it is
 * released after the download has certainly begun.
 */
export const SAVE_BLOB_URL_TTL_MS = 60_000;

/**
 * Hands a blob to the browser as a download named `filename`.
 *
 * An `a[download]` click on an object URL: browsers save it to the downloads
 * folder (or ask, per the user's setting), and Electron, which registers no
 * `will-download` handler, shows the native Save dialog.
 */
export function saveBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.rel = "noopener";
  anchor.style.display = "none";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(url), SAVE_BLOB_URL_TTL_MS);
}
