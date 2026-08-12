/**
 * `/m/daemons/$daemonId` — one machine, and the one thing you can do to it.
 *
 * Reads out of the shared `useDaemonList` cache rather than fetching a single
 * daemon: the list screen you tapped through from has already populated it,
 * so the detail paints instantly and the 5s poll keeps both views consistent.
 * There is no GetDaemon on the public control-plane client anyway.
 *
 * Resume is the only write. It is gated on `canResume` (Suspended /
 * Disconnected) rather than on "is not active", because PENDING is already
 * starting and FAILED needs the desktop's recreate path — offering the button
 * in either state produces a call that fails or duplicates work.
 */

import { useState } from "react";
import { Link, useParams } from "@tanstack/react-router";
import { ChevronLeft, Loader2, Play } from "lucide-react";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { useDaemonList, useResumeDaemon } from "@/hooks/useOnboardingQueries";
import {
  getDaemonStatusMessage,
  type Daemon,
} from "@/services/controlPlane/daemon";
import { cn } from "../../lib/utils";
import { canResume, heartbeatMs, sizeLabel } from "./daemonPresentation";
import { DaemonStatusPill } from "./MobileDaemonList";
import { relativeTimeFromMs } from "./relativeTime";
import { useVisibilityPolling } from "./useVisibilityPolling";
import {
  MOBILE_PRIMARY_ACTION,
  MobileCardGroup,
  MobileScreenBody,
  MobileScreenHeader,
} from "./MobileChrome";

const POLL_INTERVAL_MS = 5_000;

function fmtTimestamp(ts?: Timestamp): string {
  if (!ts) return "—";
  try {
    return timestampDate(ts).toLocaleString();
  } catch {
    return "—";
  }
}

function DetailRow({ label, value }: { label: string; value: string }) {
  return (
    // `last:border-b-0` keeps the divider between rows rather than across the
    // bottom of the card, where it would read as a stray line under the group.
    <div className="flex items-start justify-between gap-4 border-b border-border px-4 py-3 last:border-b-0">
      <span className="shrink-0 text-sm text-muted-foreground">{label}</span>
      <span className="min-w-0 break-words text-right text-sm font-medium text-foreground">
        {value || "—"}
      </span>
    </div>
  );
}

function ResumeButton({ daemon }: { daemon: Daemon }) {
  const [error, setError] = useState("");
  // The hook swallows reasoned quota errors — the global upgrade interceptor
  // has already opened the upgrade modal, and rendering the raw
  // "[resource_exhausted] …" under it is the duplicate the desktop pill hit.
  const resume = useResumeDaemon({
    onError: (err) =>
      setError(err instanceof Error ? err.message : "Failed to resume"),
  });

  return (
    <div>
      <button
        type="button"
        onClick={() => {
          setError("");
          resume.mutate(daemon.id);
        }}
        disabled={resume.isPending}
        // 56px, above the shared 48px floor — a primary action a user may be
        // tapping one-handed while walking, which is exactly the case this
        // whole screen exists for.
        className={cn(MOBILE_PRIMARY_ACTION, "min-h-[56px] w-full")}
      >
        {resume.isPending ? (
          <Loader2 className="h-4 w-4 animate-spin" />
        ) : (
          <Play className="h-4 w-4" />
        )}
        {resume.isPending ? "Resuming…" : "Resume"}
      </button>
      {error && (
        <p className="mt-2 text-center text-xs text-destructive">{error}</p>
      )}
    </div>
  );
}

export function MobileDaemonScreen() {
  // `strict: false` rather than `from: "/m/daemons/$daemonId"`. The route is
  // nested under `_authenticated` → `_mobile`, so its registered id is
  // `/_authenticated/_mobile/m/daemons/$daemonId`; passing the *path* throws
  // "Could not find an active match" and strands the user on the desktop error
  // page, which has no tab bar and no back link.
  const { daemonId } = useParams({ strict: false });
  const refetchInterval = useVisibilityPolling(POLL_INTERVAL_MS);
  const { data: daemons, isLoading } = useDaemonList({ refetchInterval });

  const daemon = daemons?.find((d) => d.id === daemonId);

  const header = (
    <MobileScreenHeader
      title={daemon?.name || "Machine"}
      leading={
        <Link
          to="/m/daemons"
          // Explicit px, not `h-10 w-10`: rem sizing resolves against the root
          // font-size, and at the smallest Appearance step `h-10` measures
          // under 44px — on the only way out of this screen.
          className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
          aria-label="Back to machines"
        >
          <ChevronLeft className="h-5 w-5" />
        </Link>
      }
    />
  );

  if (!daemon) {
    return (
      <div className="flex h-full min-h-0 flex-col">
        {header}
        <div className="flex flex-1 items-center justify-center">
          {isLoading ? (
            <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
          ) : (
            // Reachable by deep link to a deleted machine, or one owned by a
            // different account — not an error worth a stack trace.
            <p className="text-sm text-muted-foreground">Machine not found</p>
          )}
        </div>
      </div>
    );
  }

  const beat = heartbeatMs(daemon);
  const statusMessage = getDaemonStatusMessage(daemon);

  return (
    <div className="flex h-full min-h-0 flex-col">
      {header}

      <MobileScreenBody>
        {/* Status, its explanation, and the action it implies read as one
            unit — splitting them across cards made the Resume button look
            unrelated to the state that justifies it. */}
        <MobileCardGroup className="space-y-3 p-4">
          <div className="flex items-center gap-2">
            <DaemonStatusPill daemon={daemon} />
            {sizeLabel(daemon) && (
              <span className="rounded-md bg-primary/10 px-1.5 py-0.5 text-xs font-medium text-primary">
                {sizeLabel(daemon)}
              </span>
            )}
          </div>

          {statusMessage && (
            <p className="text-sm text-muted-foreground">{statusMessage}</p>
          )}

          {canResume(daemon) && <ResumeButton daemon={daemon} />}
        </MobileCardGroup>

        <MobileCardGroup label="Details">
          <DetailRow
            label="Last heartbeat"
            value={beat === null ? "Never" : relativeTimeFromMs(beat)}
          />
          <DetailRow label="Repository" value={daemon.gitRepo} />
          <DetailRow label="Branch" value={daemon.gitBranch} />
          <DetailRow label="Host" value={daemon.hostname} />
          <DetailRow label="Platform" value={daemon.platform} />
          <DetailRow label="Idle timeout" value={daemon.idleTimeout} />
          <DetailRow label="Connected" value={fmtTimestamp(daemon.connectedAt)} />
          <DetailRow label="Created" value={fmtTimestamp(daemon.createdAt)} />
        </MobileCardGroup>
      </MobileScreenBody>
    </div>
  );
}
