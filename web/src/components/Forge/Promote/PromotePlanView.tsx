// Copyright (c) 2025 Reliant Labs

/**
 * The promote DIFF PREVIEW: everything a reviewer needs before authorising a
 * write that cannot be undone.
 *
 * PURE PROPS, like TopologyView — it takes a plan and renders it, so the visual
 * contract can be tested by handing it a document rather than by standing up a
 * query client and a transport.
 *
 * Four facts are given structural prominence, because each one is a way a
 * reviewer can misread this screen into authorising something else:
 *
 *   DIRECTION, as a banner above everything. A rollback is not a tinted forward
 *   promote — it gets its own heading, icon and destructive treatment. See
 *   promoteVocabulary.
 *
 *   STRUCTURAL vs VERSION changes. `added`/`removed` alter which images the
 *   binding declares at all; they carry a +/− sigil and a solid left rule, while
 *   a digest move shows two digests and an arrow.
 *
 *   THE COMMIT RANGE'S OWN STATE. When forge could not compute the range this
 *   renders forge's explanation and NO number. "0 commits" is a different claim
 *   from "we could not tell", and the states that mean the latter — a release
 *   cut from a dirty tree, a ledger from another branch — are ordinary.
 *
 *   SHIPS NOTHING. Promote moves a pointer; not one byte reaches a cluster until
 *   `forge env deploy` runs. That gap being invisible is the entire reason
 *   `forge env verify` exists, so the plan states it before the write and the
 *   applied panel states it again after.
 */

import { AlertTriangle, GitCommit, Package } from "lucide-react";

import { cn } from "@/lib/utils";
import { Tooltip } from "@/components/ui/Tooltip";
import {
  changeShapeOf,
  commitRangeKind,
  imageChangeOf,
  promoteDirectionOf,
  type ForgePromoteCommitRange,
  type ForgePromoteImageChange,
  type ForgePromotePlan,
} from "@/services/forge/promote";
import { shortDigest } from "@/services/forge/topology";

import { CHANGE_STYLES, DIRECTION_STYLES } from "./promoteVocabulary";

export interface PromotePlanViewProps {
  plan: ForgePromotePlan;
  /**
   * Set false when the CONTAINER renders the ships-nothing notice itself, as the
   * applied panel does to give it top billing. Rendering it in both places would
   * put the same statement on screen twice, which reads as a template bug and
   * teaches the eye to skip it — the opposite of what this notice is for.
   */
  showShipsNothing?: boolean;
}

export function PromotePlanView({ plan, showShipsNothing = true }: PromotePlanViewProps) {
  const direction = promoteDirectionOf(plan.direction);
  const style = DIRECTION_STYLES[direction];
  const DirectionIcon = style.icon;
  const images = plan.images ?? [];

  return (
    <div className="space-y-4" data-testid="promote-plan">
      {/* DIRECTION. First thing on the screen, and its own banner — a rollback
          must never arrive as a subtitle on a forward promote. */}
      <section
        data-testid="promote-direction"
        data-direction={direction}
        className={cn("rounded-lg px-4 py-3", style.container)}
      >
        <div className={cn("flex items-center gap-2", style.foreground)}>
          <DirectionIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
          <h3 className="text-sm font-medium" data-testid="promote-direction-label">
            {style.label}
          </h3>
        </div>
        {/* Forge's own sentence when it sent one — it names the releases and the
            gap. The vocabulary's blurb is the fallback, never a replacement. */}
        <p className="mt-1 text-xs text-muted-foreground" data-testid="promote-direction-detail">
          {plan.direction_detail || style.blurb}
        </p>
        {typeof plan.releases_between === "number" && plan.releases_between > 0 && (
          <p className="mt-1 text-2xs text-muted-foreground">
            {plan.releases_between} release{plan.releases_between === 1 ? "" : "s"} separate them.
          </p>
        )}
      </section>

      <BindingSummary plan={plan} />

      {/* IMAGES. */}
      <section className="space-y-2" data-testid="promote-images">
        <div className="flex items-baseline justify-between gap-3">
          <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
            <Package className="h-3.5 w-3.5" aria-hidden="true" />
            Images
          </h3>
          <TallyStrip plan={plan} />
        </div>

        {images.length === 0 ? (
          <p className="rounded-md border border-dashed border-border px-3 py-4 text-center text-xs text-muted-foreground">
            This plan lists no images.
          </p>
        ) : (
          <ul className="space-y-1">
            {images.map((image) => (
              <ImageChangeRow key={image.image} image={image} />
            ))}
          </ul>
        )}
      </section>

      <CommitRange commits={plan.commits} />

      {showShipsNothing && <ShipsNothingNotice plan={plan} />}
    </div>
  );
}

/**
 * The from/to binding. `current.bound === false` gets its own sentence rather
 * than an empty release, because a first promote is a different situation from
 * a binding whose version is blank — and it is also the case that licenses the
 * `expectUnbound` half of the confirmation token.
 */
function BindingSummary({ plan }: { plan: ForgePromotePlan }) {
  const current = plan.current;
  const target = plan.target;
  const unbound = current?.bound === false;

  return (
    <section
      className="grid gap-3 rounded-lg border border-border px-4 py-3 sm:grid-cols-2"
      data-testid="promote-binding"
    >
      <div className="space-y-1">
        <h4 className="text-2xs font-medium uppercase tracking-wide text-muted-foreground">
          Currently bound
        </h4>
        {unbound ? (
          <p
            data-testid="promote-current-unbound"
            className="text-xs text-muted-foreground"
          >
            Never promoted — this environment has no binding yet.
          </p>
        ) : (
          <>
            <p className="font-mono text-sm text-foreground" data-testid="promote-current-release">
              {current?.release || "unknown"}
            </p>
            {current?.promoted_at && (
              // Promote time, labelled as such. It looks like a deploy timestamp
              // and is not one.
              <Tooltip content="When this environment was PROMOTED to its current release — not when it was deployed.">
                <span className="text-2xs text-muted-foreground">
                  promoted {formatStamp(current.promoted_at)}
                </span>
              </Tooltip>
            )}
            {current?.release_known === false && (
              // A real binding pointing at a ledger this checkout has never
              // seen. Not an error; provenance is simply unavailable.
              <p className="text-2xs text-muted-foreground">
                {current.note ||
                  "This release's ledger is not in this checkout, so its provenance cannot be shown."}
              </p>
            )}
            {current?.git?.dirty === true && <DirtyBadge testId="promote-current-dirty" />}
          </>
        )}
      </div>

      <div className="space-y-1">
        <h4 className="text-2xs font-medium uppercase tracking-wide text-muted-foreground">
          Promoting to
        </h4>
        <p className="font-mono text-sm text-foreground" data-testid="promote-target-release">
          {target?.release || plan.release || "unknown"}
        </p>
        {target?.created_at && (
          <span className="text-2xs text-muted-foreground">cut {formatStamp(target.created_at)}</span>
        )}
        {typeof target?.images === "number" && (
          <p className="text-2xs text-muted-foreground">
            pins {target.images} image{target.images === 1 ? "" : "s"}
          </p>
        )}
        {target?.git?.dirty === true && <DirtyBadge testId="promote-target-dirty" />}
      </div>
    </section>
  );
}

/**
 * A release cut from a tree with uncommitted changes ships bytes that match no
 * reviewable commit — so nobody can say what is in it. A hard badge, not a
 * tooltip-only hint, on either endpoint.
 */
function DirtyBadge({ testId }: { testId: string }) {
  return (
    <Tooltip content="This release was cut from a tree with uncommitted changes. The bytes it ships correspond to no reviewable commit, so what is in it cannot be established from git.">
      <span
        data-testid={testId}
        className="inline-flex items-center gap-1 rounded-full bg-destructive/15 px-2 py-0.5 text-2xs text-destructive"
      >
        <AlertTriangle className="h-3 w-3" aria-hidden="true" />
        dirty tree
      </span>
    </Tooltip>
  );
}

/**
 * The tally, with structural counts separated from the version count. Added and
 * removed are badged even at zero when the other structural count is non-zero
 * would be noise, so each appears only when it happened — but when either does,
 * it reads as its own category rather than a number in a row of four.
 */
function TallyStrip({ plan }: { plan: ForgePromotePlan }) {
  const tally = plan.tally ?? {};
  const entries: Array<{ change: "added" | "removed" | "changed" | "unchanged"; count: number }> = [
    { change: "added", count: tally.added ?? 0 },
    { change: "removed", count: tally.removed ?? 0 },
    { change: "changed", count: tally.changed ?? 0 },
    { change: "unchanged", count: tally.unchanged ?? 0 },
  ];

  return (
    <div className="flex flex-wrap items-center gap-1.5" data-testid="promote-tally">
      {entries
        .filter((entry) => entry.count > 0)
        .map((entry) => {
          const style = CHANGE_STYLES[entry.change];
          return (
            <span
              key={entry.change}
              data-testid={`promote-tally-${entry.change}`}
              data-shape={changeShapeOf(entry.change)}
              className={cn(
                "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-2xs",
                changeShapeOf(entry.change) === "structural"
                  ? cn("border border-solid", style.foreground)
                  : "text-muted-foreground"
              )}
            >
              {style.sigil && <span aria-hidden="true">{style.sigil}</span>}
              {entry.count} {style.label.toLowerCase()}
            </span>
          );
        })}
    </div>
  );
}

/**
 * One image's row.
 *
 * A structural change (added/removed) shows the sigil, the solid left rule and
 * the ONE digest that exists. A version change shows both digests with an arrow
 * between them, which is the "from what, to what" question a reviewer brings to
 * a digest move. Rendering added/removed the same way would print an arrow with
 * an empty side — the exact misreading this separation prevents.
 */
function ImageChangeRow({ image }: { image: ForgePromoteImageChange }) {
  const change = imageChangeOf(image.change);
  const shape = changeShapeOf(change);
  const style = CHANGE_STYLES[change];
  const Icon = style.icon;

  return (
    <li
      data-testid={`promote-image-${image.image}`}
      data-change={change}
      data-shape={shape}
      className={cn("flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md px-3 py-2", style.row)}
    >
      <span className={cn("flex w-24 items-center gap-1 text-2xs font-medium", style.foreground)}>
        {style.sigil ? (
          <span aria-hidden="true" className="font-mono">
            {style.sigil}
          </span>
        ) : (
          <Icon className="h-3 w-3" aria-hidden="true" />
        )}
        {style.label}
      </span>

      <span className="font-mono text-xs text-foreground">{image.image}</span>

      <Tooltip content={style.blurb}>
        <span className="font-mono text-2xs text-muted-foreground">
          {shape === "structural" ? (
            // One side genuinely does not exist. No arrow, no empty half.
            <>{shortDigest(image.target_digest || image.current_digest)}</>
          ) : (
            <>
              {shortDigest(image.current_digest) || "—"}
              <span aria-hidden="true" className="px-1">
                →
              </span>
              {shortDigest(image.target_digest) || "—"}
            </>
          )}
        </span>
      </Tooltip>
    </li>
  );
}

/**
 * The source change being approved — or an honest statement that it could not be
 * established.
 *
 * The `unavailable` branch renders NO count. That is the whole point of this
 * component: `count` is present and zero in every unavailable state, so the
 * obvious `count > 0` test would render "we could not tell" as "no changes".
 *
 * `reverts` inverts the list: for a rollback these commits are being taken AWAY,
 * not landed. Labelling them "incoming changes" would flip the most consequential
 * fact on the screen, so the heading changes with the flag.
 */
function CommitRange({ commits }: { commits: ForgePromoteCommitRange | undefined }) {
  const kind = commitRangeKind(commits?.state);
  const reverts = commits?.reverts === true;

  return (
    <section
      className="space-y-2 rounded-lg border border-border px-4 py-3"
      data-testid="promote-commits"
      data-range-kind={kind}
      data-range-state={commits?.state ?? "unknown"}
    >
      <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <GitCommit className="h-3.5 w-3.5" aria-hidden="true" />
        {reverts ? "Commits being taken away" : "Commits being promoted"}
      </h3>

      {kind === "unavailable" ? (
        // Forge's own explanation, and deliberately no number.
        <p
          data-testid="promote-commits-unavailable"
          className="rounded-md border border-dashed border-border px-3 py-2 text-xs text-muted-foreground"
        >
          {commits?.detail ||
            "The commit range could not be established, so the source change being promoted is not known."}
        </p>
      ) : kind === "empty-by-definition" ? (
        <p data-testid="promote-commits-none" className="text-xs text-muted-foreground">
          {commits?.detail || "There is no commit range for this promote."}
        </p>
      ) : (
        <>
          <p data-testid="promote-commits-count" className="text-xs text-foreground">
            {commits?.count ?? 0} commit{(commits?.count ?? 0) === 1 ? "" : "s"}
            {reverts && (
              <span className="text-destructive"> would be reverted by this rollback</span>
            )}
          </p>
          {(commits?.commits?.length ?? 0) > 0 && (
            <ul className="space-y-0.5">
              {commits?.commits?.map((line) => (
                <li key={line} className="truncate font-mono text-2xs text-muted-foreground">
                  {line}
                </li>
              ))}
            </ul>
          )}
          {commits?.truncated && (
            <p className="text-2xs text-muted-foreground">
              Showing the most recent of {commits.count} commits.
            </p>
          )}
        </>
      )}
    </section>
  );
}

/**
 * Promote ships NOTHING.
 *
 * Rendered from `ships_nothing`, which forge sets on every plan including
 * applied ones, and it carries forge's `next_step` verbatim. A UI that let a
 * user leave believing they had deployed would be worse than no UI at all —
 * that gap is why `forge env verify` exists.
 */
export function ShipsNothingNotice({ plan }: { plan: ForgePromotePlan }) {
  if (plan.ships_nothing === false) return null;

  return (
    <section
      data-testid="promote-ships-nothing"
      className="rounded-lg border border-dashed border-border px-4 py-3"
    >
      <p className="text-xs text-foreground">
        {plan.note || "Promoting moves a pointer. Nothing is deployed and no cluster changes."}
      </p>
      {plan.next_step && (
        <p className="mt-1.5 text-xs text-muted-foreground">
          To actually ship these digests, run{" "}
          <code
            data-testid="promote-next-step"
            className="rounded bg-muted px-1.5 py-0.5 font-mono text-2xs text-foreground"
          >
            {plan.next_step}
          </code>
        </p>
      )}
    </section>
  );
}

/** Renders an RFC3339 stamp locally, falling back to the raw string. */
function formatStamp(value: string): string {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return parsed.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
