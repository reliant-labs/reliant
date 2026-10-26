import { useId, useRef, useState } from "react";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import type { RecoveryInboxItem } from "@/gen/reliant/v1/inbox_pb";
import { useDaemonList } from "@/hooks/useOnboardingQueries";
import { deleteWorktreeRecovery, restoreWorktreeRecovery } from "@/hooks/worktree-actions";

type Operation = { kind: "restoring" | "deletion_requested"; key: string; target: string };

const buttonClass = "rounded border border-border px-2 py-1 hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50 [@media(pointer:coarse)]:min-h-[44px]";

/** Recovery branches inherit repository visibility; dismissal never deletes them. */
export function RecoveryAction({ recovery }: { recovery: RecoveryInboxItem }) {
  const panelId = useId();
  const [expanded, setExpanded] = useState(false);
  const machines = useDaemonList({ enabled: expanded });
  const [target, setTarget] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [worktreeId, setWorktreeId] = useState("");
  const [resultPhase, setResultPhase] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  // A transport error may follow a committed operation. Keep its identity even
  // when the response is lost; the server supplies the same identity on remount.
  const pending = useRef<Operation | null>(null);
  const inFlight = useRef(false);
  const persisted: Operation | null = recovery.operationKey &&
    (recovery.operation === "restoring" || recovery.operation === "deletion_requested")
    ? { kind: recovery.operation, key: recovery.operationKey, target: recovery.targetDaemonId }
    : null;
  const operation = pending.current ?? persisted;
  const phase = resultPhase || recovery.phase;
  const restored = phase === "restored";
  const deleted = phase === "refs_deleted";
  const destination = operation?.kind === "restoring" ? operation.target : target;
  const available = (machines.data ?? []).filter(machine =>
    machine.status === DaemonStatus.ACTIVE || machine.status === DaemonStatus.IDLE);
  const targetOnline = available.some(machine => machine.daemonId === destination);
  const unverified = ["capturing", "publishing", "delete_authorized"].includes(phase);
  const canRestore = !busy && !deleted && !restored && !unverified &&
    operation?.kind !== "deletion_requested" && targetOnline && !machines.isError;
  const canDelete = !busy && !deleted && !unverified &&
    (operation?.kind !== "restoring" || restored);

  async function restore() {
    if (!canRestore || inFlight.current) return;
    pending.current = operation ?? { kind: "restoring", key: crypto.randomUUID(), target: destination };
    inFlight.current = true;
    setBusy(true);
    setMessage("");
    try {
      const result = await restoreWorktreeRecovery(recovery.recoveryId, pending.current.target, pending.current.key);
      setResultPhase(result.recovery?.phase || "held");
      if (result.worktreeId && result.recovery?.phase === "restored") {
        setWorktreeId(result.worktreeId);
        setMessage("Restored to a separate workspace. Recovery branches are still retained.");
      } else {
        setMessage(result.recovery?.lastError || "Restore held. Recovery branches and any partial destination were retained; resolve the issue and retry on the same machine.");
      }
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Restore failed; recovery retained. Retry the same operation.");
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  }

  async function remove() {
    if (!canDelete || !confirmDelete || inFlight.current) return;
    pending.current = operation?.kind === "deletion_requested" ? operation :
      { kind: "deletion_requested", key: crypto.randomUUID(), target: "" };
    inFlight.current = true;
    setBusy(true);
    setMessage("");
    try {
      const result = await deleteWorktreeRecovery(recovery.recoveryId, pending.current.key);
      setResultPhase(result.recovery?.phase || "held");
      setMessage(result.recovery?.phase === "refs_deleted"
        ? "Exact recovery branches deleted. Git hosting may retain other copies; this is not guaranteed erasure."
        : result.recovery?.lastError || "Deletion held or partially completed; retry deletes only the outstanding exact branches.");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Deletion failed; catalog retained. Retry the same operation.");
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  }

  return <div className="w-full max-w-xl text-xs">
    <button type="button" aria-expanded={expanded} aria-controls={panelId} onClick={() => setExpanded(!expanded)} className={buttonClass}>Recovery actions</button>
    {expanded && <div id={panelId} aria-busy={busy} className="mt-2 space-y-2 rounded border border-border/60 bg-background p-3">
      <p>{recovery.checkoutCount} captured repositories · {phase}</p>
      <p className="text-muted-foreground">Restore verifies the saved data, then creates a separate workspace and branch at the saved HEAD, preserving captured staged, unstaged and untracked files. It never overwrites existing workspaces or branches.</p>
      <p className="text-muted-foreground">Recovery branches are visible to repository collaborators (public on public repositories). They have no automatic expiry. Dismissing this notice does not delete them.</p>
      {unverified && <p role="status">Preservation is not complete. Do not treat this catalog entry as a verified backup; original files must be kept.</p>}
      {phase === "held" && <p role="status">Recovery is held, not confirmed complete. Original files or partial destinations may still be present.</p>}
      {recovery.lastError && <p className="break-words text-destructive">Last reported issue: {recovery.lastError}</p>}
      {!deleted && !restored && <>
        <p className="text-muted-foreground">Choose a connected machine with this project installed and its registered repositories available. The original machine is not required. Current GitHub access is required; reconnect GitHub if access is refused.</p>
        <label className="block">Destination machine
          <select value={destination} disabled={busy || !!operation || unverified} onChange={event => setTarget(event.target.value)} className="mt-1 block w-full rounded border border-border bg-background px-2 py-1 text-foreground focus-visible:ring-2 focus-visible:ring-ring">
            <option value="">Select a connected machine</option>
            {destination && !targetOnline && <option value={destination}>{destination} (not connected)</option>}
            {available.map(machine => <option key={machine.daemonId} value={machine.daemonId}>{machine.name || machine.hostname || machine.daemonId}</option>)}
          </select>
        </label>
        {machines.isPending && <p role="status">Loading machines…</p>}
        {machines.isError && <p role="alert">Could not list machines. Recovery remains available. <button type="button" className={buttonClass} onClick={() => void machines.refetch()}>Retry machine list</button></p>}
        {!machines.isPending && !machines.isError && available.length === 0 && <p>Connect a machine and install this project before restoring.</p>}
        {operation?.kind === "restoring" && <p>Retry stays bound to this destination, including after reopening the Inbox. Reconnect that machine to continue; no new destination will be created.</p>}
        <button type="button" disabled={!canRestore} onClick={() => void restore()} className={buttonClass}>{busy && operation?.kind === "restoring" ? "Restoring…" : operation?.kind === "restoring" ? "Retry restore" : "Restore separately"}</button>
      </>}
      {worktreeId && <p>Recovered workspace: <code>{worktreeId}</code>. Select it in the project workspace picker.</p>}
      {restored && !worktreeId && <p>Restore completed. Find the recovered workspace in the project workspace picker. Recovery branches are still retained.</p>}
      {!deleted && (!confirmDelete ? <button type="button" disabled={!canDelete} onClick={() => setConfirmDelete(true)} className={`${buttonClass} text-destructive`}>{operation?.kind === "deletion_requested" ? "Retry recovery deletion…" : "Delete recovery branches…"}</button> : <div className="space-y-2 border-t border-border pt-2">
        <p>This permanently deletes only the exact saved recovery branches, and may lose the last recoverable copy of this work. It does not delete restored workspaces and is not guaranteed erasure from Git hosting. A connected machine and current GitHub access are required.</p>
        <div className="flex flex-wrap gap-2">
          <button type="button" disabled={!canDelete} onClick={() => void remove()} className={`${buttonClass} border-destructive text-destructive`}>{busy ? "Deleting…" : "Confirm permanent recovery deletion"}</button>
          <button type="button" disabled={busy} onClick={() => setConfirmDelete(false)} className={buttonClass}>Cancel</button>
        </div>
      </div>)}
      {operation?.kind === "deletion_requested" && !deleted && <p>Deletion is pending or partially complete. Restore is unavailable; retry the outstanding exact branch deletions.</p>}
      {deleted && !message && <p>Recovery branches were explicitly deleted. Other copies on Git hosting are not guaranteed erased.</p>}
      <p role="status" aria-live="polite" className="break-words">{message}</p>
    </div>}
  </div>;
}
