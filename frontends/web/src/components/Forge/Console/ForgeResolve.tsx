// Copyright (c) 2025 Reliant Labs

/**
 * The way INTO the console from anywhere that does not name a forge project:
 * the bare `/forge` (the app sidebar's entry) and every retired URL —
 * `/forge/env/:env?tab=…`, `/forge/observability?…`, `/forge/domains`, and the
 * older per-command paths.
 *
 * Console URLs are addressed by the FORGE project name (paths.ts). These
 * entries name a Reliant project id, or nothing, so this finds the name and
 * replaces the URL with its console page:
 *
 *   0. `forgeProject` param   the name itself — a link the control plane
 *                             wrote (a queued deploy's action URL). Nothing to
 *                             look up, and no Reliant project is needed: the
 *                             console reads the control plane by it.
 *   a. `project` param        that Reliant project (a pasted or bookmarked
 *                             URL, an old link).
 *   b. the current project    what the app is looking at.
 *   c. the last project       restoreLastProject — the same rung
 *                             useWorkspaceRestore uses, since ModernApp, which
 *                             normally restores it, never mounts here.
 *   d. none of those          a picker. A picker, never a dead-end sentence.
 *
 * A Reliant project's forge name is the one persisted on its row, or — for a
 * project no daemon has reported on yet — what its daemon's topology says
 * now. A project with neither is not a forge project (or its daemon cannot
 * say), and the screen says so beside the picker.
 *
 * Every hop is `replace`, so Back does not bounce through the retired URL.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "@tanstack/react-router";

import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { useForgeTopology } from "@/hooks/forge-queries";
import { useProjectStore, type Project } from "@/store/projectStore";
import { fetchProjects, useProject } from "@/hooks/project-queries";

import { ForgeProjectPicker } from "../ForgeProjectPicker";

export interface ForgeResolveProps {
  /** A Reliant project id, from the URL. */
  project?: string;
  /** A forge project name, from a control-plane link. */
  forgeProject?: string;
  /** The console URL for a forge project name. */
  target: (forgeProject: string) => string;
}

type Ladder = { state: "resolving" } | { state: "project"; projectId: string } | { state: "none" };

export function ForgeResolve({ project, forgeProject, target }: ForgeResolveProps) {
  const named = forgeProject?.trim() || undefined;
  const restoreLastProject = useProjectStore((state) => state.restoreLastProject);
  const [ladder, setLadder] = useState<Ladder>({ state: "resolving" });
  const attempted = useRef(false);

  useEffect(() => {
    if (named || attempted.current) return;
    attempted.current = true;
    void (async () => {
      try {
        if (project) {
          const row = (await fetchProjects()).find((candidate) => candidate.id === project);
          if (row) {
            setLadder({ state: "project", projectId: row.id });
            return;
          }
          // A stale id (deleted, or another account's): fall through rather than dead-end on it.
        }
        const current = useProjectStore.getState().currentProject;
        if (current) {
          setLadder({ state: "project", projectId: current.id });
          return;
        }
        if (await restoreLastProject()) {
          const restored = useProjectStore.getState().currentProject;
          if (restored) {
            setLadder({ state: "project", projectId: restored.id });
            return;
          }
        }
        setLadder({ state: "none" });
      } catch {
        setLadder({ state: "none" });
      }
    })();
  }, [named, project, restoreLastProject]);

  // The picker has already selected it in the store.
  const onPicked = useCallback((picked: Project) => setLadder({ state: "project", projectId: picked.id }), []);

  if (named) return <Redirect href={target(named)} />;
  if (ladder.state === "project") {
    return <ResolveName key={ladder.projectId} projectId={ladder.projectId} target={target} onPicked={onPicked} />;
  }
  if (ladder.state === "none") return <PickerScreen onPicked={onPicked} />;
  return <Resolving />;
}

/** The forge name of one Reliant project: persisted, else its daemon's answer. */
function ResolveName({
  projectId,
  target,
  onPicked,
}: {
  projectId: string;
  target: (forgeProject: string) => string;
  onPicked: (project: Project) => void;
}) {
  const current = useProjectStore((state) => (state.currentProject?.id === projectId ? state.currentProject : null));
  const listed = useProject(projectId);
  const row = current ?? listed ?? null;
  const persisted = (row?.forge_project_name ?? "").trim();
  const topology = useForgeTopology(projectId, { enabled: !persisted });
  const reported = topology.data?.kind === "report" ? (topology.data.report.project ?? "").trim() : "";
  const name = persisted || reported;

  // The server persists the name the daemon just reported; re-read the rows
  // so the console's own lookup (ConsoleLayout) finds it there.
  useEffect(() => {
    if (!persisted && reported) void fetchProjects({ fresh: true }).catch(() => undefined);
  }, [persisted, reported]);

  if (name) return <Redirect href={target(name)} />;
  if (topology.isLoading) return <Resolving />;
  return (
    <PickerScreen
      onPicked={onPicked}
      note={
        <>
          <span className="font-medium text-ink">{row?.name ?? "This project"}</span> has no forge project Reliant
          knows of
          {topology.data?.kind === "not-forge-project"
            ? ": there is no forge.yaml in it."
            : topology.error
              ? ", and its daemon could not be asked."
              : "."}{" "}
          Choose another, or deploy this one with forge to see it here.
        </>
      }
    />
  );
}

function Redirect({ href }: { href: string }) {
  const navigate = useNavigate();
  useEffect(() => {
    void navigate({ href, replace: true });
  }, [href, navigate]);
  return <Resolving />;
}

function Resolving() {
  return (
    <div className="forge-ui flex h-screen w-full items-start justify-center bg-surface-muted px-8 pt-24" data-testid="forge-resolving" aria-busy="true">
      <div className="w-full max-w-md space-y-3">
        <SkeletonLoader variant="text" count={2} />
      </div>
    </div>
  );
}

function PickerScreen({ onPicked, note }: { onPicked: (project: Project) => void; note?: React.ReactNode }) {
  return (
    <div className="forge-ui flex h-screen w-full flex-col items-center gap-4 overflow-y-auto bg-surface-muted px-8 pt-24">
      {note && (
        <p className="max-w-md text-sm text-ink-muted" data-testid="forge-resolve-note">
          {note}
        </p>
      )}
      <ForgeProjectPicker onSelected={onPicked} />
    </div>
  );
}
