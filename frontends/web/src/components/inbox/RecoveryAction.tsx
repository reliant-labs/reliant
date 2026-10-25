import { useRef, useState } from "react";
import type { RecoveryInboxItem } from "@/gen/reliant/v1/inbox_pb";
import { useDaemonList } from "@/hooks/useOnboardingQueries";
import { deleteWorktreeRecovery, restoreWorktreeRecovery } from "@/hooks/worktree-actions";

/** Recovery branches inherit repository visibility; dismissal never deletes them. */
export function RecoveryAction({ recovery }: { recovery: RecoveryInboxItem }) {
  const machines = useDaemonList();
  const [expanded, setExpanded] = useState(false);
  const [target, setTarget] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [worktreeId, setWorktreeId] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const restoreKey = useRef<string | null>(null);
  const deleteKey = useRef<string | null>(null);
  const boundTarget = useRef<string | null>(null);
  async function restore() {
    if (!target) return;
    restoreKey.current ??= crypto.randomUUID();
    boundTarget.current ??= target;
    setBusy(true); setMessage("");
    try {
      const result = await restoreWorktreeRecovery(recovery.recoveryId, boundTarget.current, restoreKey.current);
      if (result.worktreeId && result.recovery?.phase === "restored") {
        setWorktreeId(result.worktreeId);
        setMessage("Restored to a separate workspace. Recovery branches are still retained.");
      } else setMessage(result.recovery?.lastError || "Restore held. Recovery branches and any partial destination were retained; retry this operation after resolving the issue.");
    } catch (error) { setMessage(error instanceof Error ? error.message : "Restore failed; recovery retained."); }
    finally { setBusy(false); }
  }
  async function remove() {
    deleteKey.current ??= crypto.randomUUID(); setBusy(true); setMessage("");
    try {
      const result = await deleteWorktreeRecovery(recovery.recoveryId, deleteKey.current);
      setMessage(result.recovery?.phase === "refs_deleted" ? "Exact recovery branches deleted. Git hosting may retain other copies; this is not guaranteed erasure." : result.recovery?.lastError || "Deletion held or partially completed; retry deletes only the outstanding exact branches.");
    } catch (error) { setMessage(error instanceof Error ? error.message : "Deletion failed; catalog retained."); }
    finally { setBusy(false); }
  }
  return <div className="max-w-xl text-xs">
    <button type="button" onClick={() => setExpanded(!expanded)} className="rounded px-2 py-1 text-primary hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring">Recovery actions</button>
    {expanded && <div className="mt-2 space-y-2 rounded border border-border/60 bg-background p-3">
      <p>{recovery.checkoutCount} captured repositories · {recovery.phase}</p>
      <p className="text-muted-foreground">Restore creates a new workspace and branch at the saved HEAD, preserving staged, unstaged and untracked files. Install this project on the selected machine first.</p>
      <p className="text-muted-foreground">Recovery branches are visible to repository collaborators (public on public repositories). They have no automatic expiry.</p>
      {recovery.lastError && <p role="status" className="text-destructive">Held: {recovery.lastError}</p>}
      <label className="block">Destination machine
        <select aria-label="Destination machine" value={target} disabled={busy || boundTarget.current !== null} onChange={event => setTarget(event.target.value)} className="ml-2 rounded border border-border bg-background px-2 py-1 text-foreground">
          <option value="">Select a machine</option>
          {(machines.data ?? []).map(machine => <option key={machine.daemonId} value={machine.daemonId}>{machine.name || machine.hostname || machine.daemonId}</option>)}
        </select>
      </label>
      {machines.isError && <p role="alert">Could not list machines. Recovery remains available.</p>}
      <button type="button" disabled={busy || !target} onClick={() => void restore()} className="rounded border border-border px-2 py-1 hover:bg-muted disabled:opacity-50">{busy ? "Working…" : restoreKey.current ? "Retry restore" : "Restore separately"}</button>
      {worktreeId && <p>Recovered workspace: <code>{worktreeId}</code>. Select it in the project workspace picker.</p>}
      {!confirmDelete ? <button type="button" disabled={busy} onClick={() => setConfirmDelete(true)} className="ml-2 text-destructive">Delete recovery branches…</button> : <div className="space-y-2 border-t border-border pt-2">
        <p>This permanently deletes only the exact saved recovery branches, and may lose the last recoverable copy of this work. It does not delete restored workspaces and is not guaranteed erasure from Git hosting.</p>
        <button type="button" disabled={busy} onClick={() => void remove()} className="rounded border border-destructive px-2 py-1 text-destructive">Confirm permanent recovery deletion</button>
        <button type="button" disabled={busy} onClick={() => setConfirmDelete(false)} className="ml-2">Cancel</button>
      </div>}
      {message && <p role="status">{message}</p>}
    </div>}
  </div>;
}
