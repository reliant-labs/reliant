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
 * Which Reliant project: the one that declares the console's forge project
 * (askReliantProject.ts). When that is not decidable the reader is asked, once,
 * with a picker; it never guesses.
 *
 * `onChatCreated` is called with the chat's id when the reader sends — the
 * first moment the chat exists — so a surface can record "chat started from
 * this issue". It fires from the new-chat screen, after this component has
 * unmounted, so it must not close over component state.
 */

import { useCallback, useContext, useId, useRef, useState, type ReactNode } from "react";
import { useRouter } from "@tanstack/react-router";
import { MessageSquarePlus } from "lucide-react";

import Modal from "@/components/forge-ui/modal";
import Button from "@/components/forge-ui/button";
import { fetchProjects } from "@/hooks/project-queries";
import { toast } from "@/lib/toast-manager";
import { useProjectStore, type Project } from "@/store/projectStore";

import { ForgeConsoleContext } from "../../Console/consoleContext";
import { BUTTON_PRIMARY, BUTTON_SECONDARY, BUTTON_SMALL } from "../Dashboards/parts";
import { useObsAskContext } from "./askReliantContext";
import { askWorkspaceName, buildAskReliantPrompt, type AskSubject } from "./askReliantPrompt";
import { resolveAskProject, type AskProjectResolution } from "./askReliantProject";
import { startAskReliantChat } from "./startAskReliantChat";

export interface AskReliantOptions {
  /** The forge project, when the surface knows it and the console context does not. */
  forgeProject?: string;
  /** The environments of the project, listed in the prompt when the subject's own is unknown. */
  envChoices?: readonly string[];
  onChatCreated?: (chatId: string) => void;
}

type Choosing = Extract<AskProjectResolution, { kind: "choose" }> & { subject: AskSubject; onChatCreated?: (chatId: string) => void };

const nonce = () => Math.random().toString(36).slice(2, 6);

/**
 * `ask(subject)` starts it; render `dialog` once beside the surface (it is
 * nothing until a project has to be chosen). For a surface that has a menu
 * rather than a button.
 */
export function useAskReliant(options: AskReliantOptions = {}) {
  const consoleContext = useContext(ForgeConsoleContext);
  const obs = useObsAskContext();
  const router = useRouter({ warn: false });
  const [busy, setBusy] = useState(false);
  const [choosing, setChoosing] = useState<Choosing | null>(null);
  const latest = useRef({ options, consoleContext, obs });
  latest.current = { options, consoleContext, obs };

  const run = useCallback(
    async (project: Project, subject: AskSubject, onChatCreated?: (chatId: string) => void): Promise<boolean> => {
      const { options: opts, consoleContext: console, obs: ctx } = latest.current;
      const forgeProject = opts.forgeProject ?? console?.forgeProject ?? ctx.forgeProject;
      const envChoices = opts.envChoices ?? (ctx.environments.length ? ctx.environments.map((env) => env.name) : console?.roster.envs.map((env) => env.name));
      const prompt = buildAskReliantPrompt(subject, { forgeProject, envChoices });
      setBusy(true);
      try {
        await startAskReliantChat({
          project,
          prompt,
          workspaceName: askWorkspaceName(subject, nonce()),
          onChatCreated: onChatCreated ?? opts.onChatCreated,
          openProject: (projectId) => void router?.navigate({ to: "/project/$projectId", params: { projectId }, search: {} }),
        });
        return true;
      } catch (error) {
        void toast.error(error);
        return false;
      } finally {
        setBusy(false);
      }
    },
    [router]
  );

  const ask = useCallback(
    async (subject: AskSubject, onChatCreated?: (chatId: string) => void): Promise<boolean> => {
      const { options: opts, consoleContext: console, obs: ctx } = latest.current;
      const projects = await fetchProjects().catch(() => []);
      const { currentProject } = useProjectStore.getState();
      const resolution = resolveAskProject({
        forgeProject: opts.forgeProject ?? console?.forgeProject ?? ctx.forgeProject,
        consoleProjectId: console?.projectId,
        projects,
        currentProject,
      });
      if (resolution.kind === "project") return run(resolution.project, subject, onChatCreated);
      setChoosing({ ...resolution, subject, onChatCreated });
      return false;
    },
    [run]
  );

  const dialog: ReactNode = choosing ? (
    <ProjectChoice
      choosing={choosing}
      busy={busy}
      onCancel={() => setChoosing(null)}
      onChoose={(project) => {
        const { subject, onChatCreated } = choosing;
        void run(project, subject, onChatCreated).then((started) => started && setChoosing(null));
      }}
    />
  ) : null;

  return { ask, busy, dialog };
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
  const { ask, busy, dialog } = useAskReliant({ forgeProject, envChoices, onChatCreated });
  const label = "Ask Reliant";
  const hint = title ?? "Open a new chat on its own workspace with this written up for you to review. Nothing is sent until you send it.";
  return (
    <>
      {appearance === "small" ? (
        <button type="button" className={BUTTON_SMALL} onClick={() => void ask(subject)} disabled={disabled || busy} title={hint} data-testid="ask-reliant">
          <MessageSquarePlus className="h-3 w-3" aria-hidden="true" />
          {label}
        </button>
      ) : (
        <Button variant="outline" size="sm" leftIcon={<MessageSquarePlus className="h-3.5 w-3.5" />} onClick={() => void ask(subject)} disabled={disabled} loading={busy} title={hint} data-testid="ask-reliant">
          {label}
        </Button>
      )}
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
