import type React from "react";

export type StatusTone = "success" | "warning" | "danger" | "neutral";
export type StatusSize = "sm" | "md" | "lg";

export interface StatusProps {
  /**
   * What the state means, not what colour it is. `success` and `neutral`
   * leave the word in the surrounding text colour; only `warning` and
   * `danger` tint it, so a table of healthy rows reads calm and the one
   * broken row is the thing the eye finds.
   */
  tone?: StatusTone;
  /**
   * The word. Give it unless the very same words already sit beside the dot
   * (a row whose text says "Running"): a dot alone is colour-only
   * information, so without a label it renders as decoration, hidden from
   * screen readers.
   */
  label?: React.ReactNode;
  /** Keep the word for screen readers but show only the dot (a dense grid). */
  hideLabel?: boolean;
  /** Dot size: 6px, 8px (default) or 10px. */
  size?: StatusSize;
  /** The dot pulses: something is in progress (deploying, syncing). */
  pulse?: boolean;
  /**
   * The dot becomes a spinning ring of the same size: the thing is mid-change
   * (a rollout in flight) and its verdict is not in yet. The ring is neutral
   * whatever the tone — a spinner says "wait", not "good" or "bad" — and holds
   * still as a whole ring under reduced motion. Wins over `pulse`.
   */
  busy?: boolean;
  className?: string;
}

const dotTint: Record<StatusTone, string> = {
  success: "bg-success",
  warning: "bg-warning",
  danger: "bg-danger",
  neutral: "bg-ink-muted/50",
};

const wordTint: Record<StatusTone, string> = {
  success: "",
  warning: "text-warning-ink",
  danger: "text-danger-ink",
  neutral: "",
};

const dotSize: Record<StatusSize, string> = {
  sm: "h-1.5 w-1.5",
  md: "h-2 w-2",
  lg: "h-2.5 w-2.5",
};

/** The ring's stroke: thin enough to read as a ring at 6px, heavy enough to see at 10px. */
const ringStroke: Record<StatusSize, string> = {
  sm: "border",
  md: "border-[1.5px]",
  lg: "border-2",
};

/**
 * Status — a dot and a word: the one way to show the state of a thing.
 *
 *   <Status tone="success" label="Healthy" />
 *   <Status tone="danger" label="Crash looping" />
 *   <Status tone="neutral" label="Deploying" pulse />
 *   <Status label="Settling" busy />
 *
 * The word inherits the font size around it, so the same component sits in a
 * `text-sm` table cell and a `text-xs` meta line without a size prop.
 *
 * Status colours appear ONLY on status. The accent is never a status: it marks
 * the primary action, focus and selection, so an "in progress" state is a
 * neutral dot that pulses, not an accent one.
 *
 * Distinct from <Tag> (a bordered label for a kind or an environment, which
 * is not a state) and from <Badge> (a filled pill — at most one per screen).
 */
export default function Status({
  tone = "neutral",
  label,
  hideLabel,
  size = "md",
  pulse,
  busy,
  className,
}: StatusProps) {
  return (
    <span
      className={`inline-flex items-center gap-2 whitespace-nowrap ${className ?? ""}`}
      data-busy={busy ? "true" : undefined}
    >
      {busy ? (
        <span
          aria-hidden="true"
          data-status-spinner=""
          className={`inline-block shrink-0 animate-spin rounded-full border-ink-muted border-t-transparent motion-reduce:animate-none motion-reduce:border-t-ink-muted/40 ${dotSize[size]} ${ringStroke[size]}`}
        />
      ) : (
        <span
          aria-hidden="true"
          className={`inline-block shrink-0 rounded-full ${dotSize[size]} ${dotTint[tone]} ${
            pulse ? "animate-pulse motion-reduce:animate-none" : ""
          }`}
        />
      )}
      {label !== undefined && (
        <span className={hideLabel ? "sr-only" : wordTint[tone]}>{label}</span>
      )}
    </span>
  );
}
