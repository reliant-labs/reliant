import { useCallback, useEffect, useMemo, useState } from "react";
import { Play, X } from "lucide-react";
import { useDaemonList, useResumeDaemon } from "@/hooks/useOnboardingQueries";
import {
  DAEMON_STATUS_ACTIVE,
  DAEMON_STATUS_SUSPENDED,
  type Daemon,
} from "@/services/controlPlane/daemon";

const DISMISS_KEY = "reliant.resumeDaemonPill.dismissed";

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

export function ResumeDaemonPill() {
  const { data: daemons = [] } = useDaemonList();
  const [dismissedSig, setDismissedSig] = useState<string>(() => readDismissed());
  const [error, setError] = useState("");
  // The hook routes reasoned-quota errors to the global UpgradeRequiredModal
  // and only fires onError for OTHER failures. Without that filter the pill
  // used to render "[resource_exhausted] …" under the modal.
  const resume = useResumeDaemon({
    onError: (err) => {
      setError(err instanceof Error ? err.message : "Failed to resume environment");
    },
  });

  const { active, suspended } = useMemo(() => {
    const a: Daemon[] = [];
    const s: Daemon[] = [];
    for (const d of daemons) {
      if (d.status === DAEMON_STATUS_ACTIVE) a.push(d);
      else if (d.status === DAEMON_STATUS_SUSPENDED) s.push(d);
    }
    return { active: a, suspended: s };
  }, [daemons]);

  // Signature changes when a new daemon gets suspended → pill reappears even if
  // the user dismissed an earlier set.
  const sig = useMemo(() => signature(suspended.map((d) => d.id)), [suspended]);

  const dismiss = useCallback(() => {
    setDismissedSig(sig);
    writeDismissed(sig);
  }, [sig]);

  useEffect(() => {
    if (suspended.length === 0) setError("");
  }, [suspended.length]);

  if (active.length > 0 || suspended.length === 0) return null;
  if (dismissedSig && dismissedSig === sig) return null;

  const handleResume = (id: string) => {
    setError("");
    resume.mutate(id);
  };

  const busyId =
    resume.isPending && typeof resume.variables === "string" ? resume.variables : null;

  return (
    <div className="pointer-events-none absolute left-1/2 top-3 z-20 -translate-x-1/2">
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
              busy={busyId === suspended[0].id}
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
          <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-1 text-xs text-destructive">
            {error}
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
    <button
      type="button"
      onClick={() => void onResume(daemon.id)}
      disabled={busy}
      title={`Resume ${daemon.name}`}
      className="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 font-medium text-amber-500 transition-colors hover:bg-amber-500/10 disabled:opacity-60"
    >
      <Play className="h-3.5 w-3.5" />
      <span>{busy ? "Resuming…" : `Resume ${daemon.name}`}</span>
    </button>
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
            const busy = busyId === d.id;
            return (
              <button
                key={d.id}
                type="button"
                onClick={() => {
                  setOpen(false);
                  void onResume(d.id);
                }}
                disabled={busy}
                className="flex w-full items-center justify-between gap-3 px-3 py-2 text-sm hover:bg-accent disabled:opacity-60"
              >
                <span className="truncate">{d.name}</span>
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
