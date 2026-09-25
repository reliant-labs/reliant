// Copyright (c) 2025 Reliant Labs

/**
 * The managed secret store's screen body. "secrets tables are fucking ugly" —
 * this is the replacement.
 *
 * ── WHAT "VERCEL" MEANT, CONCRETELY, IN THIS FILE ───────────────────────────
 *
 * Every one of these is a decision that the previous table got the other way
 * round:
 *
 *   - ONE surface, not a stack of slabs. The table sits on a single bordered
 *     card. The detail panel replaces it rather than appearing beside it, so
 *     there is never a card inside a card.
 *   - Hairline borders, no fills. Row separation is a 1px divide, not a striped
 *     or tinted background. Nothing on this screen has a colored row.
 *   - Status is a small dot plus plain text, never a filled pill. Four states
 *     with four dots reads at a glance; four pills is four blocks of colour
 *     fighting the secret names for attention.
 *   - THE ACCENT IS SPENT ONCE — on the primary "Add secret" button. Nothing
 *     else on this screen is accent-coloured. That is what makes the one thing
 *     the user came here to do findable without a legend pointing at it.
 *   - Few type sizes: sm for content, xs for metadata, one semibold heading.
 *   - Monospace STRICTLY for identifiers — secret names, env names, version
 *     numbers. Never the headings, never the prose, never the counts. A
 *     monospaced count is the tell of a table that thinks everything is data.
 *
 * ── WHY NOT forge's data_table COMPONENT ────────────────────────────────────
 *
 * It was the obvious choice and it is the wrong one HERE. data_table brings
 * sorting affordances, a selection column and a pagination footer — chrome that
 * earns its place on an unbounded admin list and actively misinforms on this
 * one. A project has a handful of secrets, all of them fetched, so a "Rows per
 * page: 10" footer beneath six rows tells the reader the list is truncated when
 * it is complete. The component's own docstring makes this same point about its
 * footer. What remains after removing all of that is a list, so this renders a
 * list.
 *
 * ── EVERY STATE IS DESIGNED, NOT INHERITED ──────────────────────────────────
 *
 * Empty, unavailable, read-only-provider and populated are four different
 * screens, and the three non-populated ones are where a scaffold normally
 * leaves a spinner or a shrug. Each gets one calm sentence and, where there is
 * something to do, exactly one action.
 */

import { useMemo } from "react";
import { ArrowLeft, KeyRound, Plus } from "lucide-react";

import StatusDot from "@/components/forge-ui/status_dot";
import { cn } from "@/lib/utils";
import type { ForgeSecretsReport } from "@/services/forge/secrets";
import type { ManagedSecretSummary, ManagedStoreAvailability } from "@/services/forge/secretStore";
import { availabilityExplanation, managedSecretStateExplanation } from "@/services/forge/secretStore";
import {
  joinSecretRows,
  modeExplanation,
  modeLabel,
  modeSupportsWrite,
  rowStatusLabel,
  rowStatusVariant,
  tallyRows,
  type SecretSurfaceMode,
  type SecretSurfaceRow,
} from "@/services/forge/secretSurface";

import { SecretVersionHistory } from "./SecretVersionHistory";
import type { ManagedSecretVersion } from "@/services/forge/secretStore";

export interface ManagedSecretsViewProps {
  env: string;
  mode: SecretSurfaceMode;
  availability: ManagedStoreAvailability;
  /** forge's declaration report — which secrets the env's workloads ask for. */
  report: ForgeSecretsReport | null;
  managed: ManagedSecretSummary[];
  isLoading: boolean;

  /** Detail panel state, lifted so the URL can own it. */
  selectedName: string | null;
  onSelect: (name: string | null) => void;
  versions: ManagedSecretVersion[];
  versionsLoading: boolean;

  onAdd: () => void;
  onSet: (row: SecretSurfaceRow) => void;
  onDelete: (name: string, version: number) => void;
  onUndelete: (name: string, version: number) => void;
  onDestroy: (name: string, version: number) => void;
  pendingVersion: number | null;
}

export function ManagedSecretsView(props: ManagedSecretsViewProps) {
  const { env, mode, availability, report, managed, isLoading, selectedName, onSelect } = props;

  // Only an `available` store's empty list means "holds nothing". Any other
  // availability means this console could not look, and the join falls back
  // to forge's own observation rather than painting every declared secret as
  // a red "Not set" blocker.
  const rows = useMemo(
    () => joinSecretRows(report, managed, availability === "available"),
    [report, managed, availability]
  );
  const tally = useMemo(() => tallyRows(rows), [rows]);
  const canWrite = modeSupportsWrite(mode);

  const selected = selectedName ? rows.find((r) => r.name === selectedName) ?? null : null;

  // The detail view REPLACES the list rather than sitting beside it. A split
  // pane at this width means two cramped columns; a replace means each screen
  // gets the whole width and the back affordance is unambiguous.
  if (selected) {
    return <SecretDetail {...props} row={selected} onBack={() => onSelect(null)} />;
  }

  return (
    <div className="space-y-5" data-testid="managed-secrets">
      <StoreHeader
        env={env}
        mode={mode}
        availability={availability}
        tally={tally}
        canWrite={canWrite}
        onAdd={props.onAdd}
      />

      {isLoading ? (
        <SecretListSkeleton />
      ) : rows.length === 0 ? (
        <EmptyState mode={mode} canWrite={canWrite} onAdd={props.onAdd} />
      ) : (
        <SecretList rows={rows} onSelect={onSelect} />
      )}
    </div>
  );
}

// ── Header ──────────────────────────────────────────────────────────────────

/**
 * The store line and the counts.
 *
 * This is where the "worst legend ever" used to be — three prose boxes across
 * the top of the page to show three numbers. What replaced it: one sentence
 * naming the provider, and a compact inline count strip. The counts are
 * plain text at one size, separated by hairlines, not cards.
 *
 * `unset` is the only count that ever takes a colour, and only when it is
 * non-zero, because it is the only one that means something is broken.
 */
function StoreHeader({
  env,
  mode,
  availability,
  tally,
  canWrite,
  onAdd,
}: {
  env: string;
  mode: SecretSurfaceMode;
  availability: ManagedStoreAvailability;
  tally: ReturnType<typeof tallyRows>;
  canWrite: boolean;
  onAdd: () => void;
}) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-4">
      <div className="space-y-1.5">
        <div className="flex items-center gap-2">
          <h2 className="text-sm font-semibold text-foreground">{modeLabel(mode)}</h2>
          <span className="font-mono text-xs text-muted-foreground">{env}</span>
        </div>
        <p className="max-w-2xl text-xs leading-relaxed text-muted-foreground">
          {/* A reason there is no lookup outranks the mode sentence: it is the
              more specific answer to "why can I not set this here". */}
          <WithCode text={availabilityExplanation(availability) ?? modeExplanation(mode)} />
        </p>

        {tally.total > 0 && (
          <div className="flex items-center gap-3 pt-1 text-xs text-muted-foreground">
            <Count label="total" value={tally.total} />
            <Divider />
            <Count label="set" value={tally.set} />
            {tally.unset > 0 && (
              <>
                <Divider />
                {/* The one count that can take colour, and only when it matters. */}
                <Count label="not set" value={tally.unset} tone="danger" />
              </>
            )}
            {tally.inactive > 0 && (
              <>
                <Divider />
                <Count label="inactive" value={tally.inactive} />
              </>
            )}
            {tally.unknown > 0 && (
              <>
                <Divider />
                <Count label="not known" value={tally.unknown} />
              </>
            )}
          </div>
        )}
      </div>

      {/* The accent, spent once. */}
      {canWrite && (
        <button
          type="button"
          onClick={onAdd}
          data-testid="add-secret"
          className={cn(
            "inline-flex shrink-0 items-center gap-1.5 rounded-md px-3 py-1.5 text-sm font-medium",
            "bg-primary text-primary-foreground transition-colors hover:bg-primary/90"
          )}
        >
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          Add secret
        </button>
      )}
    </div>
  );
}

/**
 * Renders `backticked` spans of a sentence as inline code, so a command the
 * reader has to type is visibly a command. The sentences live in the service
 * layer as plain strings; this is the one place they become markup.
 */
function WithCode({ text }: { text: string }) {
  return (
    <>
      {text.split(/(`[^`]+`)/g).map((part, i) =>
        part.startsWith("`") && part.endsWith("`") ? (
          <code key={i} className="font-mono text-foreground">
            {part.slice(1, -1)}
          </code>
        ) : (
          <span key={i}>{part}</span>
        )
      )}
    </>
  );
}

/** Counts are prose, not data — deliberately NOT monospace. */
function Count({ label, value, tone }: { label: string; value: number; tone?: "danger" }) {
  return (
    <span>
      <span className={cn("font-medium", tone === "danger" ? "text-destructive" : "text-foreground")}>
        {value}
      </span>{" "}
      {label}
    </span>
  );
}

function Divider() {
  return <span aria-hidden="true" className="h-3 w-px bg-border" />;
}

// ── List ────────────────────────────────────────────────────────────────────

/**
 * The table.
 *
 * A semantic <table> for the header/row relationship a screen reader needs,
 * with the visual weight of a list: no vertical rules, no zebra striping, no
 * fills. The only chrome is one hairline under the header and one between
 * rows.
 *
 * The whole row is the click target, not a trailing "View" link — the row IS
 * the affordance, which is why the hover tint is on the row.
 */
function SecretList({
  rows,
  onSelect,
}: {
  rows: SecretSurfaceRow[];
  onSelect: (name: string) => void;
}) {
  return (
    <div className="overflow-hidden rounded-lg border border-border bg-card">
      <table className="w-full border-collapse text-sm">
        <caption className="sr-only">
          Secrets in this environment, by state and version. No values are shown or fetched.
        </caption>
        <thead>
          <tr className="border-b border-border">
            <Th>Name</Th>
            <Th>Status</Th>
            <Th>Version</Th>
            <Th>Last updated</Th>
            <Th>Declared by</Th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr
              key={row.name}
              data-testid={`secret-row-${row.name}`}
              onClick={() => onSelect(row.name)}
              tabIndex={0}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  onSelect(row.name);
                }
              }}
              className={cn(
                "cursor-pointer border-b border-border/60 transition-colors last:border-0",
                "hover:bg-foreground/[0.03] focus:outline-none focus-visible:bg-foreground/[0.03]"
              )}
            >
              {/* Mono: a secret name is an identifier. */}
              <td className="px-4 py-2.5 font-mono text-sm text-foreground">{row.name}</td>
              <td className="px-4 py-2.5">
                <StatusDot
                  size="sm"
                  variant={rowStatusVariant(row)}
                  label={<span className="text-xs">{rowStatusLabel(row)}</span>}
                />
              </td>
              <td className="px-4 py-2.5 font-mono text-xs text-muted-foreground">
                {row.summary && row.summary.currentVersion > 0 ? `v${row.summary.currentVersion}` : "—"}
              </td>
              <td className="px-4 py-2.5 text-xs text-muted-foreground">
                {formatShort(row.summary?.updatedAt)}
              </td>
              <td className="px-4 py-2.5 text-xs text-muted-foreground">
                <DeclaredBy row={row} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Th({ children }: { children: React.ReactNode }) {
  return (
    <th
      scope="col"
      /* Muted uppercase micro-label — the section-label treatment, used for
         column heads so they recede behind the data they describe. */
      className="px-4 py-2 text-left text-2xs font-medium uppercase tracking-wide text-muted-foreground"
    >
      {children}
    </th>
  );
}

/**
 * Which workloads declare this secret. Names are identifiers, so mono.
 *
 * An orphan says so in prose rather than showing an empty cell: "nothing
 * injects this" is a finding, and a blank cell reads as missing data.
 */
function DeclaredBy({ row }: { row: SecretSurfaceRow }) {
  if (row.origin === "orphan") {
    return <span className="italic">Nothing declares this</span>;
  }
  const names = row.declaredBy.map((d) => d.workload).filter((w): w is string => !!w);
  if (names.length === 0) return <span>—</span>;
  return (
    <span className="font-mono">
      {names.slice(0, 2).join(", ")}
      {names.length > 2 && ` +${names.length - 2}`}
    </span>
  );
}

function formatShort(iso: string | undefined): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (!Number.isFinite(date.getTime())) return "—";
  return date.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

// ── Detail ──────────────────────────────────────────────────────────────────

/**
 * One secret: what it is, who wants it, and everything that has happened to it.
 *
 * The state explanation is rendered as a sentence rather than a tooltip because
 * "destroyed" and "no versions" are states a reader has genuinely not seen
 * before, and hiding the difference behind a hover is how the previous version
 * of this surface needed a legend.
 */
function SecretDetail({
  row,
  env,
  mode,
  availability,
  versions,
  versionsLoading,
  onBack,
  onSet,
  onDelete,
  onUndelete,
  onDestroy,
  pendingVersion,
}: ManagedSecretsViewProps & { row: SecretSurfaceRow; onBack: () => void }) {
  const canWrite = modeSupportsWrite(mode);

  return (
    <div className="space-y-5" data-testid="secret-detail">
      <button
        type="button"
        onClick={onBack}
        className="inline-flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
      >
        <ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />
        All secrets
      </button>

      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="space-y-2">
          {/* The secret name is the page's subject AND an identifier: mono. */}
          <h2 className="font-mono text-base font-semibold text-foreground">{row.name}</h2>
          <div className="flex items-center gap-2.5">
            <StatusDot
              size="sm"
              variant={rowStatusVariant(row)}
              label={<span className="text-xs">{rowStatusLabel(row)}</span>}
            />
            <span className="font-mono text-xs text-muted-foreground">{env}</span>
          </div>
          <p className="max-w-2xl text-xs leading-relaxed text-muted-foreground">
            {row.origin === "declared-unset"
              ? "A workload in this environment declares this secret and the store has never held a value for it. Deploys that need it will fail until it is set."
              : row.origin === "declared-unread"
                ? "A workload in this environment declares this secret, but its store could not be read from here, so whether it holds a value is not known. This is not a statement that it is missing."
                : row.origin === "declared-present"
                  ? "Forge reports the store holds a value for this secret. Its version history lives in the store, which this console cannot read for this environment."
                  : row.state
                ? managedSecretStateExplanation(row.state)
                : ""}
          </p>
        </div>

        {canWrite && (
          <button
            type="button"
            onClick={() => onSet(row)}
            data-testid="set-new-version"
            className={cn(
              "inline-flex shrink-0 items-center gap-1.5 rounded-md px-3 py-1.5 text-sm font-medium",
              "bg-primary text-primary-foreground transition-colors hover:bg-primary/90"
            )}
          >
            {row.origin === "declared-unset" ? "Set value" : "Set new version"}
          </button>
        )}
      </div>

      {row.declaredBy.length > 0 && (
        <section className="space-y-2">
          <h3 className="text-2xs font-medium uppercase tracking-wide text-muted-foreground">
            Declared by
          </h3>
          <ul className="space-y-1">
            {row.declaredBy.map((decl, i) => (
              <li key={`${decl.workload}-${i}`} className="text-xs text-muted-foreground">
                <span className="font-mono text-foreground">{decl.workload ?? "—"}</span>
                {decl.kind && <span> · {decl.kind}</span>}
              </li>
            ))}
          </ul>
        </section>
      )}

      {availability !== "available" ? (
        // Only the store knows a secret's versions. When it cannot be read
        // here, "No versions yet" would be a claim — say where they are.
        <p className="max-w-2xl text-xs text-muted-foreground" data-testid="version-history-unavailable">
          Version history is kept in the store, which cannot be read from here for this environment.
        </p>
      ) : (
      <section className="space-y-2">
        <h3 className="text-2xs font-medium uppercase tracking-wide text-muted-foreground">
          Version history
        </h3>
        <SecretVersionHistory
          name={row.name}
          versions={versions}
          isLoading={versionsLoading}
          canMutate={canWrite}
          onDelete={(v) => onDelete(row.name, v)}
          onUndelete={(v) => onUndelete(row.name, v)}
          onDestroy={(v) => onDestroy(row.name, v)}
          pendingVersion={pendingVersion}
        />
      </section>
      )}
    </div>
  );
}

// ── Empty & loading ─────────────────────────────────────────────────────────

/**
 * One calm sentence plus one action — and the action only when there is one.
 *
 * The external case is the important one: it gets NO button, because forge
 * never sees those values and a create affordance there would teach a
 * falsehood. It gets the sentence that says where the value actually comes
 * from instead.
 */
function EmptyState({
  mode,
  canWrite,
  onAdd,
}: {
  mode: SecretSurfaceMode;
  canWrite: boolean;
  onAdd: () => void;
}) {
  return (
    <div
      data-testid="secrets-empty"
      className="flex flex-col items-center gap-3 rounded-lg border border-dashed border-border px-6 py-14 text-center"
    >
      <KeyRound className="h-5 w-5 text-muted-foreground" aria-hidden="true" />
      <p className="max-w-md text-sm text-muted-foreground">
        {mode === "managed"
          ? "No secrets in this environment yet."
          : mode === "managed-remote"
            ? "No secrets are declared here, and this console cannot read the managed store. Set one with forge secret set."
          : mode === "external"
            ? "No secrets are declared here. An external secret manager holds the values for this environment, so they are provisioned outside reliant."
            : mode === "file"
              ? null
              : "This environment has no secret store configured."}
        {/* The file case names the command, because "your machine holds it"
            without "and here is how you write it" is half an answer. */}
        {mode === "file" && (
          <>
            No secrets are declared here. This environment reads values from a gitignored file on
            your own machine — set one with <span className="font-mono">forge secret set</span>.
          </>
        )}
      </p>
      {canWrite && (
        <button
          type="button"
          onClick={onAdd}
          data-testid="add-secret-empty"
          className="mt-1 inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90"
        >
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          Add secret
        </button>
      )}
    </div>
  );
}

function SecretListSkeleton() {
  return (
    <div
      className="overflow-hidden rounded-lg border border-border bg-card"
      data-testid="secrets-skeleton"
    >
      <span className="sr-only">Loading secrets…</span>
      {[0, 1, 2, 3].map((i) => (
        <div key={i} className="flex items-center gap-4 border-b border-border/60 px-4 py-3 last:border-0">
          <div className="h-3.5 w-40 animate-pulse rounded bg-foreground/[0.06]" />
          <div className="h-3.5 w-16 animate-pulse rounded bg-foreground/[0.06]" />
          <div className="h-3.5 w-10 animate-pulse rounded bg-foreground/[0.06]" />
        </div>
      ))}
    </div>
  );
}
