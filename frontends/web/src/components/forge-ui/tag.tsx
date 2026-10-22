import React from "react";

export type TagTone = "neutral" | "warning" | "danger";

export interface TagProps {
  /** `neutral` for nearly everything; `warning` / `danger` only for a label that is itself a problem ("rolled back"). */
  tone?: TagTone;
  /** A 12px icon before the text (a lock on a read-only dashboard). */
  icon?: React.ReactNode;
  /** Full text on hover, for a tag whose text is shortened. */
  title?: string;
  className?: string;
  children: React.ReactNode;
}

const toneStyles: Record<TagTone, string> = {
  neutral: "border-border text-ink-muted",
  warning: "border-warning-ink/40 text-warning-ink",
  danger: "border-danger-ink/35 text-danger-ink",
};

/**
 * Tag — a bordered, unfilled label for a KIND or a SCOPE: an environment
 * (`prod`), a workload kind (`job`), a source (`workspace`), a scope
 * (`scoped: frontends`).
 *
 *   <Tag>prod</Tag>
 *   <Tag tone="danger">rolled back</Tag>
 *
 * No fill, on purpose. A row of filled pills competes with the data it
 * annotates; an outline reads as metadata. A STATE is not a tag — use
 * <Status> (dot + word). A filled pill is <Badge>, kept for the one thing per
 * screen that must shout.
 */
export default function Tag({ tone = "neutral", icon, title, className, children }: TagProps) {
  return (
    <span
      title={title}
      className={`inline-flex h-5 items-center gap-1 whitespace-nowrap rounded-md border px-1.5 align-middle text-xs leading-none ${
        toneStyles[tone]
      } ${className ?? ""}`}
    >
      {icon && (
        <span aria-hidden="true" className="inline-flex h-3 w-3 shrink-0 [&>svg]:h-3 [&>svg]:w-3">
          {icon}
        </span>
      )}
      {children}
    </span>
  );
}
