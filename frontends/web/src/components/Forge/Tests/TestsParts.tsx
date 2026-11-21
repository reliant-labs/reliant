// Copyright (c) 2026 Reliant Labs

/**
 * The pieces the Runs and Flaky tests pages share: a section card, a test's
 * name and package, a failure's output, a link that leaves the console, and
 * the filter bar's free-text control (a branch, a commit, a release).
 */

import { useState, type ReactNode } from "react";
import { ExternalLink as ExternalIcon } from "lucide-react";

import Card from "@/components/forge-ui/card";
import { filterChipClass } from "@/components/forge-ui/filter_bar";
import type { TestKey } from "@/services/controlPlane/tests/types";
import { cn } from "@/lib/utils";

/** A titled panel of a page: heading and an optional note on one line, then the body. */
export function Section({ title, note, testId, children, flush }: { title: string; note?: ReactNode; testId: string; children: ReactNode; flush?: boolean }) {
  return (
    <Card padding="none" className="overflow-hidden" data-testid={testId}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 px-4 pb-2 pt-3">
        <h2 className="text-section text-ink">{title}</h2>
        {note && <p className="text-meta text-ink-muted">{note}</p>}
      </div>
      <div className={flush ? undefined : "px-4 pb-4"}>{children}</div>
    </Card>
  );
}

/** The module part of a Go import path is noise beside its test: "…/internal/billing". */
export function shortPackage(pkg: string): string {
  const internal = pkg.search(/\/(internal|pkg|cmd)\//);
  return internal > 0 ? `…${pkg.slice(internal)}` : pkg;
}

/** A test's name, and its package under it. */
export function TestName({ test, className }: { test: TestKey; className?: string }) {
  return (
    <span className={cn("block min-w-0", className)}>
      <span className="block truncate font-mono text-code text-ink" title={test.name}>
        {test.name || test.id}
      </span>
      {test.package && (
        <span className="block truncate text-meta text-ink-muted" title={test.package}>
          {shortPackage(test.package)}
        </span>
      )}
    </span>
  );
}

/**
 * A failure's output: monospace, collapsed unless asked open. The server has
 * already scrubbed known secret shapes and truncated it to 8 KiB.
 */
export function FailureOutput({ output, open, label = "Output", testId }: { output: string; open?: boolean; label?: string; testId?: string }) {
  if (!output) return <p className="text-meta text-ink-muted">No output was recorded.</p>;
  return (
    <details open={open} className="group rounded-md border border-border/60 bg-surface-sunken" data-testid={testId}>
      <summary className="cursor-pointer select-none rounded-md px-3 py-1.5 text-meta font-medium text-ink-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
        {label} <span className="font-normal">· {output.split("\n").length} lines</span>
      </summary>
      <pre className="max-h-96 overflow-auto whitespace-pre px-3 pb-3 pt-1 font-mono text-code text-ink">{output}</pre>
    </details>
  );
}

/**
 * A recorded URL that is safe to open: an absolute https URL, or null. CI run
 * and issue URLs arrive in uploaded run metadata, which anyone holding the
 * project's upload token writes, so a `javascript:`, `data:` or plain-http
 * value is never made a link. Any https host is allowed: CI and issue
 * trackers are self-hosted as often as not.
 */
export function safeOutUrl(href: string): URL | null {
  try {
    const url = new URL(href);
    return url.protocol === "https:" && !url.username && !url.password ? url : null;
  } catch {
    return null;
  }
}

/**
 * A link out of the console (CI, an issue): a new tab, marked as leaving, its
 * destination host in its title. A URL safeOutUrl refuses is shown as text.
 */
export function OutLink({ href, children, testId, className }: { href: string; children: ReactNode; testId?: string; className?: string }) {
  const url = safeOutUrl(href);
  if (!url) {
    return (
      <span className={cn("whitespace-nowrap text-ink-muted", className)} title={`Not a link: ${href}`} data-testid={testId} data-unsafe-url="">
        {children}
      </span>
    );
  }
  return (
    <a
      href={url.href}
      title={`Opens ${url.host}`}
      target="_blank"
      rel="noopener noreferrer"
      className={cn("inline-flex items-center gap-1 whitespace-nowrap text-primary-ink underline-offset-2 hover:underline", className)}
      data-testid={testId}
    >
      {children}
      <ExternalIcon className="h-3 w-3" aria-hidden="true" />
      <span className="sr-only">(opens in a new tab)</span>
    </a>
  );
}

/**
 * A free-text filter in a FilterBar: a chip ("Commit") that opens a field,
 * set on Enter or when the field loses focus, cleared with its ×. A branch, a
 * commit or a release is typed or pasted, never picked from a list the page
 * could only partly know.
 */
export function TextFilter({
  label,
  value,
  placeholder,
  onChange,
  testId,
  mono,
}: {
  label: string;
  value?: string;
  placeholder: string;
  onChange: (value: string | undefined) => void;
  testId: string;
  mono?: boolean;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const commit = () => {
    setEditing(false);
    const next = draft.trim();
    if (next !== (value ?? "")) onChange(next || undefined);
  };
  if (editing) {
    return (
      <label className="flex h-8 items-center gap-1.5 rounded-md border border-accent/50 bg-surface px-2.5 text-sm text-ink focus-within:ring-2 focus-within:ring-ring">
        <span className="opacity-80">{label}:</span>
        <input
          autoFocus
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          onBlur={commit}
          onKeyDown={(event) => {
            if (event.key === "Enter") commit();
            if (event.key === "Escape") setEditing(false);
          }}
          placeholder={placeholder}
          aria-label={`${label} filter`}
          className={cn("w-40 bg-transparent text-sm text-ink placeholder:text-ink-muted focus:outline-none", mono && "font-mono")}
          data-testid={`${testId}-input`}
        />
      </label>
    );
  }
  return (
    <div className="relative inline-flex">
      <button
        type="button"
        onClick={() => {
          setDraft(value ?? "");
          setEditing(true);
        }}
        className={`${filterChipClass(!!value)} ${value ? "rounded-r-none border-r-0 pr-2" : ""}`}
        data-testid={testId}
      >
        {value ? (
          <>
            <span className="font-normal opacity-80">{label}:</span> <span className={cn("max-w-[12rem] truncate", mono && "font-mono")}>{value}</span>
          </>
        ) : (
          label
        )}
      </button>
      {value && (
        <button
          type="button"
          onClick={() => onChange(undefined)}
          aria-label={`Clear ${label.toLowerCase()} filter`}
          className="inline-flex h-8 w-7 items-center justify-center rounded-r-md border border-l-0 border-accent/50 bg-accent/10 text-accent-ink hover:bg-accent/20 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid={`${testId}-clear`}
        >
          <svg viewBox="0 0 12 12" className="h-3 w-3" aria-hidden="true">
            <path d="M3.5 3.5 8.5 8.5M8.5 3.5 3.5 8.5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
          </svg>
        </button>
      )}
    </div>
  );
}
