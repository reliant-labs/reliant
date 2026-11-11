// Copyright (c) 2025 Reliant Labs

/**
 * "Ask Reliant": hand the thing on screen to an agent.
 *
 * One button, four surfaces (an issue, a trace, a firing alert, a dashboard
 * panel). It opens the app's new-chat composer on a fresh workspace with a
 * prompt already in it — and does not send it. The prompt is references and the
 * forge commands to fetch the rest (askReliantPrompt.ts); the reader reads it,
 * edits it and presses send.
 *
 * Issues and alerts get two actions instead (AskReliantActions, or
 * AskReliantMenu in a row): "Investigate in chat", the same unsent prompt
 * asking for the root cause, and "Fix with agent", which sends a prompt asking
 * for the fix at once, on the new workspace's own branch.
 *
 * Which Reliant project: the one that declares the console's forge project
 * (askReliantProject.ts). When that is not decidable the reader is asked, once,
 * with a picker; it never guesses.
 *
 * `onChatCreated` is called with the chat's id when the reader sends — the
 * first moment the chat exists — so a surface can record "chat started from
 * this issue". It fires from the new-chat screen, after this component has
 * unmounted, so it must not close over component state.
 *
 * Where there is no chat to start (the forge shell, a demo on fixture data:
 * AskAgentModeProvider mode="handoff"), every surface's action is one "Ask an
 * agent" button instead, which opens the handoff sheet (AskAgentSheet) and
 * touches neither the daemon nor a project.
 */

import { useCallback, useContext, useId, useRef, useState, type ReactNode } from "react";
import { useRouter } from "@tanstack/react-router";
import { Bot, ChevronDown, Loader2, MessageSquarePlus, MessageSquareText, Rocket, Wrench } from "lucide-react";

import Modal from "@/components/forge-ui/modal";
import Button from "@/components/forge-ui/button";
import DropdownMenu from "@/components/forge-ui/dropdown_menu";
import { tourEvents } from "@/components/Onboarding/tourEvents";
import { fetchProjects } from "@/hooks/project-queries";
import { trackEvent } from "@/lib/analytics";
import { toast } from "@/lib/toast-manager";
import { useProjectStore, type Project } from "@/store/projectStore";

import { ForgeConsoleContext } from "../../Console/consoleContext";
import { BUTTON_PRIMARY, BUTTON_SECONDARY, BUTTON_SMALL, ICON_BUTTON } from "../Dashboards/parts";
import { AskAgentSheet } from "./AskAgentSheet";
import { useAskAgentMode } from "./askAgentMode";
import { useObsAskContext } from "./askReliantContext";
import { askWorkspaceName, buildAskReliantPrompt, type AskEnv, type AskIntent, type AskSubject } from "./askReliantPrompt";
import { resolveAskProject, type AskProjectResolution } from "./askReliantProject";
import { startAskReliantChat } from "./startAskReliantChat";

export interface AskReliantOptions {
  /** The forge project, when the surface knows it and the console context does not. */
  forgeProject?: string;
  /** The environments of the project, listed in the prompt when the subject's own is unknown. */
  envChoices?: readonly string[];
  onChatCreated?: (chatId: string) => void;
}

type Choosing = Extract<AskProjectResolution, { kind: "choose" }> & { subject: AskSubject; onChatCreated?: (chatId: string) => void; intent?: AskIntent };

/** The handoff mode's one label, on every surface. */
const ASK_AGENT_LABEL = "Ask an agent";
const ASK_AGENT_HINT = "Hand this to your own agent: a prompt with the ids and the forge commands to fetch the rest.";

const nonce = () => Math.random().toString(36).slice(2, 6);

const errorText = (error: unknown) => (error instanceof Error ? error.message : String(error));

/**
 * `ask(subject)` starts it; render `dialog` once beside the surface (it is
 * nothing until a project has to be chosen). For a surface that has a menu
 * rather than a button.
 *
 * With an intent, the prompt says what the chat is for; "fix" also sends it at
 * once, so an agent starts on it on its own branch (startAskReliantChat).
 */
export function useAskReliant(options: AskReliantOptions = {}) {
  const consoleContext = useContext(ForgeConsoleContext);
  const obs = useObsAskContext();
  const router = useRouter({ warn: false });
  const { mode, cli, demo } = useAskAgentMode();
  // What is starting: an intent, "ask" for none, or null.
  const [starting, setStarting] = useState<AskIntent | "ask" | null>(null);
  const [choosing, setChoosing] = useState<Choosing | null>(null);
  // Handoff mode: the subject whose sheet is open.
  const [handoff, setHandoff] = useState<{ subject: AskSubject; intent?: AskIntent } | null>(null);
  const latest = useRef({ options, consoleContext, obs });
  latest.current = { options, consoleContext, obs };

  const run = useCallback(
    async (project: Project, subject: AskSubject, onChatCreated?: (chatId: string) => void, intent?: AskIntent): Promise<boolean> => {
      const { options: opts, consoleContext: console, obs: ctx } = latest.current;
      const forgeProject = opts.forgeProject ?? console?.forgeProject ?? ctx.forgeProject;
      const envChoices = opts.envChoices ?? (ctx.environments.length ? ctx.environments.map((env) => env.name) : console?.roster.envs.map((env) => env.name));
      const prompt = buildAskReliantPrompt(subject, { forgeProject, envChoices, intent });
      setStarting(intent ?? "ask");
      try {
        const started = await startAskReliantChat({
          project,
          prompt,
          workspaceName: askWorkspaceName(subject, nonce(), intent),
          send: intent === "fix",
          onChatCreated: onChatCreated ?? opts.onChatCreated,
          openProject: (projectId) => void router?.navigate({ to: "/project/$projectId", params: { projectId }, search: {} }),
        });
        if (started.sendError !== undefined) {
          toast.warning(`Couldn't start the agent: ${errorText(started.sendError)}. Its prompt is waiting in the composer; send it when you're ready.`);
        }
        return true;
      } catch (error) {
        void toast.error(error);
        return false;
      } finally {
        setStarting(null);
      }
    },
    [router]
  );

  const ask = useCallback(
    async (subject: AskSubject, onChatCreated?: (chatId: string) => void, intent?: AskIntent): Promise<boolean> => {
      tourEvents.emit("askAgent:opened", { subjectKind: subject.kind, mode });
      trackEvent("ask_agent_opened", { subject_kind: subject.kind, mode, surface: cli === "forge" ? "forge" : "reliant", demo });
      if (mode === "handoff") {
        // No daemon, no project: the sheet hands the prompt to the reader's own agent.
        setHandoff({ subject, intent });
        return true;
      }
      const { options: opts, consoleContext: console, obs: ctx } = latest.current;
      const projects = await fetchProjects().catch(() => []);
      const { currentProject } = useProjectStore.getState();
      const resolution = resolveAskProject({
        forgeProject: opts.forgeProject ?? console?.forgeProject ?? ctx.forgeProject,
        consoleProjectId: console?.projectId,
        projects,
        currentProject,
      });
      if (resolution.kind === "project") return run(resolution.project, subject, onChatCreated, intent);
      setChoosing({ ...resolution, subject, onChatCreated, intent });
      return false;
    },
    [run, mode, cli, demo]
  );

  const busy = starting !== null;
  const dialog: ReactNode = handoff ? (
    <AskAgentSheet
      subject={handoff.subject}
      intent={handoff.intent}
      forgeProject={options.forgeProject ?? consoleContext?.forgeProject ?? obs.forgeProject}
      envChoices={options.envChoices ?? (obs.environments.length ? obs.environments.map((env) => env.name) : consoleContext?.roster?.envs.map((env) => env.name))}
      onClose={() => setHandoff(null)}
    />
  ) : choosing ? (
    <ProjectChoice
      choosing={choosing}
      busy={busy}
      onCancel={() => setChoosing(null)}
      onChoose={(project) => {
        const { subject, onChatCreated, intent } = choosing;
        void run(project, subject, onChatCreated, intent).then((started) => started && setChoosing(null));
      }}
    />
  ) : null;

  return { ask, busy, starting, dialog, mode };
}

const INVESTIGATE_HINT = "Open a new chat on its own workspace with this written up for you to review. Nothing is sent until you send it.";
const FIX_HINT = "Start an agent on it now, in a new chat on its own workspace and git branch, so your main checkout is untouched.";

/**
 * The two actions for a surface with room for them: "Investigate in chat"
 * (prompt waiting, unsent) and "Fix with agent" (sent at once, on a branch).
 * With `linkedChat`, a chat already started from this subject leads, and the
 * two move into a menu so the obvious click is not a duplicate.
 */
export function AskReliantActions({
  subject,
  onChatCreated,
  forgeProject,
  envChoices,
  linkedChat,
}: AskReliantOptions & {
  subject: AskSubject;
  /** A chat already started from this subject: its label and how to open it. */
  linkedChat?: { label: string; open: () => void };
}) {
  const { ask, starting, dialog, mode } = useAskReliant({ forgeProject, envChoices, onChatCreated });
  return (
    <span className="inline-flex flex-wrap items-center gap-2" data-testid="ask-reliant-actions">
      {mode === "handoff" ? (
        // One button: the sheet has the Investigate / Fix toggle. A chat linked
        // to this subject lives in Reliant, so there is nothing to open here.
        <Button variant="outline" size="sm" leftIcon={<Bot className="h-3.5 w-3.5" />} onClick={() => void ask(subject, undefined, "investigate")} title={ASK_AGENT_HINT} data-testid="ask-agent">
          {ASK_AGENT_LABEL}
        </Button>
      ) : linkedChat ? (
        <>
          <Button variant="outline" size="sm" leftIcon={<MessageSquareText className="h-3.5 w-3.5" />} onClick={linkedChat.open} title={`Open ${linkedChat.label}, started from this`} data-testid="ask-reliant-open-chat">
            Open chat
          </Button>
          <AskReliantMenu subject={subject} onChatCreated={onChatCreated} forgeProject={forgeProject} envChoices={envChoices} appearance="icon" label="Start another chat" />
        </>
      ) : (
        <>
          <Button
            variant="outline"
            size="sm"
            leftIcon={<MessageSquarePlus className="h-3.5 w-3.5" />}
            onClick={() => void ask(subject, undefined, "investigate")}
            disabled={starting !== null}
            loading={starting === "investigate"}
            title={INVESTIGATE_HINT}
            data-testid="ask-reliant-investigate"
          >
            Investigate in chat
          </Button>
          <Button
            variant="outline"
            size="sm"
            leftIcon={<Wrench className="h-3.5 w-3.5" />}
            onClick={() => void ask(subject, undefined, "fix")}
            disabled={starting !== null}
            loading={starting === "fix"}
            title={FIX_HINT}
            data-testid="ask-reliant-fix"
          >
            Fix with agent
          </Button>
        </>
      )}
      {dialog}
    </span>
  );
}

/**
 * The same two actions behind one trigger, for a row with no room for two
 * buttons: "icon" is a bare icon button, "button" a small labelled one.
 */
export function AskReliantMenu({
  subject,
  onChatCreated,
  forgeProject,
  envChoices,
  appearance = "icon",
  label = "Ask Reliant",
  testId = "ask-reliant-menu",
}: AskReliantOptions & { subject: AskSubject; appearance?: "icon" | "button"; label?: string; testId?: string }) {
  const { ask, busy, dialog, mode } = useAskReliant({ forgeProject, envChoices, onChatCreated });
  if (mode === "handoff") {
    return (
      <>
        <button
          type="button"
          onClick={() => void ask(subject, undefined, "investigate")}
          aria-label={appearance === "icon" ? ASK_AGENT_LABEL : undefined}
          title={ASK_AGENT_HINT}
          className={appearance === "icon" ? ICON_BUTTON : BUTTON_SMALL}
          data-testid="ask-agent"
        >
          <Bot className="h-3.5 w-3.5" aria-hidden="true" />
          {appearance === "button" && ASK_AGENT_LABEL}
        </button>
        {dialog}
      </>
    );
  }
  return (
    <>
      <DropdownMenu
        align="right"
        label={label}
        menuClassName="min-w-48"
        disabled={busy}
        trigger={(props) => (
          <button
            type="button"
            {...props}
            aria-label={appearance === "icon" ? label : undefined}
            title={label}
            className={appearance === "icon" ? ICON_BUTTON : BUTTON_SMALL}
            data-testid={testId}
          >
            {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" /> : <MessageSquarePlus className="h-3.5 w-3.5" aria-hidden="true" />}
            {appearance === "button" && (
              <>
                {label}
                <ChevronDown className="h-3 w-3" aria-hidden="true" />
              </>
            )}
          </button>
        )}
        groups={[
          {
            items: [
              { label: "Investigate in chat", icon: <MessageSquarePlus className="h-3.5 w-3.5" />, onClick: () => void ask(subject, undefined, "investigate"), testId: "ask-reliant-investigate" },
              { label: "Fix with agent", icon: <Wrench className="h-3.5 w-3.5" />, onClick: () => void ask(subject, undefined, "fix"), testId: "ask-reliant-fix" },
            ],
          },
        ]}
      />
      {dialog}
    </>
  );
}

/** The button for a surface with room for one. */
export function AskReliantButton({
  subject,
  onChatCreated,
  forgeProject,
  envChoices,
  appearance = "outline",
  disabled,
  title,
}: AskReliantOptions & {
  subject: AskSubject;
  /** "outline": the Button the view's other actions use. "small": the dashboard drawer's compact one. */
  appearance?: "outline" | "small";
  disabled?: boolean;
  title?: string;
}) {
  const { ask, busy, dialog, mode } = useAskReliant({ forgeProject, envChoices, onChatCreated });
  const handoff = mode === "handoff";
  const label = handoff ? ASK_AGENT_LABEL : "Ask Reliant";
  const hint = handoff ? ASK_AGENT_HINT : (title ?? "Open a new chat on its own workspace with this written up for you to review. Nothing is sent until you send it.");
  const testId = handoff ? "ask-agent" : "ask-reliant";
  return (
    <>
      {appearance === "small" ? (
        <button type="button" className={BUTTON_SMALL} onClick={() => void ask(subject)} disabled={disabled || busy} title={hint} data-testid={testId}>
          <MessageSquarePlus className="h-3 w-3" aria-hidden="true" />
          {label}
        </button>
      ) : (
        <Button variant="outline" size="sm" leftIcon={<MessageSquarePlus className="h-3.5 w-3.5" />} onClick={() => void ask(subject)} disabled={disabled} loading={busy} title={hint} data-testid={testId}>
          {label}
        </Button>
      )}
      {dialog}
    </>
  );
}

/**
 * "Deploy with an agent": the console's empty states lead with this, as /apps
 * leads with "Build an app". It opens a new chat on its own workspace with the
 * deploy written up (askReliantPrompt `deploy`: `reliant forge env deploy`,
 * then `obs check`), waiting for the reader to send it; the command itself
 * stays on the page beside it for anyone who would rather run it.
 */
export function DeployWithAgentButton({ env, forgeProject, envChoices }: AskReliantOptions & { env: AskEnv }) {
  const { ask, busy, dialog } = useAskReliant({ forgeProject, envChoices });
  return (
    <>
      <Button
        variant="primary"
        size="md"
        leftIcon={<Rocket className="h-4 w-4" />}
        onClick={() => void ask({ kind: "deploy", env })}
        loading={busy}
        title={`Open a new chat with the deploy${env ? ` to ${env}` : ""} written up for an agent. Nothing runs until you send it.`}
        data-testid="deploy-with-agent"
      >
        Deploy with an agent
      </Button>
      {dialog}
    </>
  );
}

const REASON: Record<Choosing["reason"], string> = {
  ambiguous: "More than one project here declares this forge project, and none of them is open.",
  unmapped: "No project here declares this forge project, so Reliant can't tell which checkout it is.",
};

/**
 * Sits above the dashboard's panel drawer (z-60) like AlertFromPanelDialog, and
 * keeps Escape from also closing the drawer under it.
 */
function ProjectChoice({ choosing, busy, onCancel, onChoose }: { choosing: Choosing; busy: boolean; onCancel: () => void; onChoose: (project: Project) => void }) {
  const name = useId();
  const [picked, setPicked] = useState(choosing.preselected);
  const project = choosing.candidates.find((candidate) => candidate.id === picked);
  return (
    <div className="relative z-[70]" onKeyDown={(event) => event.key === "Escape" && event.nativeEvent.preventDefault()}>
      <Modal
        open
        onClose={onCancel}
        title="Which project is this?"
        description={REASON[choosing.reason]}
        footer={
          <div className="flex justify-end gap-2">
            <button type="button" className={BUTTON_SECONDARY} onClick={onCancel}>
              Cancel
            </button>
            <button type="button" className={BUTTON_PRIMARY} disabled={!project || busy} onClick={() => project && onChoose(project)} data-testid="ask-reliant-start">
              {busy ? "Starting…" : project ? `Start in ${project.name}` : "Choose a project"}
            </button>
          </div>
        }
      >
        {choosing.candidates.length === 0 ? (
          <p className="text-sm text-ink-muted" data-testid="ask-reliant-no-projects">
            There is no project to start a chat in. Add one from the main screen first.
          </p>
        ) : (
          <div role="radiogroup" aria-label="Project" className="space-y-1.5" data-testid="ask-reliant-projects">
            {choosing.candidates.map((candidate) => (
              <label key={candidate.id} className="flex cursor-pointer items-start gap-2 rounded-md border border-border bg-surface px-3 py-2 text-sm has-[:checked]:border-ink/40">
                <input type="radio" name={name} value={candidate.id} checked={picked === candidate.id} onChange={() => setPicked(candidate.id)} className="mt-1" />
                <span className="min-w-0">
                  <span className="block font-medium text-ink">{candidate.name}</span>
                  <span className="block truncate font-mono text-xs text-ink-muted">{candidate.path}</span>
                </span>
              </label>
            ))}
          </div>
        )}
      </Modal>
    </div>
  );
}
