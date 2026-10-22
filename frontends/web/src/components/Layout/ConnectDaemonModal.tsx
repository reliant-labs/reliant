import { useEffect, useState } from "react";
import { ArrowLeft, Check, RefreshCw } from "lucide-react";
import { Modal } from "../ui/Modal";
import { ComputeStep } from "../OnboardingFlow/steps/ComputeStep";
import { isCloudCompute } from "../OnboardingFlow/types";
import type { LaunchPlan } from "../OnboardingFlow/types";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { useDaemonWait } from "../../hooks/useDaemonWait";
import { DaemonWaitState } from "../DaemonWaitState";
import {
  daemonStartCommand,
  daemonStartCommandNeedsEditing,
  GATEWAY_URL_PLACEHOLDER,
} from "../../lib/cli-commands";

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

  const isCloud = isCloudCompute(plan.compute);

  // Waiting is only meaningful in the wait phase and before a machine lands.
  const daemonWait = useDaemonWait({
    waiting: isOpen && phase === "wait" && !activeDaemon,
    onRetry: refresh,
  });

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
          {/* Same words, same escalation, same failure reasons as every other
              surface — this modal used to invent its own copy and never time
              out or explain anything. */}
          {daemonWait.state && (
            <DaemonWaitState
              state={daemonWait.state}
              variant="panel"
              onRetry={daemonWait.retryNow}
              className="h-auto rounded-lg border border-border/50 bg-muted/30 p-4"
            />
          )}

          {!isCloud && (
            <div className="space-y-1.5">
              <p className="text-xs text-muted-foreground">Daemon start command:</p>
              <code className="block select-all rounded border border-border/40 bg-background px-3 py-2 font-mono text-xs text-foreground break-all">
                {daemonStartCommand()}
              </code>
              {daemonStartCommandNeedsEditing() && (
                <p className="text-xs text-yellow-600 dark:text-yellow-400">
                  Replace {GATEWAY_URL_PLACEHOLDER} with your daemon-gateway
                  address before running this. It is a separate process from the
                  API server, so the daemon cannot infer it on localhost.
                </p>
              )}
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
