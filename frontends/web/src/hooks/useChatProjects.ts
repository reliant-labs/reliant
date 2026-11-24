// Copyright (c) 2025 Reliant Labs

/**
 * The projects a chat works in — its primary (Chat.project_id) and the
 * projects attached beside it — read and changed in one place, for every
 * surface that shows them (Chat/ChatProjectChips: the new-chat view, the
 * composer; mobile reuses both).
 *
 *   useChatProjects(chat)            an existing chat: ListChatProjects,
 *                                    AttachProjectToChat, DetachProjectFromChat
 *   useDraftChatProjects(primaryId)  a new chat's draft: local state
 *                                    (store/newChatProjectsStore), sent as
 *                                    StartChatRequest.attached_project_ids
 *
 * Both answer the same ChatProjectsControl, so a surface renders either the
 * same way.
 *
 * One machine first: every attached project must be set up on the chat's
 * machine. A project the picker knows is not there is set up on it first
 * (the clone ProjectCheckoutMissingPanel runs) and attached once it lands. A
 * project the server says is not there — FailedPrecondition carrying
 * ProjectCheckoutMissing — becomes an offer to set it up (`setUp`), and is
 * attached once the person accepts and the clone lands.
 *
 * An older server does not serve these rpcs (Unimplemented): the chat then
 * shows its primary project only, with no error and no add control
 * (`supported` false), and the transport does not take it for version skew
 * (api/rpcErrorPolicy PROBED_PROCEDURES).
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { create as createMessage } from "@bufbuild/protobuf";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { toast } from "sonner";
import { create } from "zustand";

import { ChatService, ListChatProjectsResponseSchema, type ChatProject } from "@/gen/reliant/v1/chat_pb";
import { ProjectCheckoutState } from "@/gen/reliant/v1/filesystem_pb";
import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { connectClient } from "@/lib/connect";
import { projectCheckoutMissing } from "@/lib/daemon-errors";
import { machineDisplayName } from "@/lib/machineName";
import { canSetUpElsewhere, projectPlacement, setUpPathOn, type InstallRow } from "@/lib/projectPlacement";
import { shouldRetryQuery } from "@/lib/queryRetry";
import { describeCloneError } from "@/components/Projects/cloneTargets";
import {
  MAX_ATTACHED_PROJECTS,
  setNewChatAttachedProjects,
  getNewChatAttachedProjects,
  useNewChatAttachedProjects,
} from "@/store/newChatProjectsStore";
import type { Chat } from "@/types/chat";

import { chatServiceQueries } from "./chat-service-hooks_gen";
import { createProjectFromRepo, useProjectDaemonInstalls, useProjects, type Project } from "./project-queries";
import { useDaemonList } from "./useOnboardingQueries";

const client = connectClient(ChatService);

/** One chip: a project of the chat. */
export interface ChatProjectChipModel {
  projectId: string;
  name: string;
  /** The chat's own project: listed first, never removed from here. */
  primary: boolean;
}

/** A machine, named for the person. */
export interface ChatProjectsMachine {
  daemonId: string;
  name: string;
}

/** A project waiting to be set up on the chat's machine before it is attached. */
export interface ChatProjectSetUp {
  projectId: string;
  projectName: string;
  machine: ChatProjectsMachine;
  /** A repository can be cloned there; a plain folder elsewhere cannot. */
  canClone: boolean;
  /** offer: the person is asked; setting_up: the clone is landing; failed: it did not. */
  state: "offer" | "setting_up" | "failed";
  error?: string;
}

export interface ChatProjectsControl {
  /** The primary first, then the attached projects. */
  projects: ChatProjectChipModel[];
  /** Projects can be attached here (the server serves it, and the chat has a machine). */
  supported: boolean;
  /** The machine attached projects must be on, when known. */
  machine?: ChatProjectsMachine;
  /** Every project_daemons row the person has, for the picker's hints. */
  installs: ReadonlyArray<InstallRow>;
  /** Project ids being attached, set up or removed. */
  pending: ReadonlySet<string>;
  /** Attach a project picked in the project selector (set it up on the machine first when it is not there). */
  add: (project: Project) => void;
  remove: (projectId: string) => void;
  setUp: ChatProjectSetUp | null;
  /** Set the offered project up on the machine; it is attached once it lands. */
  confirmSetUp: () => void;
  dismissSetUp: () => void;
}

// ── Whether the server serves attached projects ─────────────────────────────

/**
 * What the last ListChatProjects / AttachProjectToChat answer said: null
 * before any. A new chat's draft offers attached projects unless a chat's
 * read has already shown the server cannot keep them.
 */
const useSupportStore = create<{ supported: boolean | null }>(() => ({ supported: null }));

function noteSupport(supported: boolean): void {
  if (useSupportStore.getState().supported !== supported) useSupportStore.setState({ supported });
}

/** Tests: forget what a server said. */
export function resetChatProjectsSupportForTests(): void {
  useSupportStore.setState({ supported: null });
}

function isUnimplemented(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.Unimplemented;
}

function errorText(error: unknown): string {
  return error instanceof ConnectError ? error.rawMessage : error instanceof Error ? error.message : String(error);
}

// ── The machine and the set-up ──────────────────────────────────────────────

/** The person's machines (read only while `enabled`), and a name for one. */
function useMachines(enabled: boolean) {
  const { data } = useDaemonList({ enabled, refetchInterval: false });
  const daemons = useMemo(() => data ?? [], [data]);
  const nameOf = useCallback(
    (daemonId: string) => {
      const d = daemons.find((m) => m.daemonId === daemonId);
      return d ? machineDisplayName(d) : "this machine";
    },
    [daemons],
  );
  return { daemons, nameOf };
}

interface SetUpJob extends ChatProjectSetUp {
  cloneUrl: string;
  path: string;
  /** The machine's row still says an EARLIER clone failed: not this one's answer yet. */
  staleFailure?: boolean;
}

/**
 * The set-up of one project on the machine, then `attachNow` once its
 * checkout is there. Shared by the chat and the draft.
 */
function useSetUpThenAttach(enabled: boolean, attachNow: (projectId: string) => void) {
  const [job, setJob] = useState<SetUpJob | null>(null);
  const settingUp = job?.state === "setting_up";
  // While a clone lands, the rows are re-read so the attach follows it.
  const installsQuery = useProjectDaemonInstalls(enabled, settingUp ? 3_000 : false);
  const installs = useMemo(() => installsQuery.data ?? [], [installsQuery.data]);
  const { daemons } = useMachines(enabled);
  const attachRef = useRef(attachNow);
  attachRef.current = attachNow;

  useEffect(() => {
    if (!job || job.state !== "setting_up") return;
    const row = installs.find((r) => r.project_id === job.projectId && r.daemon_id === job.machine.daemonId);
    if (row?.install_state === ProjectInstallState.INSTALLED) {
      setJob(null);
      attachRef.current(job.projectId);
    } else if (row?.install_state === ProjectInstallState.FAILED && !job.staleFailure) {
      setJob({ ...job, state: "failed", error: row.install_error?.trim() || undefined });
    }
  }, [job, installs]);

  const start = useCallback(
    async (next: SetUpJob) => {
      if (!next.canClone) {
        setJob({ ...next, state: "offer" });
        return;
      }
      const prior = installs.find((r) => r.project_id === next.projectId && r.daemon_id === next.machine.daemonId);
      setJob({
        ...next,
        state: "setting_up",
        error: undefined,
        staleFailure: prior?.install_state === ProjectInstallState.FAILED,
      });
      try {
        const path = setUpPathOn({
          installs,
          projectId: next.projectId,
          path: next.path,
          daemonId: next.machine.daemonId,
          platformOf: (id) => daemons.find((d) => d.daemonId === id)?.platform,
        });
        await createProjectFromRepo({
          cloneUrl: next.cloneUrl,
          daemonId: next.machine.daemonId,
          name: next.projectName,
          ...(path ? { path } : {}),
        });
        // Landed already, or on its way: the effect above attaches it. Read
        // after the clone was queued, a failure on the row is this clone's.
        await installsQuery.refetch();
        setJob((current) => (current?.projectId === next.projectId ? { ...current, staleFailure: false } : current));
      } catch (err) {
        setJob({ ...next, state: "failed", error: describeCloneError(err, next.machine.name) });
      }
    },
    [installs, daemons, installsQuery],
  );

  const setUp: ChatProjectSetUp | null = job
    ? {
        projectId: job.projectId,
        projectName: job.projectName,
        machine: job.machine,
        canClone: job.canClone,
        state: job.state,
        ...(job.error ? { error: job.error } : {}),
      }
    : null;

  return {
    installs,
    setUp,
    job,
    offer: (next: SetUpJob) => setJob(next),
    start,
    confirm: () => {
      if (job) void start(job);
    },
    dismiss: () => setJob(null),
  };
}

/** The set-up job for a project the picker knows is not on `machine`. */
function jobFor(project: Project, machine: ChatProjectsMachine): SetUpJob {
  return {
    projectId: project.id,
    projectName: project.name,
    machine,
    canClone: canSetUpElsewhere(project),
    state: "offer",
    cloneUrl: project.remote_url ?? "",
    path: project.path,
  };
}

/**
 * Attach a picked project, or set it up on `machine` first: a project
 * already landing there is waited for, one absent (or failed) there is
 * cloned there. With no record of the project anywhere, or no machine known,
 * the server decides (and offers the set-up when it refuses).
 */
function addOrSetUp(
  project: Project,
  machine: ChatProjectsMachine | undefined,
  flow: ReturnType<typeof useSetUpThenAttach>,
  attachNow: () => void,
): void {
  const placement = projectPlacement(flow.installs, project.id);
  const state = machine && placement.known ? placement.stateOn(machine.daemonId) : "ready";
  if (!machine || state === "ready") attachNow();
  else if (state === "setting_up") flow.offer({ ...jobFor(project, machine), state: "setting_up" });
  else void flow.start(jobFor(project, machine));
}

function tooMany(name: string): void {
  toast.error(`Couldn't add ${name}`, { description: `A chat can have up to ${MAX_ATTACHED_PROJECTS} other projects.` });
}

// ── An existing chat ────────────────────────────────────────────────────────

type ChatLike = Pick<Chat, "id" | "projectId" | "activeDaemonId" | "noMachine" | "viewerAccess">;

/**
 * The chat's projects, and attach / detach. `primaryName` names the primary
 * until (or where) the server cannot list them. A viewer of a shared chat
 * and a chat with no machine read nothing: they show the primary only.
 */
export function useChatProjects(chat: ChatLike | undefined, primaryName?: string): ChatProjectsControl {
  const queryClient = useQueryClient();
  const chatId = chat?.id ?? "";
  const eligible = !!chat && !chat.viewerAccess && !chat.noMachine;
  const options = useMemo(() => chatServiceQueries.listChatProjects({ chatId }), [chatId]);
  const query = useQuery({
    ...options,
    enabled: eligible,
    retry: (count, error) => !isUnimplemented(error) && shouldRetryQuery(count, error),
  });
  const unsupported = isUnimplemented(query.error);
  useEffect(() => {
    if (query.data) noteSupport(true);
    else if (unsupported) noteSupport(false);
  }, [query.data, unsupported]);

  const { nameOf: machineName } = useMachines(eligible);
  const [pending, setPending] = useState<ReadonlySet<string>>(() => new Set());
  const mark = (id: string, on: boolean) =>
    setPending((prev) => {
      const next = new Set(prev);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });

  const list = useMemo((): readonly ChatProject[] => query.data?.projects ?? [], [query.data]);
  const projects = useMemo((): ChatProjectChipModel[] => {
    if (list.length > 0) return list.map((p) => ({ projectId: p.projectId, name: p.name, primary: p.primary }));
    return chat ? [{ projectId: chat.projectId, name: primaryName ?? "", primary: true }] : [];
  }, [list, chat, primaryName]);

  // The machine: the chat's pin, else where the server placed the primary.
  const daemonId = chat?.activeDaemonId || list.find((p) => p.primary)?.daemonId || undefined;
  const machine = daemonId ? { daemonId, name: machineName(daemonId) } : undefined;

  const write = useCallback(
    (projectsNow: ChatProject[]) => {
      queryClient.setQueryData(options.queryKey, createMessage(ListChatProjectsResponseSchema, { projects: projectsNow }));
    },
    [queryClient, options.queryKey],
  );

  const nameOf = (projectId: string, fallback?: string) =>
    fallback ?? list.find((p) => p.projectId === projectId)?.name ?? "the project";

  // Late-bound: the set-up calls it once a clone lands.
  const attachRef = useRef<(projectId: string, name?: string) => Promise<void>>(async () => {});
  const setUpFlow = useSetUpThenAttach(eligible, (projectId) => void attachRef.current(projectId));

  attachRef.current = async (projectId: string, name?: string) => {
    if (!chat) return;
    mark(projectId, true);
    try {
      const response = await client.attachProjectToChat({ chatId: chat.id, projectId });
      noteSupport(true);
      write(response.projects);
    } catch (err) {
      const missing = projectCheckoutMissing(err);
      if (missing) {
        const target = missing.daemonId || daemonId;
        if (target) {
          const job: SetUpJob = {
            projectId: missing.projectId || projectId,
            projectName: nameOf(projectId, name),
            machine: { daemonId: target, name: machineName(target) },
            canClone: Boolean(missing.remoteUrl),
            state: "offer",
            cloneUrl: missing.remoteUrl,
            path: missing.path,
          };
          // A clone already landing there: wait for it rather than ask.
          if (missing.state === ProjectCheckoutState.CLONING) setUpFlow.offer({ ...job, state: "setting_up" });
          else setUpFlow.offer(job);
          return;
        }
      }
      if (isUnimplemented(err)) {
        noteSupport(false);
        return;
      }
      if (err instanceof ConnectError && err.code === Code.ResourceExhausted) {
        tooMany(nameOf(projectId, name));
        return;
      }
      toast.error(`Couldn't add ${nameOf(projectId, name)}`, { description: errorText(err) });
    } finally {
      mark(projectId, false);
    }
  };

  const add = (project: Project) => {
    if (!chat || projects.some((p) => p.projectId === project.id) || pending.has(project.id)) return;
    if (projects.length - 1 >= MAX_ATTACHED_PROJECTS) {
      tooMany(project.name);
      return;
    }
    addOrSetUp(project, machine, setUpFlow, () => void attachRef.current(project.id, project.name));
  };

  const remove = (projectId: string) => {
    if (!chat || pending.has(projectId)) return;
    mark(projectId, true);
    client
      .detachProjectFromChat({ chatId: chat.id, projectId })
      .then((response) => write(response.projects))
      .catch((err: unknown) => {
        if (isUnimplemented(err)) noteSupport(false);
        else toast.error(`Couldn't remove ${nameOf(projectId)}`, { description: errorText(err) });
      })
      .finally(() => mark(projectId, false));
  };

  const settingUpId = setUpFlow.job?.state === "setting_up" ? setUpFlow.job.projectId : null;
  const pendingAll = useMemo(() => {
    if (!settingUpId) return pending;
    return new Set([...pending, settingUpId]);
  }, [pending, settingUpId]);

  return {
    projects,
    supported: eligible && !!query.data && !unsupported,
    machine,
    installs: setUpFlow.installs,
    pending: pendingAll,
    add,
    remove,
    setUp: setUpFlow.setUp,
    confirmSetUp: setUpFlow.confirm,
    dismissSetUp: setUpFlow.dismiss,
  };
}

// ── A new chat's draft ──────────────────────────────────────────────────────

/**
 * The projects a new chat in `primaryId` is started with beside it, for its
 * StartChat: the draft's (store/newChatProjectsStore), unless the chat has no
 * machine or the server has shown it cannot keep them.
 */
export function newChatAttachedProjectIds(primaryId: string | null | undefined, noMachine: boolean): readonly string[] {
  if (noMachine || useSupportStore.getState().supported === false) return [];
  return getNewChatAttachedProjects(primaryId);
}

/**
 * The draft's projects for a new chat in `primaryId`: the primary and the
 * projects attached in the draft (store/newChatProjectsStore). `machine` is
 * where the chat will run, when known; undefined for a chat with no machine
 * (`noMachine`), which cannot have attached projects.
 */
export function useDraftChatProjects(options: {
  primaryId: string | null | undefined;
  machineDaemonId?: string;
  noMachine?: boolean;
}): ChatProjectsControl {
  const { primaryId, machineDaemonId, noMachine = false } = options;
  const attachedIds = useNewChatAttachedProjects(primaryId);
  const { projects: all } = useProjects();
  const serverSupport = useSupportStore((s) => s.supported);
  const { nameOf: machineName } = useMachines(!!primaryId && !noMachine);
  const machine = machineDaemonId ? { daemonId: machineDaemonId, name: machineName(machineDaemonId) } : undefined;

  const addNow = useCallback(
    (projectId: string) => {
      if (!primaryId) return;
      setNewChatAttachedProjects(primaryId, [...getNewChatAttachedProjects(primaryId), projectId]);
    },
    [primaryId],
  );
  const setUpFlow = useSetUpThenAttach(!!primaryId && !noMachine, addNow);

  const projects = useMemo((): ChatProjectChipModel[] => {
    if (!primaryId) return [];
    const nameOf = (id: string) => all.find((p) => p.id === id)?.name ?? "";
    return [
      { projectId: primaryId, name: nameOf(primaryId), primary: true },
      // A project the list no longer holds (deleted, another org's) is not offered.
      ...attachedIds.filter((id) => all.length === 0 || all.some((p) => p.id === id)).map((id) => ({ projectId: id, name: nameOf(id), primary: false })),
    ];
  }, [primaryId, attachedIds, all]);

  const supported = !!primaryId && !noMachine && serverSupport !== false;
  const settingUpId = setUpFlow.job?.state === "setting_up" ? setUpFlow.job.projectId : null;
  const pending = useMemo(() => new Set(settingUpId ? [settingUpId] : []), [settingUpId]);

  return {
    projects: supported ? projects : projects.slice(0, 1),
    supported,
    machine,
    installs: setUpFlow.installs,
    pending,
    add: (project) => {
      if (!primaryId || project.id === primaryId || attachedIds.includes(project.id)) return;
      if (attachedIds.length >= MAX_ATTACHED_PROJECTS) {
        tooMany(project.name);
        return;
      }
      addOrSetUp(project, machine, setUpFlow, () => addNow(project.id));
    },
    remove: (projectId) => {
      if (!primaryId) return;
      setNewChatAttachedProjects(primaryId, getNewChatAttachedProjects(primaryId).filter((id) => id !== projectId));
    },
    setUp: setUpFlow.setUp,
    confirmSetUp: setUpFlow.confirm,
    dismissSetUp: setUpFlow.dismiss,
  };
}
