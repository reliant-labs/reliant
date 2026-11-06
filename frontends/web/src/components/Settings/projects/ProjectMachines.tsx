import { useState } from "react";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useListProjectDaemons, useDetachProjectFromDaemon, usePullProjectOnDaemon, useRecloneProjectOnDaemon, useRemoveProjectFromDaemon } from "@/hooks/project-service-hooks_gen";
import { createProjectFromRepo, type Project } from "@/hooks/project-queries";
import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { daemonStatusLabel } from "@/components/Automations/daemonChoices";
import { machineDisplayName } from "@/lib/machineName";
import { AttachProjectFolderDialog } from "@/components/Projects/AttachProjectFolderDialog";
import { MachineSelect } from "@/components/Projects/MachineSelect";
import { describeMachineFolderError } from "@/components/Projects/machineFolderErrors";
import Modal from "@/components/forge-ui/modal";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";

/** Machine rows are the checkout paths; the project's original path isn't a target on another disk. */
export function ProjectMachines({ project }: { project: Project }) {
  const { daemons } = useDaemonStatus();
  const rows = useListProjectDaemons({}, {
    refetchInterval: (query) => query.state.data?.projectDaemons.some((row) => row.installState === ProjectInstallState.INSTALLING) ? 5000 : false,
  });
  const detach = useDetachProjectFromDaemon({ meta: { silenceErrorToast: true } });
  const pull = usePullProjectOnDaemon({ meta: { silenceErrorToast: true } });
  const reclone = useRecloneProjectOnDaemon({ meta: { silenceErrorToast: true } });
  const remove = useRemoveProjectFromDaemon({ meta: { silenceErrorToast: true } });
  const [attaching, setAttaching] = useState(false);
  const [cloning, setCloning] = useState(false);
  const [target, setTarget] = useState<string | null>(null);
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [confirmation, setConfirmation] = useState<{ daemonId: string; name: string; path: string; action: "remove" | "reclone" } | null>(null);
  const installs = (rows.data?.projectDaemons ?? []).filter((row) => row.projectId === project.id);
  const available = daemons.filter((daemon) => !installs.some((row) => row.daemonId === daemon.daemonId));
  const run = async (action: () => Promise<unknown>, success: string, machine: string) => {
    setBusy(true); setError(null); setNotice(null);
    try { await action(); setNotice(success); setConfirmation(null); }
    catch (err) { setError(describeMachineFolderError(err, machine)); setConfirmation(null); }
    finally { setBusy(false); }
  };
  const button = "rounded-md border border-border px-2 py-1 text-xs hover:bg-muted disabled:opacity-50";
  return (
    <section aria-label={`${project.name} machines`} className="rounded-lg border border-border bg-background p-4 space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-semibold">{project.name} · Machines</h3>
        <div className="flex gap-2">
          <button className={button} onClick={() => setAttaching(true)}>Add machine · use a folder</button>
          {project.remote_url && <button className={button} onClick={() => { setCloning(true); setTarget(available[0]?.daemonId ?? null); setPath(""); }}>Clone onto a machine…</button>}
        </div>
      </div>
      {rows.isLoading && <p role="status" className="text-sm text-muted-foreground">Loading machines…</p>}
      {rows.error && <p role="alert">Could not load machines. <button onClick={() => void rows.refetch()}>Try again</button></p>}
      {!rows.isLoading && !rows.error && installs.length === 0 && <p className="text-sm text-muted-foreground">No recorded machines. Add a machine to choose its folder.</p>}
      {installs.map((row) => {
        const daemon = daemons.find((d) => d.daemonId === row.daemonId);
        const name = row.daemonName || machineDisplayName(daemon ?? { daemonId: row.daemonId });
        const installing = row.installState === ProjectInstallState.INSTALLING;
        return <div key={row.daemonId} className="border-t border-border/60 pt-3 space-y-1">
          <p className="text-sm font-medium">{name} <span className="font-normal text-muted-foreground">· {daemon ? daemonStatusLabel(daemon.status) : "status unknown"} · {installing ? "Installing" : row.installState === ProjectInstallState.FAILED ? "Failed" : "Installed"}</span></p>
          <p className="break-all font-mono text-xs text-muted-foreground">{row.path}</p>
          <p className="text-xs text-muted-foreground">Last verified: {row.verifiedAt ? new Date(row.verifiedAt).toLocaleString() : "Not yet verified"}</p>
          {row.installError && <p role="alert" className="text-sm text-destructive-ink">{row.installError}</p>}
          <div className="flex flex-wrap gap-2 pt-1">
            {project.remote_url && <><button className={button} disabled={busy || installing} onClick={() => void run(() => pull.mutateAsync({ projectId: project.id, daemonId: row.daemonId }), `Pulled on ${name}`, name)}>Pull</button>
              <button className={button} disabled={busy || installing} onClick={() => setConfirmation({ daemonId: row.daemonId, name, path: row.path, action: "reclone" })}>Reclone…</button></>}
            <button className={button} disabled={busy || installing || installs.length <= 1} title={installs.length <= 1 ? "The project's only machine cannot be detached" : "Keeps files on disk"} onClick={() => void run(() => detach.mutateAsync({ projectId: project.id, daemonId: row.daemonId }), `Detached from ${name}; files kept`, name)}>Detach (keep files)</button>
            <button className={`${button} text-destructive-ink`} disabled={busy || installing} onClick={() => setConfirmation({ daemonId: row.daemonId, name, path: row.path, action: "remove" })}>Remove from machine (delete files)…</button>
          </div>
        </div>;
      })}
      {notice && <p role="status" className="text-sm text-success-ink">{notice}</p>}
      {error && <p role="alert" className="text-sm text-destructive-ink">{error}</p>}
      {attaching && <AttachProjectFolderDialog open project={project} onClose={() => setAttaching(false)} />}
      {cloning && <Modal open onClose={() => { if (!busy) setCloning(false); }} title={`Clone ${project.name} onto a machine`}>
        <div className="space-y-3">
          <MachineSelect machines={available} value={target} onChange={setTarget} />
          {!available.length && <p>Every machine already has this project.</p>}
          <label className="block text-sm">Destination folder (optional)
            <input aria-label="Clone destination folder" className="mt-1 w-full rounded-md border border-input bg-background p-2" value={path} onChange={(event) => setPath(event.target.value)} placeholder="Machine's default projects directory" />
          </label>
          <p className="text-xs text-muted-foreground">An offline machine clones when it is ready.</p>
          {error && <p role="alert" className="text-destructive-ink">{error}</p>}
          <button className={button} disabled={!target || busy} onClick={() => void run(async () => {
            await createProjectFromRepo({ cloneUrl: project.remote_url!, daemonId: target!, name: project.name, ...(path.trim() ? { path: path.trim() } : {}) }); setCloning(false);
          }, "Clone queued", "the selected machine")}>Queue clone</button>
        </div>
      </Modal>}
      <ConfirmationDialog open={!!confirmation} variant="danger" title={confirmation?.action === "reclone" ? "Delete files and reclone?" : "Delete this project's folder?"}
        description={confirmation ? `${confirmation.path} on ${confirmation.name} will be deleted, including local changes. ${confirmation.action === "reclone" ? "A fresh clone will replace it." : "This cannot be undone. Use Detach instead to keep the files."}` : ""}
        confirmLabel={confirmation?.action === "reclone" ? "Delete and reclone" : "Delete folder"} loading={busy}
        onCancel={() => { if (!busy) setConfirmation(null); }} onConfirm={() => { if (!confirmation) return; const c = confirmation; void run(() => (c.action === "reclone" ? reclone : remove).mutateAsync({ projectId: project.id, daemonId: c.daemonId }), c.action === "reclone" ? "Reclone requested" : "Folder removed", c.name); }} />
    </section>
  );
}
