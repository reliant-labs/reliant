import { useEffect, useState } from "react";
import { ArrowLeft, Check, Loader2, RefreshCw } from "lucide-react";
import { Modal } from "../ui/Modal";
import { ComputeStep } from "../OnboardingFlow/steps/ComputeStep";
import type { LaunchPlan } from "../OnboardingFlow/types";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { daemonStartCommand } from "../../lib/cli-commands";

interface ConnectDaemonModalProps {
  isOpen: boolean;
  onClose: () => void;
}

type Phase = "choose" | "wait";

const AUTOCLOSE_MS = 900;

export function ConnectDaemonModal({ isOpen, onClose }: ConnectDaemonModalProps) {
  const [plan, setPlan] = useState<Partial<LaunchPlan>>({});
  const [phase, setPhase] = useState<Phase>("choose");
  const [justConnected, setJustConnected] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const { activeDaemon, daemons, loading, refresh } = useDaemonStatus();

  // Auto-close shortly after a daemon comes online while we're in the wait phase.
  useEffect(() => {
    if (!isOpen || phase !== "wait" || !activeDaemon) return;
    setJustConnected(true);
    const t = setTimeout(onClose, AUTOCLOSE_MS);
    return () => clearTimeout(t);
  }, [isOpen, phase, activeDaemon, onClose]);

  // Reset state whenever the modal closes so the next open starts fresh.
  useEffect(() => {
    if (!isOpen) {
      setPhase("choose");
      setPlan({});
      setJustConnected(false);
      setRefreshing(false);
    }
  }, [isOpen]);

  const isCloud = plan.compute === "cloud_free_trial";

  const handleRefresh = async () => {
    setRefreshing(true);
    try {
      await refresh();
    } finally {
      setRefreshing(false);
    }
  };

  return (
    <Modal isOpen={isOpen} onClose={onClose} title="Connect a daemon" size="xl">
      {phase === "choose" ? (
        <ComputeStep
          plan={plan}
          updatePlan={(updates) => setPlan((prev) => ({ ...prev, ...updates }))}
          onNext={() => setPhase("wait")}
          onBack={onClose}
          hideHeader
        />
      ) : justConnected ? (
        <div className="flex flex-col items-center gap-3 py-8 text-center">
          <div className="rounded-full bg-emerald-500/15 p-3 text-emerald-500">
            <Check className="h-6 w-6" />
          </div>
          <p className="text-base font-semibold text-foreground">Daemon connected</p>
          <p className="text-sm text-muted-foreground">You can start chatting now.</p>
        </div>
      ) : (
        <div className="space-y-5">
          <div className="flex items-start gap-3 rounded-lg border border-border/50 bg-muted/30 p-4">
            <Loader2 className="mt-0.5 h-5 w-5 flex-shrink-0 animate-spin text-primary" />
            <div className="min-w-0 flex-1">
              <p className="text-sm font-medium text-foreground">
                {isCloud ? "Provisioning your cloud daemon" : "Waiting for your daemon to connect"}
              </p>
              <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
                {isCloud
                  ? "This usually takes 30 to 60 seconds. The chat will open as soon as the daemon is ready — you can leave this window open or close it and come back."
                  : "Run the command below in a terminal. This window will close automatically when the daemon connects."}
              </p>
            </div>
          </div>

          {!isCloud && (
            <div className="space-y-1.5">
              <p className="text-xs text-muted-foreground">Daemon start command:</p>
              <code className="block select-all rounded border border-border/40 bg-background px-3 py-2 font-mono text-xs text-foreground break-all">
                {daemonStartCommand()}
              </code>
            </div>
          )}

          {!loading && daemons.length > 0 && !activeDaemon && (
            <p className="rounded border border-yellow-500/30 bg-yellow-500/5 px-3 py-2 text-xs text-yellow-600 dark:text-yellow-400">
              A daemon is registered but not yet active. Make sure it is still running.
            </p>
          )}

          <div className="flex items-center justify-between gap-2 pt-1">
            <button
              type="button"
              onClick={() => setPhase("choose")}
              className="inline-flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
            >
              <ArrowLeft className="h-3.5 w-3.5" />
              Choose a different option
            </button>
            <button
              type="button"
              onClick={handleRefresh}
              disabled={refreshing}
              className="inline-flex items-center gap-1.5 rounded-lg border border-border/50 bg-background px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted disabled:opacity-60"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${refreshing ? "animate-spin" : ""}`} />
              {refreshing ? "Checking..." : "Check now"}
            </button>
          </div>
        </div>
      )}
    </Modal>
  );
}
