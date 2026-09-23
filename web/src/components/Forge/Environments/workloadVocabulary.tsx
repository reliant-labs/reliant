// Copyright (c) 2025 Reliant Labs

/**
 * How a workload's status becomes pixels. One module, so the mapping can be
 * asserted by a test rather than inferred from a rendered table.
 *
 * STATUS IS A DOT PLUS PLAIN TEXT, NOT A FILLED PILL. The screen shows sixteen
 * rows; sixteen filled pills is a wall of colour in which nothing stands out,
 * which is the opposite of what a status column is for. A hairline row and a
 * 6px dot let the one broken workload be the only loud thing on the page.
 *
 * THE UNKNOWN TREATMENT IS NOT A COLOUR CHOICE. `unknown` means forge could
 * not read the cluster, and it must survive a greyscale screenshot in an
 * incident channel, a high-contrast theme, and a reader with a colour-vision
 * deficiency — because the one misreading that matters here is "not measured"
 * taken for "measured and fine". So it is distinguished on a SECOND axis:
 * every measured status gets a SOLID dot, and `unknown` gets a HOLLOW RING.
 * Fill versus no fill separates the certain statuses from the uncertain one,
 * exactly as the dashed treatment does on the topology and status screens, and
 * it is the axis that survives every rendering.
 *
 * The labels never collapse two statuses onto one word, and `unknown`'s says
 * what it is rather than what it resembles: "State unknown" is the absence of
 * a reading, not a mild version of a bad one.
 */

import { cn } from "@/lib/utils";
import type { ForgeCheckStatus } from "@/services/forge/status";

/** The statuses forge puts on a workload, plus the catch-all. */
export const WORKLOAD_STATUSES: ForgeCheckStatus[] = ["pass", "warn", "fail", "unknown", "skip"];

export const WORKLOAD_STATUS_LABELS: Record<ForgeCheckStatus, string> = {
  pass: "Running",
  warn: "Degraded",
  fail: "Failing",
  unknown: "State unknown",
  skip: "Not applicable",
};

/**
 * What each status means for a workload specifically. The `unknown` sentence
 * is the one that stops a reader treating an unreadable cluster as an empty or
 * healthy one.
 */
export const WORKLOAD_STATUS_BLURBS: Record<ForgeCheckStatus, string> = {
  pass: "The cluster was read and this workload has the pods it asked for.",
  warn: "The cluster was read and this workload is running, but not cleanly.",
  fail: "The cluster was read and this workload is not running as declared.",
  unknown:
    "Forge could not read this workload's state. It is declared by the render, and nothing is known about whether it is running — this is neither healthy nor broken.",
  skip: "This workload was deliberately not assessed.",
};

/** Text tint, used for the label beside the dot and for a finding line. */
export const WORKLOAD_STATUS_FOREGROUND: Record<ForgeCheckStatus, string> = {
  pass: "text-foreground",
  warn: "text-warning",
  fail: "text-destructive",
  unknown: "text-muted-foreground",
  skip: "text-muted-foreground",
};

/**
 * normalizeStatus maps forge's string onto the vocabulary. An absent or
 * unrecognised status becomes `unknown`, NEVER `pass` — a status this build
 * cannot read has established nothing, and guessing good makes it
 * indistinguishable from a verified reading.
 */
export function normalizeStatus(status: string | undefined): ForgeCheckStatus {
  switch (status) {
    case "pass":
    case "fail":
    case "warn":
    case "skip":
      return status;
    default:
      return "unknown";
  }
}

/**
 * The status dot. Solid for anything measured; a hollow ring for `unknown`.
 *
 * Deliberately not forge's StatusDot: that component draws a filled dot in
 * every variant, so it has no way to express the fill/no-fill axis the
 * certainty distinction rests on, and an `unknown` rendered as a filled grey
 * dot reads as a status rather than as the absence of one.
 */
export function WorkloadStatusDot({ status }: { status: ForgeCheckStatus }) {
  const measured = status !== "unknown";
  return (
    <span
      aria-hidden="true"
      data-status-dot={status}
      data-measured={measured}
      className={cn(
        "inline-block h-1.5 w-1.5 shrink-0 rounded-full",
        status === "pass" && "bg-success",
        status === "warn" && "bg-warning",
        status === "fail" && "bg-destructive",
        status === "skip" && "bg-muted-foreground",
        // No fill, ring only: the axis that survives greyscale.
        status === "unknown" && "bg-transparent ring-1 ring-muted-foreground/70"
      )}
    />
  );
}

/**
 * Dot plus plain text. The recurring status shape on this screen.
 *
 * `ephemeral` changes the word, not the dot. A Job forge reports as `pass` has
 * not got the pods it asked for — it RAN and finished, and its pods were
 * reaped — so labelling it "Running" beside a Deployment that genuinely is
 * running states something false about both. The two kinds share a verdict and
 * do not share a meaning, and the label is where that divergence belongs: the
 * colour is still "this is fine", which is true of both.
 */
export function WorkloadStatus({
  status,
  ephemeral,
  className,
}: {
  status: ForgeCheckStatus;
  ephemeral?: boolean;
  className?: string;
}) {
  const label =
    ephemeral && status === "pass" ? "Completed" : WORKLOAD_STATUS_LABELS[status];
  return (
    <span
      data-status={status}
      data-ephemeral={ephemeral ? "true" : undefined}
      title={
        ephemeral && status === "pass"
          ? "This ran to completion. Its pods are expected to be gone."
          : WORKLOAD_STATUS_BLURBS[status]
      }
      className={cn("inline-flex items-center gap-2 text-sm", WORKLOAD_STATUS_FOREGROUND[status], className)}
    >
      <WorkloadStatusDot status={status} />
      {label}
    </span>
  );
}
