/**
 * ConnectDaemonModal — the picker's in-place "Connect a new daemon" flow.
 *
 * Offers BOTH connect paths without bouncing the user into the onboarding
 * wizard:
 *   - Managed cloud daemon: provisions a hosted daemon via CreateDaemon (only
 *     offered when this deployment has cloud daemons AND the account is
 *     entitled to compute).
 *   - Self-hosted daemon: the SelfHostedDaemonConnect panel (shared with
 *     onboarding's ComputeStep) — generates a token + shows the
 *     `reliant daemon start --token` install steps.
 *
 * The modal auto-dismisses once a daemon connects (the picker rerenders into
 * its normal project list as soon as useDaemonStatus reports an active daemon).
 *
 * ## Why eligibility is checked here
 *
 * `capabilities.cloudDaemons` answers "does this DEPLOYMENT have cloud
 * daemons", which is not the same question as "may THIS ACCOUNT start one".
 * Gating on it alone offered a fully-enabled "Provision a hosted daemon now"
 * button to unfunded users, and the click really did fire CreateDaemon.
 *
 * Onboarding's ComputeStep has always consulted `useCloudEligibility()` for
 * exactly this reason, and shows the coupon/plans affordances when the answer
 * is no. This modal is the other door into the same action, so it asks the
 * same question and offers the same way out. The server remains the authority;
 * this only stops the UI committing to a flow it has been told will fail.
 *
 * Extracted from ProjectPicker.tsx so the gate is directly testable.
 */
import { useCallback, useEffect, useState } from "react";
import { ArrowLeft, Check, Cloud, Loader2, Monitor } from "lucide-react";
import { Modal } from "../ui/Modal";
import { SelfHostedDaemonConnect } from "./SelfHostedDaemonConnect";
import { RedeemCouponForm } from "../RedeemCouponForm";
import { useGoToBilling } from "@/hooks/useGoToBilling";
import { useCloudEligibility, useCreateDaemon } from "@/hooks/useOnboardingQueries";
import { capabilities } from "../../services/controlPlane/capabilities";
import { cn } from "../../lib/utils";

const MANAGED_DAEMON_TYPE = 1;
const MANAGED_DAEMON_SIZE_SMALL = 1;

export function ConnectDaemonModal({
  isOpen,
  onClose,
}: {
  isOpen: boolean;
  onClose: () => void;
}) {
  const hasCloud = capabilities.cloudDaemons;
  // "cloud" | "self" | null — null shows the choice, the others show the
  // respective flow. Default to the choice screen so the user explicitly
  // picks how they want to connect.
  const [mode, setMode] = useState<"cloud" | "self" | null>(null);
  const [cloudError, setCloudError] = useState<string | null>(null);
  const [cloudStarted, setCloudStarted] = useState(false);

  const goToBilling = useGoToBilling();
  const {
    eligible,
    reason,
    isLoading: eligibilityLoading,
    refetch: refetchEligibility,
  } = useCloudEligibility();

  // "Not yet known" is not "allowed": while the eligibility query is in flight
  // the cloud option stays disabled rather than defaulting open. Deriving
  // permission from a still-loading query is how a refused action gets offered.
  const canStartCloud = hasCloud && eligible && !eligibilityLoading;

  const createDaemonMutation = useCreateDaemon({
    onError: (err) => {
      const msg = err instanceof Error ? err.message : "Failed to start cloud daemon";
      setCloudError(msg);
    },
  });

  // Reset to the choice screen each time the modal opens so a prior session's
  // mode doesn't leak in.
  useEffect(() => {
    if (isOpen) {
      setMode(null);
      setCloudError(null);
      setCloudStarted(false);
    }
  }, [isOpen]);

  const startCloudDaemon = useCallback(async () => {
    // Belt and braces with the button's disabled state, for the same reason
    // ComputeStep carries this guard: `disabled` is a rendering concern and can
    // be defeated by a stale render or a keyboard activation racing a refetch.
    // Starting to provision a machine the server will refuse is worse than
    // doing nothing.
    if (!canStartCloud) return;

    setCloudError(null);
    try {
      const { listDaemons, hasActiveDaemon } = await import(
        "../../services/controlPlane/daemon"
      );
      const { daemons } = await listDaemons();
      if (hasActiveDaemon(daemons)) {
        // Already have an active daemon — nothing to provision; the picker
        // will pick it up on the next poll.
        setCloudStarted(true);
        return;
      }
      if (daemons.length > 0) {
        // A suspended/disconnected daemon exists — resume it instead of
        // creating a duplicate. Resume failures are non-fatal; the user can
        // retry from the "Resume a daemon" list.
        const id = daemons[0]?.id ?? "";
        if (id) {
          const { resumeDaemon } = await import(
            "../../services/controlPlane/daemon"
          );
          await resumeDaemon(id);
          setCloudStarted(true);
          return;
        }
      }
      await createDaemonMutation.mutateAsync({
        name: "workspace-daemon",
        daemonType: MANAGED_DAEMON_TYPE,
        size: MANAGED_DAEMON_SIZE_SMALL,
        gitRepo: "",
        gitBranch: "main",
      });
      setCloudStarted(true);
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to start cloud daemon";
      setCloudError(msg);
    }
  }, [canStartCloud, createDaemonMutation]);

  const startingCloud = createDaemonMutation.isPending;

  const cloudUnavailableCopy = !hasCloud
    ? "Cloud daemons are not enabled for this deployment."
    : eligibilityLoading
      ? "Checking your plan…"
      : !eligible
        ? (reason ??
          "Redeem a coupon code or choose a plan to start a cloud machine.")
        : "Provision a hosted daemon now. No install required.";

  return (
    <Modal isOpen={isOpen} onClose={onClose} title="Connect a daemon" size="lg">
      {mode === null && (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            A daemon runs your code. Connect one to start working — either a
            hosted Reliant Cloud daemon or your own self-hosted machine.
          </p>
          <div className="grid gap-3 sm:grid-cols-2">
            <button
              type="button"
              onClick={() => {
                setMode("cloud");
                void startCloudDaemon();
              }}
              disabled={!canStartCloud}
              className={cn(
                "flex min-w-0 flex-col items-start gap-3 rounded-xl border-2 p-5 text-left transition-all",
                canStartCloud
                  ? "border-primary/25 bg-primary/5 hover:border-primary/50 hover:bg-primary/10"
                  : "cursor-not-allowed border-border/50 bg-muted/30 opacity-70",
              )}
            >
              <div className="rounded-lg bg-primary/15 p-2.5 text-primary">
                <Cloud className="h-6 w-6" />
              </div>
              <div className="space-y-1">
                <span className="block text-sm font-semibold text-foreground">
                  Reliant Cloud
                </span>
                <span className="block text-xs leading-relaxed text-muted-foreground">
                  {cloudUnavailableCopy}
                </span>
              </div>
            </button>

            <button
              type="button"
              onClick={() => setMode("self")}
              className="flex min-w-0 flex-col items-start gap-3 rounded-xl border-2 border-border/50 bg-background p-5 text-left transition-all hover:border-primary/50 hover:bg-muted/50"
            >
              <div className="rounded-lg bg-muted p-2.5 text-muted-foreground">
                <Monitor className="h-6 w-6" />
              </div>
              <div className="space-y-1">
                <span className="block text-sm font-semibold text-foreground">
                  Self-hosted
                </span>
                <span className="block text-xs leading-relaxed text-muted-foreground">
                  Run the daemon on your own laptop or server with a token.
                </span>
              </div>
            </button>
          </div>

          {/* An ineligible user must always get the explanation AND the way
              out — otherwise this screen is a dead end with a greyed button
              and no account of why. Mirrors ComputeStep's affordances. */}
          {hasCloud && !eligible && !eligibilityLoading && (
            <div className="space-y-3 rounded-xl border border-border/50 bg-muted/30 p-4">
              <p className="text-xs text-muted-foreground">{reason}</p>
              <RedeemCouponForm onRedeemed={() => void refetchEligibility()} />
              <button
                type="button"
                onClick={goToBilling}
                className="text-xs font-medium text-primary transition-colors hover:underline"
              >
                View plans
              </button>
            </div>
          )}
        </div>
      )}

      {mode === "cloud" && (
        <div className="space-y-4">
          <button
            type="button"
            onClick={() => setMode(null)}
            className="inline-flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            <ArrowLeft className="h-3.5 w-3.5" />
            Back
          </button>
          <div className="flex items-center gap-3 rounded-xl border border-border/50 bg-muted/30 p-4">
            {startingCloud ? (
              <Loader2 className="h-5 w-5 animate-spin text-primary" />
            ) : cloudStarted ? (
              <Check className="h-5 w-5 text-emerald-500" />
            ) : (
              <Cloud className="h-5 w-5 text-primary" />
            )}
            <div className="min-w-0">
              <h3 className="text-sm font-medium text-foreground">
                {startingCloud
                  ? "Requesting a cloud daemon..."
                  : cloudStarted
                    ? "Cloud daemon requested"
                    : "Reliant Cloud"}
              </h3>
              <p className="mt-0.5 text-xs text-muted-foreground">
                {cloudStarted
                  ? "It may take a few minutes to provision. This screen will refresh once it connects."
                  : "Provisioning a hosted daemon for your account."}
              </p>
            </div>
          </div>
          {cloudError && (
            <div className="space-y-2">
              <p className="text-xs text-destructive">{cloudError}</p>
              <button
                type="button"
                onClick={() => void startCloudDaemon()}
                disabled={startingCloud}
                className="w-full rounded-lg bg-primary px-4 py-2.5 text-sm font-semibold text-primary-foreground transition-colors hover:bg-primary/90 disabled:opacity-60"
              >
                Try again
              </button>
            </div>
          )}
        </div>
      )}

      {mode === "self" && (
        <div className="space-y-4">
          <button
            type="button"
            onClick={() => setMode(null)}
            className="inline-flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            <ArrowLeft className="h-3.5 w-3.5" />
            Back
          </button>
          {/* Shared with onboarding's ComputeStep — generates a token and
              shows `reliant daemon start --token` install steps. Closes the
              modal the moment the daemon connects. */}
          <SelfHostedDaemonConnect onConnected={onClose} />
        </div>
      )}
    </Modal>
  );
}
