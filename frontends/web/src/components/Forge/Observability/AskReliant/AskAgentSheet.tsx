// Copyright (c) 2025 Reliant Labs

/**
 * The handoff sheet: "Ask an agent" where there is no Reliant chat to start —
 * the forge shell, and every demo on fixture data (askAgentMode.tsx).
 *
 *   1. Paste into your agent   the references-only prompt (askReliantPrompt,
 *                              `host: "external"`, so it claims no workspace),
 *                              with an Investigate / Fix toggle and Copy.
 *   2. Or run it yourself      the forge commands that read the subject
 *                              (askAgentCommands), each with its own Copy.
 *   3. Give your agent the     forge's observability skill and MCP server.
 *      playbook
 *   4. Footer                  the login the agent needs (forge shell), and
 *                              "Open in Reliant", where Investigate and Fix
 *                              are one click away; on sample data, a note
 *                              saying so instead.
 *
 * Copy buttons put exactly the text shown on the clipboard.
 */

import { useEffect, useRef, useState } from "react";
import { Check, Copy, ExternalLink } from "lucide-react";

import Modal from "@/components/forge-ui/modal";
import { tourEvents } from "@/components/Onboarding/tourEvents";
import { trackEvent } from "@/lib/analytics";

import { consolePath, withSearch, type ConsolePage } from "../../Console/paths";
import { BUTTON_SECONDARY } from "../Dashboards/parts";
import { useAskAgentMode } from "./askAgentMode";
import { askAgentCommands, askAgentPlaybook, buildAskReliantPrompt, type AskIntent, type AskSubject } from "./askReliantPrompt";
import { SEGMENTED, segmentTone } from "../../controlStyles";
import { ReliantWorkflowNote } from "./ReliantWorkflowNote";

/** Reliant's web app, where the console's chat actions run. */
export const RELIANT_APP_URL = "https://app.reliantlabs.io";

/** Where "Open in Reliant" lands: the same subject in Reliant's forge console. */
export function openInReliantHref(subject: AskSubject, forgeProject: string | undefined): string {
  if (!forgeProject) return `${RELIANT_APP_URL}/forge`;
  const env = subject.env ? { env: subject.env } : {};
  const at = (page: ConsolePage, search: Record<string, unknown> = {}) => `${RELIANT_APP_URL}${withSearch(consolePath(forgeProject, page), { ...env, ...search })}`;
  switch (subject.kind) {
    case "issue":
      return at({ kind: "obs-view", view: "errors" }, { issue: subject.issueId });
    case "trace":
      return at({ kind: "obs-view", view: "traces" }, { trace: subject.traceId });
    case "alert":
      return at({ kind: "obs-view", view: "alerts" }, { rule: subject.rule });
    case "session":
      return at({ kind: "obs-view", view: "sessions" }, { session: subject.sessionId });
    case "release":
      return at({ kind: "release", version: subject.version });
    default:
      return at({ kind: "overview" });
  }
}

type CopyWhat = "prompt" | "command" | "playbook";

/** Copy `text`; `copied` is true for a moment after it lands. */
function useCopy(text: string, onCopied: () => void) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      return; // No clipboard (insecure context, denied): the text is still on screen to select.
    }
    onCopied();
    setCopied(true);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 1500);
  }
  return { copied, copy };
}

function CopyRow({ text, testId, onCopied, label }: { text: string; testId: string; onCopied: () => void; label?: string }) {
  const { copied, copy } = useCopy(text, onCopied);

  return (
    <div className="flex max-w-full items-center gap-1 rounded-md border border-border/60 bg-surface py-0.5 pl-2 pr-0.5">
      <code className="min-w-0 flex-1 overflow-x-auto whitespace-pre font-mono text-xs text-ink">{text}</code>
      <button
        type="button"
        onClick={() => void copy()}
        aria-label={copied ? `Copied ${label ?? text}` : `Copy ${label ?? text}`}
        title={copied ? "Copied" : "Copy"}
        className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-ink-muted hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        data-testid={testId}
      >
        {copied ? <Check className="h-3 w-3" aria-hidden="true" /> : <Copy className="h-3 w-3" aria-hidden="true" />}
      </button>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="space-y-2">
      <h4 className="text-xs font-semibold uppercase tracking-wide text-ink-muted">{title}</h4>
      {children}
    </section>
  );
}

export interface AskAgentSheetProps {
  subject: AskSubject;
  /** The intent the reader picked on the surface; the sheet's toggle starts on it. */
  intent?: AskIntent;
  forgeProject?: string;
  envChoices?: readonly string[];
  onClose: () => void;
}

export function AskAgentSheet({ subject, intent: initialIntent, forgeProject, envChoices, onClose }: AskAgentSheetProps) {
  const { cli, demo } = useAskAgentMode();
  const [intent, setIntent] = useState<AskIntent>(initialIntent ?? "investigate");
  const ctx = { forgeProject, envChoices, intent, cli, host: "external" as const };
  const prompt = buildAskReliantPrompt(subject, ctx);
  const commands = askAgentCommands(subject, ctx);
  const playbook = askAgentPlaybook(cli);
  const standalone = cli === "forge";

  const copied = (what: CopyWhat, verb?: string) => {
    tourEvents.emit("askAgent:copied", { what });
    trackEvent("ask_agent_copied", { what, intent, ...(verb ? { verb } : {}) });
  };

  return (
    <div className="relative z-[70]" onKeyDown={(event) => event.key === "Escape" && event.nativeEvent.preventDefault()}>
      <Modal
        open
        onClose={onClose}
        size="md"
        title="Ask an agent"
        description="Hand this to your own agent: Claude Code, Codex or Cursor. It gets ids and forge commands, never the telemetry itself, and fetches the details with its own login."
        footer={
          <div className="flex w-full flex-wrap items-center justify-between gap-3">
            {demo ? (
              <p className="text-xs text-ink-muted" data-testid="ask-agent-demo-note">
                These ids are sample data. On your app, the same button fills in yours.
              </p>
            ) : (
              <a
                href={openInReliantHref(subject, forgeProject)}
                target="_blank"
                rel="noopener noreferrer"
                onClick={() => trackEvent("ask_agent_open_in_reliant", { subject_kind: subject.kind })}
                className="inline-flex items-center gap-1.5 text-sm font-medium text-primary-ink hover:underline"
                data-testid="ask-agent-open-in-reliant"
              >
                Open in Reliant
                <ExternalLink className="h-3.5 w-3.5" aria-hidden="true" />
              </a>
            )}
            <button type="button" className={BUTTON_SECONDARY} onClick={onClose}>
              Done
            </button>
          </div>
        }
      >
        <div className="space-y-5" data-testid="ask-agent-sheet" data-subject-kind={subject.kind}>
          <Section title="Paste into your agent">
            <div role="radiogroup" aria-label="What the agent should do" className={`${SEGMENTED} text-xs`}>
              {(["investigate", "fix"] as const).map((option) => (
                <button
                  key={option}
                  type="button"
                  role="radio"
                  aria-checked={intent === option}
                  onClick={() => setIntent(option)}
                  className={`rounded px-2.5 py-1 ${segmentTone(intent === option)}`}
                  data-testid={`ask-agent-intent-${option}`}
                >
                  {option === "investigate" ? "Investigate" : "Fix"}
                </button>
              ))}
            </div>
            <div className="relative">
              <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-words rounded-md border border-border/60 bg-surface-sunken p-3 pr-10 font-mono text-xs text-ink" data-testid="ask-agent-prompt">
                {prompt}
              </pre>
              <div className="absolute right-1.5 top-1.5">
                <PromptCopy text={prompt} onCopied={() => copied("prompt")} />
              </div>
            </div>
          </Section>

          {commands.length > 0 && (
            <Section title="Or run it yourself">
              <div className="space-y-1.5">
                {commands.map((command, index) => (
                  <CopyRow key={command.command} text={command.command} testId={`ask-agent-copy-cmd-${index}`} onCopied={() => copied("command", command.verb)} />
                ))}
              </div>
            </Section>
          )}

          <Section title="Give your agent the playbook">
            <div className="space-y-1.5">
              {playbook.map((line, index) => (
                <CopyRow key={line} text={line} testId={`ask-agent-copy-playbook-${index}`} onCopied={() => copied("playbook")} />
              ))}
            </div>
            <p className="text-xs text-ink-muted">
              forge&apos;s MCP server covers introspection, skills, lint and generate; <code className="font-mono">{cli} obs</code> runs in your agent&apos;s shell.
            </p>
          </Section>

          {(demo || standalone) && <ReliantWorkflowNote />}

          {standalone && (
            <p className="text-xs text-ink-muted" data-testid="ask-agent-login">
              Your agent needs a forge login: <code className="font-mono">forge login</code>, once per machine. Signed in to Reliant?{" "}
              <code className="font-mono">reliant forge …</code> uses your session.
            </p>
          )}
        </div>
      </Modal>
    </div>
  );
}

/** The prompt's Copy: an icon button over the prompt block. */
function PromptCopy({ text, onCopied }: { text: string; onCopied: () => void }) {
  const { copied, copy } = useCopy(text, onCopied);
  return (
    <button
      type="button"
      onClick={() => void copy()}
      aria-label={copied ? "Copied the prompt" : "Copy the prompt"}
      title={copied ? "Copied" : "Copy"}
      className="inline-flex h-7 items-center gap-1 rounded border border-border bg-surface px-2 text-xs text-ink-muted hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      data-testid="ask-agent-copy-prompt"
    >
      {copied ? <Check className="h-3 w-3" aria-hidden="true" /> : <Copy className="h-3 w-3" aria-hidden="true" />}
      {copied ? "Copied" : "Copy"}
    </button>
  );
}
