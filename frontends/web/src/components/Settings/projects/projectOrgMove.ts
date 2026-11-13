// Copyright (c) 2025 Reliant Labs

/**
 * The words and decisions behind moving a project to another organization
 * (control-plane docs/design/org-project-move.md §4): which orgs it can go to,
 * what the preview means in sentences, and when a move has settled. Kept apart
 * from MoveProjectOrgDialog so each rule is tested without rendering.
 *
 * Every count comes from the server's preview. This module only puts it into
 * words; it never derives a number.
 */
import { ChatActivity } from "@/gen/reliant/v1/chat_pb";
import {
  ProjectOrgMoveState,
  type ProjectOrgMoveBlockers,
  type ProjectOrgMoveEffects,
} from "@/gen/reliant/v1/project_pb";
import type { MyOrg } from "@/services/controlPlane/orgs";

/** The orgs a project in `fromOrgId` can move to: every other org the caller belongs to. */
export function moveTargets(orgs: MyOrg[], fromOrgId: string | undefined): MyOrg[] {
  return orgs.filter((o) => o.id !== fromOrgId);
}

/**
 * Whether a move has stopped changing, as far as the dialog cares: MOVED means
 * the project is already in the target organization (the last steps, its
 * activity events and turning triggers back on, finish in the background).
 */
export function isMoveSettled(state: ProjectOrgMoveState): boolean {
  return (
    state === ProjectOrgMoveState.MOVED ||
    state === ProjectOrgMoveState.COMPLETED ||
    state === ProjectOrgMoveState.ABORTED
  );
}

/** Whether any running work refuses the move now. */
export function hasBlockers(b: ProjectOrgMoveBlockers | undefined): boolean {
  return !!b && (b.chatsTotal > 0 || b.liveRuns > 0 || b.backgroundProcesses > 0);
}

/** How a blocking chat's state reads in the list. */
export function activityLabel(activity: ChatActivity): string {
  switch (activity) {
    case ChatActivity.RUNNING:
      return "Running";
    case ChatActivity.AWAITING_INPUT:
      return "Waiting for you";
    case ChatActivity.PAUSED:
      return "Paused";
    case ChatActivity.WAITING_FOR_DAEMON:
      return "Waiting for its machine";
    case ChatActivity.QUEUED_FOR_MACHINE:
      return "Queued for a machine";
    case ChatActivity.WAITING_FOR_USAGE_LIMIT:
      return "Waiting out a usage limit";
    default:
      return "Active";
  }
}

function plural(n: number, one: string, many = `${one}s`): string {
  return `${n.toLocaleString()} ${n === 1 ? one : many}`;
}

/**
 * The running work, in one sentence: "2 chats and 1 background process are
 * still running." "" when nothing blocks.
 */
export function blockerSentence(b: ProjectOrgMoveBlockers | undefined): string {
  if (!b || !hasBlockers(b)) return "";
  const parts: string[] = [];
  if (b.chatsTotal > 0) parts.push(plural(b.chatsTotal, "chat"));
  // Runs belong to chats; name them only when no chat accounts for them.
  if (b.chatsTotal === 0 && b.liveRuns > 0) parts.push(plural(b.liveRuns, "run"));
  if (b.backgroundProcesses > 0) parts.push(plural(b.backgroundProcesses, "background process", "background processes"));
  const total = (b.chatsTotal > 0 ? b.chatsTotal : b.liveRuns) + b.backgroundProcesses;
  return `${parts.join(" and ")} ${total === 1 ? "is" : "are"} still active. A project moves only when nothing in it is running.`;
}

/**
 * What the move does, one sentence each, in the order a reader weighs them:
 * what goes, what stops, what is lost, what stays behind.
 */
export function effectLines(e: ProjectOrgMoveEffects | undefined, fromName: string, toName: string): string[] {
  if (!e) return [];
  const lines: string[] = [];
  lines.push(
    `${plural(e.chats, "chat")} and ${plural(e.runs, "run")} move with it, with their whole history. Past usage stays billed to ${fromName}; the next run bills ${toName}.`,
  );
  if (e.triggers > 0) {
    lines.push(
      e.triggersToPause > 0
        ? `${plural(e.triggers, "automation")} move with it. ${plural(e.triggersToPause, "enabled automation")} ${e.triggersToPause === 1 ? "is" : "are"} paused for the move and turned back on in ${toName} once ${e.triggersToPause === 1 ? "it checks" : "they check"} out there.`
        : `${plural(e.triggers, "automation")} move with it.`,
    );
  }
  if (e.triggersNeedingSetup.length > 0) {
    const names = e.triggersNeedingSetup.map((t) => t.name || t.triggerId).join(", ");
    lines.push(`These stay paused until you set them up again in ${toName}: ${names}.`);
  }
  if (e.orgBindingsToClear > 0) {
    lines.push(
      `${plural(e.orgBindingsToClear, "automation")} ${e.orgBindingsToClear === 1 ? "uses" : "use"} a connection of ${fromName}; that link is removed, never carried over.`,
    );
  }
  if (e.sharesToClear > 0) {
    lines.push(
      `The project and chats shared with ${fromName} (${e.sharesToClear.toLocaleString()}) become private; people there lose access. Share them again in ${toName}.`,
    );
  }
  if (e.secretAccessToRevoke !== undefined && e.secretAccessToRevoke > 0) {
    lines.push(`${plural(e.secretAccessToRevoke, "secret-access entry", "secret-access entries")} of ${fromName} for this project ${e.secretAccessToRevoke === 1 ? "is" : "are"} revoked.`);
  }
  if (e.forgeProjectName) {
    // Control-plane says whether the source hosts a deployment of that name;
    // without its answer, say what happens if it does.
    const staying = e.hostedProjectsStaying.includes(e.forgeProjectName);
    lines.push(
      !e.controlPlaneEffectsKnown
        ? `It deploys as ${e.forgeProjectName}: a hosted deployment of that name stays in ${fromName}, and the next deploy goes to ${toName}.`
        : staying
          ? `It deploys as ${e.forgeProjectName}: its hosted deployment stays in ${fromName}, and the next deploy goes to ${toName}.`
          : `It deploys as ${e.forgeProjectName}, which ${fromName} does not host; the next deploy goes to ${toName}.`,
    );
  }
  if (e.machines.length > 0) {
    const elsewhere = e.machines.filter((m) => m.inTargetOrg === false).length;
    lines.push(
      elsewhere > 0
        ? `Nothing about its ${plural(e.machines.length, "machine")} changes; ${plural(elsewhere, "machine")} outside ${toName} keep${elsewhere === 1 ? "s" : ""} billing compute where ${elsewhere === 1 ? "it belongs" : "they belong"}.`
        : `Nothing about its ${plural(e.machines.length, "machine")} changes.`,
    );
  }
  if (e.targetFunded === false) {
    lines.push(`${toName} has no plan or balance yet, so the project's next run there won't start until it does.`);
  }
  return lines;
}

/** "Moved web to Acme." with what is still finishing, for the toast. */
export function movedSentence(projectName: string, toName: string, state: ProjectOrgMoveState): string {
  return state === ProjectOrgMoveState.COMPLETED
    ? `Moved ${projectName} to ${toName}`
    : `Moved ${projectName} to ${toName}; its automations are being turned back on`;
}
