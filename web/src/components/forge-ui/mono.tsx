"use client";

import { useEffect, useRef, useState } from "react";

export interface MonoProps {
  /** The full value. It is what `title` shows and what the copy button copies. */
  value: string;
  /**
   * Characters to show before the ellipsis. Defaults by shape: a digest
   * (`sha256:…`) keeps 19 (the algorithm plus 12 hex), a bare hex sha keeps 8,
   * anything else is shown whole and left to CSS to truncate in a narrow cell.
   */
  length?: number;
  /** Render the copy button (default true). */
  copy?: boolean;
  className?: string;
}

const DIGEST = /^[a-z0-9]+:[0-9a-f]{16,}$/i;
const HEX_SHA = /^[0-9a-f]{12,}$/i;

/** How many characters `value` shows by default — see `MonoProps.length`. */
export function monoLength(value: string): number | undefined {
  if (DIGEST.test(value)) return 19;
  if (HEX_SHA.test(value)) return 8;
  return undefined;
}

/**
 * Mono — a machine identifier: a commit sha, an image digest, a pod name, a
 * request id. Monospace is for exactly these; versions, environment names,
 * workload names and cluster names are words and stay in the body face.
 *
 *   <Mono value="9f3c2a17e4b0c6d8a1f2e3d4c5b6a7980f1e2d3c" />   → 9f3c2a17
 *   <Mono value="sha256:4be1c0a9d2f7e3b6…" />                      → sha256:4be1c0a9d2f7
 *   <Mono value="api-7d9f8c6b5-x2x9q" copy={false} />
 *
 * Always truncated, always recoverable: the full value is in `title` and one
 * click away on the copy button, so shortening it never costs the reader the
 * thing they came to grab.
 */
export default function Mono({ value, length, copy = true, className }: MonoProps) {
  const keep = length ?? monoLength(value);
  const shown = keep !== undefined && value.length > keep ? `${value.slice(0, keep)}…` : value;
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      return; // No clipboard (insecure context, denied): leave the icon as it was.
    }
    setCopied(true);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 1500);
  }

  return (
    <span
      className={`group inline-flex min-w-0 max-w-full items-center gap-0.5 align-middle ${className ?? ""}`}
    >
      <code title={value} className="min-w-0 truncate font-mono text-xs text-ink-muted">
        {shown}
      </code>
      {copy && (
        <button
          type="button"
          onClick={handleCopy}
          aria-label={copied ? `Copied ${value}` : `Copy ${value}`}
          title={copied ? "Copied" : "Copy"}
          className="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded text-ink-muted opacity-0 transition-opacity hover:bg-ink/5 hover:text-ink focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent group-hover:opacity-100 [@media(hover:none)]:opacity-100"
        >
          {copied ? (
            <svg
              className="h-3 w-3"
              fill="none"
              viewBox="0 0 24 24"
              strokeWidth={2.5}
              stroke="currentColor"
              aria-hidden="true"
            >
              <path strokeLinecap="round" strokeLinejoin="round" d="M4.5 12.75l6 6 9-13.5" />
            </svg>
          ) : (
            <svg
              className="h-3 w-3"
              fill="none"
              viewBox="0 0 24 24"
              strokeWidth={2}
              stroke="currentColor"
              aria-hidden="true"
            >
              <rect x="8.25" y="8.25" width="12" height="12" rx="2" />
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                d="M15.75 8.25V5.25a1.5 1.5 0 00-1.5-1.5h-9a1.5 1.5 0 00-1.5 1.5v9a1.5 1.5 0 001.5 1.5h3"
              />
            </svg>
          )}
        </button>
      )}
    </span>
  );
}
