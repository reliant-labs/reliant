// Copyright (c) 2025 Reliant Labs

/**
 * What the storage browser shows of one object: its size in words, which
 * preview it gets, and a text or JSON preview's text.
 *
 * The server judges the preview (GetObjectMetadata.preview) from the stored
 * content type and size; the client only refuses to widen it. An image is
 * drawn only for the four raster types the server presigns inline, never an
 * SVG, whatever a newer or misconfigured server claims: an SVG can carry
 * script, and the object came from the app's users.
 */

import type { PreviewKindName, StorageObjectMeta } from "@/services/controlPlane/storageBrowser/storageBrowserApi";

/** The text and JSON preview cap: PreviewObject returns at most this many bytes. */
export const TEXT_PREVIEW_CAP = 1024 * 1024;

const UNITS = ["KiB", "MiB", "GiB", "TiB", "PiB"] as const;

/**
 * A size for a person, in the binary units the server's caps are written in
 * (1 MiB, 10 MiB): `0 B`, `912 B`, `1.5 KiB`, `10 MiB`, `118 MiB`.
 */
export function formatSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  if (bytes < 1024) return `${Math.round(bytes)} B`;
  let value = bytes;
  let unit = -1;
  while (value >= 1024 && unit < UNITS.length - 1) {
    value /= 1024;
    unit++;
  }
  const shown = value < 100 ? value.toFixed(1).replace(/\.0$/, "") : Math.round(value).toString();
  return `${shown} ${UNITS[unit]}`;
}

/** The exact size, for a title or the object pane: `1,536 bytes`. */
export function exactBytes(bytes: number): string {
  return `${bytes.toLocaleString("en-US")} ${bytes === 1 ? "byte" : "bytes"}`;
}

/** The image types an inline preview may draw (the server's list; never SVG). */
const SAFE_IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);

/** The media type alone, lowercased: `image/PNG; charset=x` → `image/png`. */
export function mediaType(contentType: string): string {
  return contentType.split(";")[0]!.trim().toLowerCase();
}

/** Which preview an object gets: the server's judgement, narrowed to what is safe to draw. */
export function previewKindOf(meta: Pick<StorageObjectMeta, "preview" | "contentType">): PreviewKindName {
  if (meta.preview === "image") return SAFE_IMAGE_TYPES.has(mediaType(meta.contentType)) ? "image" : "none";
  return meta.preview;
}

export interface PreviewText {
  /** What to show, as text (never HTML). */
  text: string;
  /** JSON, re-indented. */
  pretty: boolean;
  /** The server sent less than the whole object: it was cut at the cap. */
  cut: boolean;
  /** Asked for as JSON, but the bytes did not parse (or were cut), so shown as they are. */
  unparsed: boolean;
}

/**
 * A text or JSON preview's bytes as text: UTF-8 with invalid sequences
 * replaced (the proto's promise), JSON pretty-printed when it parses. A cut
 * JSON document cannot parse, so it is shown as sent, with the cut said.
 */
export function previewText(content: Uint8Array, kind: "text" | "json", size: number): PreviewText {
  const text = new TextDecoder("utf-8").decode(content);
  const cut = content.byteLength < size;
  if (kind !== "json") return { text, pretty: false, cut, unparsed: false };
  if (cut) return { text, pretty: false, cut, unparsed: true };
  try {
    return { text: JSON.stringify(JSON.parse(text), null, 2), pretty: true, cut, unparsed: false };
  } catch {
    return { text, pretty: false, cut, unparsed: true };
  }
}
