import { useCallback, useEffect, useMemo, useState } from "react";
import { Tooltip } from "../ui/Tooltip";
import { ArrowUpRight, Play, X } from "lucide-react";
import { useDaemonList, useResumeDaemon } from "@/hooks/useOnboardingQueries";
import {
  DaemonStatus,
  type DaemonInfo as Daemon,
} from "@/gen/reliant/v1/daemon_registry_pb";
import { useGoToBilling } from "@/hooks/useGoToBilling";
import { resumeErrorMessage, resumeErrorNeedsUpgrade } from "@/lib/daemon-resume";

const RESUME_POLL_MS = 3_000;
const RESUME_GRACE_MS = 120_000;
const DISMISS_KEY = "reliant.resumeDaemonPill.dismissed";

interface ResumeDaemonPillProps {
  placement?: "absolute" | "inline";
}

function signature(ids: string[]): string {
  return ids.slice().sort().join(",");
}

function readDismissed(): string {
  if (typeof window === "undefined") return "";
  try {
    return sessionStorage.getItem(DISMISS_KEY) ?? "";
  } catch {
    return "";
  }
}

function writeDismissed(sig: string): void {
  try {
    sessionStorage.setItem(DISMISS_KEY, sig);
  } catch {
    // sessionStorage can throw in private modes; non-fatal.
  }
}

export function ResumeDaemonPill({ placement = "absolute" }: ResumeDaemonPillProps) {
  const goToBilling = useGoToBilling();
  // Daemons whose Resume RPC succeeded. The registry's lifecycle mirror lags
  // the real state (control-plane's reconciler writes it), so the list keeps
  // saying "suspended" for a while after a successful resume; trust the RPC
  // until the list catches up or RESUME_GRACE_MS passes.
  const [resumedIds, setResumedIds] = useState<ReadonlySet<string>>(() => new Set());
  const { data: daemons = [] } = useDaemonList({
    refetchInterval: resumedIds.size > 0 ? RESUME_POLL_MS : false,
  });
  const [dismissedSig, setDismissedSig] = useState<string>(() => readDismissed());
  const [error, setError] = useState<{ message: string; upgrade: boolean } | null>(null);
  // The hook routes reasoned-quota errors to the global UpgradeRequiredModal
  // and only fires onError for OTHER failures. Without that filter the pill
  // used to render "[resource_exhausted] …" under the modal.
  const resume = useResumeDaemon({
    onSuccess: (id) => {
      setError(null);
      setResumedIds((prev) => new Set(prev).add(id));
      window.setTimeout(() => {
        setResumedIds((prev) => {
          if (!prev.has(id)) return prev;
          const next = new Set(prev);
          next.delete(id);
          return next;
        });
      }, RESUME_GRACE_MS);
    },
    onError: (err) =>
      setError({ message: resumeErrorMessage(err), upgrade: resumeErrorNeedsUpgrade(err) }),
  });

  const { active, suspended } = useMemo(() => {
    const a: Daemon[] = [];
    const s: Daemon[] = [];
    for (const d of daemons) {
      if (d.status === DaemonStatus.ACTIVE) a.push(d);
      else if (d.status === DaemonStatus.SUSPENDED && !resumedIds.has(d.daemonId)) s.push(d);
    }
    return { active: a, suspended: s };
  }, [daemons, resumedIds]);

  // Once the list stops reporting a resumed daemon as suspended, forget it so a
  // later suspend shows the pill again.
  useEffect(() => {
    if (resumedIds.size === 0) return;
    const stillSuspended = new Set(
      daemons.filter((d) => d.status === DaemonStatus.SUSPENDED).map((d) => d.daemonId),
    );
    const settled = [...resumedIds].filter((id) => !stillSuspended.has(id));
    if (settled.length === 0) return;
    setResumedIds((prev) => new Set([...prev].filter((id) => stillSuspended.has(id))));
  }, [daemons, resumedIds]);

  // Signature changes when a new daemon gets suspended → pill reappears even if
  // the user dismissed an earlier set.
  const sig = useMemo(() => signature(suspended.map((d) => d.daemonId)), [suspended]);

  const dismiss = useCallback(() => {
    setDismissedSig(sig);
    writeDismissed(sig);
  }, [sig]);

  useEffect(() => {
    if (suspended.length === 0) setError(null);
  }, [suspended.length]);

  if (active.length > 0 || suspended.length === 0) return null;
  if (dismissedSig && dismissedSig === sig) return null;

  const handleResume = (id: string) => {
    if (resume.isPending) return;
    setError(null);
    resume.mutate(id);
  };

  const handleUpgrade = () => {
    // In-app upgrade path. goToBilling routes an anonymous session through
    // identity linking first, since a plan bought against a browser session
    // belongs to nobody reachable.
    goToBilling();
  };

  const busyId =
    resume.isPending && typeof resume.variables === "string" ? resume.variables : null;

  return (
    <div
      className={
        placement === "absolute"
          ? "pointer-events-none absolute left-1/2 top-3 z-20 -translate-x-1/2"
          : "pointer-events-none relative z-20 flex w-full justify-center"
      }
    >
      <div className="pointer-events-auto flex flex-col items-center gap-1">
        <div className="inline-flex items-center gap-0.5 rounded-full border border-amber-500/30 bg-amber-500/10 px-1.5 py-1 text-sm shadow-md backdrop-blur">
          {suspended.length > 1 ? (
            <PillDropdown
              suspended={suspended}
              onResume={handleResume}
              busyId={busyId}
            />
          ) : (
            <ResumeButton
              daemon={suspended[0]}
              onResume={handleResume}
              busy={busyId === suspended[0].daemonId}
            />
          )}
          <button
            type="button"
            onClick={dismiss}
            aria-label="Dismiss"
            className="rounded-full p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>
        {error && (
          <div className="max-w-[min(560px,calc(100vw-3rem))] rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-center text-xs leading-relaxed text-destructive-ink shadow-sm backdrop-blur">
            <span>{error.message}</span>
            {error.upgrade && (
              <button
                type="button"
                onClick={handleUpgrade}
                className="ml-2 inline-flex items-center gap-1 font-medium underline-offset-2 hover:underline"
              >
                Upgrade plan
                <ArrowUpRight className="h-3 w-3" />
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

interface ResumeButtonProps {
  daemon: Daemon;
  onResume: (id: string) => void | Promise<void>;
  busy: boolean;
}

function ResumeButton({ daemon, onResume, busy }: ResumeButtonProps) {
  return (
    <Tooltip content={`Resume ${daemon.hostname}`} placement="top" delay={300} wrapperClassName="inline-flex">
<button
      type="button"
      onClick={() => void onResume(daemon.daemonId)}
      disabled={busy}
      className="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 font-medium text-amber-500 transition-colors hover:bg-amber-500/10 disabled:opacity-60"
    >
      <Play className="h-3.5 w-3.5" />
      <span>{busy ? "Resuming…" : `Resume ${daemon.hostname}`}</span>
    </button>
</Tooltip>
  );
}

interface PillDropdownProps {
  suspended: Daemon[];
  onResume: (id: string) => void | Promise<void>;
  busyId: string | null;
}

function PillDropdown({ suspended, onResume, busyId }: PillDropdownProps) {
  const [open, setOpen] = useState(false);

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (!target.closest("[data-resume-daemon-pill]")) setOpen(false);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);

  return (
    <div className="relative" data-resume-daemon-pill>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 font-medium text-amber-500 transition-colors hover:bg-amber-500/10"
      >
        <Play className="h-3.5 w-3.5" />
        <span>{suspended.length} suspended</span>
      </button>
      {open && (
        <div className="absolute left-1/2 top-full mt-2 w-64 -translate-x-1/2 rounded-md border border-border bg-popover py-1 shadow-lg">
          <div className="px-3 py-1.5 text-xs font-medium uppercase tracking-wider text-muted-foreground">
            Resume an environment
          </div>
          {suspended.map((d) => {
            const busy = busyId === d.daemonId;
            return (
              <button
                key={d.daemonId}
                type="button"
                onClick={() => {
                  setOpen(false);
                  void onResume(d.daemonId);
                }}
                disabled={busy}
                className="flex w-full items-center justify-between gap-3 px-3 py-2 text-sm hover:bg-accent disabled:opacity-60"
              >
                <span className="truncate">{d.hostname}</span>
                <span className="inline-flex items-center gap-1 text-amber-500">
                  <Play className="h-3.5 w-3.5" />
                  {busy ? "Resuming…" : "Resume"}
                </span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
