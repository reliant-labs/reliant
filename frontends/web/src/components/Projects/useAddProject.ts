// Copyright (c) 2025 Reliant Labs

/**
 * The two ways to add a project — clone a repository, open a folder — as one
 * hook, so the Add project dialog and the first-run state offer exactly the
 * same actions with the same availability rules.
 *
 * Adding a project opens it: the new project becomes the context of a fresh
 * new-chat view (openNewChat). If the sidebar is filtered to another project,
 * the filter moves to the new one, so the project just added is the one the
 * list shows.
 */

import { useCallback, useMemo, useState } from "react";
import { defaultMachineDaemon } from "../../lib/chatMachine";
import { useCurrentMachineStore } from "../../store/currentMachineStore";
import { useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { FolderOpen, GitFork } from "lucide-react";

import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { useListProjectDaemons } from "../../hooks/project-service-hooks_gen";
import { invalidateDaemonList } from "../../hooks/daemon-registry-queries";
import { useGitHubCredential } from "../../hooks/useGitHubCredential";
import { createProjectFromRepo, createProjectWithResult, fetchProjects, type Project } from "../../hooks/project-queries";
import { machineDisplayName } from "../../lib/machineName";
import { markMachineGone, useGoneMachinesVersion, withoutGoneMachines } from "../../lib/goneMachines";
import { basename } from "../../lib/pathUtils";
import { toast } from "../../lib/toast-manager";
import { cloudPathForRepo, repoNameFromUrl } from "../../lib/cloudProjectPath";
import { capabilities } from "../../services/controlPlane/capabilities";
import type { GitRepo } from "../../services/controlPlane/git";
import { openNewChat } from "../../store/openChat";
import { useProjectStore } from "../../store/projectStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { addProjectLead } from "./addProjectActions";
import type { AddProjectAction } from "./picker/addProjectActionModel";
import { isCloudDaemon } from "./picker/format";
import {
  cloneAvailability,
  cloneDescription,
  describeCloneError,
  isCloneTargetGone,
  pickCloneTarget,
} from "./cloneTargets";
import { CLONE_REPOSITORY_LABEL, OPEN_FOLDER_LABEL } from "./projectCopy";

// The server classifies a duplicate path as AlreadyExists; the code is the
// signal, not the message.
function isAlreadyExistsError(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.AlreadyExists;
}

/**
 * Open a project that was just added (or that already existed), with a new
 * chat on `daemonId` when one is named: the machine the folder or clone is
 * on. Without it, a folder that attached to a project already installed on
 * the default machine would open a chat there instead.
 */
export async function openAddedProject(project: Pick<Project, "id">, daemonId?: string): Promise<void> {
  const filter = useWorkspaceStateStore.getState().projectFilterId;
  if (filter && filter !== project.id) {
    await useProjectStore.getState().setProjectFilter(project.id);
  }
  await openNewChat({ projectId: project.id, daemonId });
}

interface UseAddProjectOptions {
  /** Called once a project was added and opened (close a dialog). */
  onAdded?: (project: Project) => void;
  /** Called when the user picks Clone a repository (show the repo list). */
  onChooseClone: () => void;
  /** Called when the browser must show the in-app folder browser. */
  onChooseFolderInBrowser: () => void;
}

export function useAddProject({ onAdded, onChooseClone, onChooseFolderInBrowser }: UseAddProjectOptions) {
  const queryClient = useQueryClient();
  const { refetch: refetchProjectMachines } = useListProjectDaemons({});
  const { daemons, activeDaemon, loading: daemonLoading } = useDaemonStatus();
  const currentMachineId = useCurrentMachineStore((state) => state.daemonId);
  const folderDaemonId = currentMachineId ?? defaultMachineDaemon(daemons)?.daemonId;
  // Offline machines remain selectable: browsing explains how to reconnect.
  const machineReachable = daemons.length > 0 || daemonLoading;
  const { hasToken: hasGitHubCredential } = useGitHubCredential();
  const [cloneStatus, setCloneStatus] = useState<string | null>(null);

  // Cloud machines are the only valid clone targets. Machines the control
  // plane has already said are gone this session are never offered again
  // (lib/goneMachines).
  const goneVersion = useGoneMachinesVersion();
  const cloudDaemons = useMemo(
    () => withoutGoneMachines(daemons.filter((d) => isCloudDaemon(d.daemonType))),
    // goneVersion re-runs the filter when a machine is marked gone.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [daemons, goneVersion],
  );
  const machineNameFor = useCallback(
    (daemonId: string) => {
      const d = daemons.find((x) => x.daemonId === daemonId);
      return d ? machineDisplayName(d) : `machine ${daemonId.slice(0, 8)}`;
    },
    [daemons],
  );

  const cloneState = useMemo(() => cloneAvailability(cloudDaemons), [cloudDaemons]);
  const showClone = capabilities.cloudDaemons;
  const cloneLeads =
    showClone &&
    addProjectLead({ hasCloudDaemons: cloudDaemons.length > 0, activeDaemonType: activeDaemon?.daemonType }) ===
      "clone";

  // The machine the next clone uses: the user's explicit choice while it is
  // still on offer, else the most recently used one, else any that will
  // drain the queue.
  const [chosenCloneDaemonId, setChosenCloneDaemonId] = useState<string | null>(null);
  const chosenStillOffered =
    chosenCloneDaemonId !== null && cloudDaemons.some((d) => d.daemonId === chosenCloneDaemonId);
  const effectiveCloneDaemonId =
    (chosenStillOffered ? chosenCloneDaemonId : null) ??
    pickCloneTarget(cloudDaemons)?.daemonId ??
    (cloneState.kind === "ready" ? cloneState.target.daemonId : null);

  const finish = useCallback(
    async (project: Project, daemonId?: string) => {
      await openAddedProject(project, daemonId);
      onAdded?.(project);
    },
    [onAdded],
  );

  // CreateProjectFromRepo owns clone + create + install server-side. It
  // returns once the clone is QUEUED: the machine may still be asleep, so the
  // copy never claims the checkout exists yet.
  const cloneRepo = useCallback(
    async (repo: GitRepo) => {
      const targetDaemonId = effectiveCloneDaemonId;
      if (!targetDaemonId) {
        toast.error(cloneState.kind === "blocked" ? cloneState.reason : "No machine available to clone onto");
        return;
      }
      const projectName = repoNameFromUrl(repo.cloneUrl) || repo.fullName;
      const targetHost = machineNameFor(targetDaemonId);
      setCloneStatus(`Queueing ${projectName} for ${targetHost}…`);
      try {
        const result = await createProjectFromRepo({
          cloneUrl: repo.cloneUrl,
          daemonId: targetDaemonId,
          name: projectName,
          branch: repo.defaultBranch || "main",
          path: cloudPathForRepo(repo),
        });
        if (result.queued) {
          toast.info(`Queued — ${projectName} will clone when ${result.daemonName || targetHost} is ready`);
        }
        if (result.project) {
          const openedId = result.project.id;
          const opened = (await fetchProjects({ fresh: true })).find((p) => p.id === openedId);
          if (opened) await finish(opened, result.projectDaemon?.daemon_id || targetDaemonId);
        }
      } catch (err) {
        if (isCloneTargetGone(err)) {
          // Stop offering a machine the control plane does not have.
          markMachineGone(targetDaemonId);
          setChosenCloneDaemonId(null);
          invalidateDaemonList(queryClient);
        }
        toast.error(describeCloneError(err, machineNameFor(targetDaemonId)));
      } finally {
        setCloneStatus(null);
      }
    },
    [cloneState, effectiveCloneDaemonId, finish, machineNameFor, queryClient],
  );

  // Register (or reopen) a project for a folder the user picked.
  const openFolderPath = useCallback(
    async (selectedPath: string, daemonId: string | undefined = folderDaemonId) => {
      const projectName = basename(selectedPath) || selectedPath || "Untitled project";
      const loadingToast = toast.loading(`Opening ${projectName}…`);
      try {
        const created = await createProjectWithResult({
          name: projectName,
          path: selectedPath,
          description: "",
          default_branch: "main",
          daemon_id: daemonId,
        });
        await finish(created.project, created.projectDaemon?.daemon_id || daemonId);
      } catch (error) {
        if (isAlreadyExistsError(error)) {
          const machines = await refetchProjectMachines();
          const existing = machines.data?.projectDaemons.find((row) => row.daemonId === daemonId && row.path === selectedPath);
          const found = (await fetchProjects({ fresh: true })).find((p) => existing ? p.id === existing.projectId : !daemonId && p.path === selectedPath);
          if (found) {
            toast.success(`Opening ${found.name}, which is already a project`);
            await finish(found, existing?.daemonId || daemonId);
            return;
          }
        }
        // createProject suppresses AlreadyExists for this recovery path.
        if (isAlreadyExistsError(error)) toast.error(error);
      } finally {
        toast.dismiss(loadingToast);
      }
    },
    [finish, folderDaemonId, refetchProjectMachines],
  );

  // Always pick the machine first. DirectoryPicker offers the native dialog
  // only when the desktop app can identify that machine as this computer.
  const chooseFolder = useCallback(() => {
    onChooseFolderInBrowser();
  }, [onChooseFolderInBrowser]);

  // The two methods, the leading one first. Each stays visible and says why
  // when it cannot be used, so there is never a state with no way to add a
  // project.
  const actions: AddProjectAction[] = [];
  if (showClone) {
    actions.push({
      kind: "clone",
      label: CLONE_REPOSITORY_LABEL,
      description: cloneDescription({ cloneState, hasGitHubCredential }),
      icon: GitFork,
      onClick: onChooseClone,
      disabled: cloneState.kind === "blocked",
      lead: cloneLeads,
      testId: "add-project-clone",
    });
  }
  actions.push({
    kind: "open",
    label: OPEN_FOLDER_LABEL,
    description: !machineReachable
      ? "Connect a machine first: a folder is opened on the machine that holds it."
      : "Choose a machine, then browse to a folder or type its path. Reliant works in it where it is.",
    icon: FolderOpen,
    onClick: () => void chooseFolder(),
    disabled: !machineReachable,
    lead: !cloneLeads,
    testId: "add-project-open-folder",
  });
  actions.sort((a, b) => Number(b.lead) - Number(a.lead));

  return {
    actions,
    machineReachable,
    folderDaemonId,
    cloudDaemons,
    effectiveCloneDaemonId,
    setChosenCloneDaemonId,
    cloneRepo,
    openFolderPath,
    cloneStatus,
  };
}
