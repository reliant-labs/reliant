/**
 * Slash commands for the chat composer.
 *
 * Bridges the shared command registry (src/lib/commands.ts) to runnable
 * actions. Rather than reaching into a dozen stores from the composer — which
 * would duplicate the shortcut handlers and drift from them — each command
 * dispatches the SAME window event its keyboard shortcut dispatches. ModernApp
 * owns those handlers, so there is exactly one implementation per action.
 *
 * The shortcut hint beside each entry is read from the shortcuts store, so it
 * reflects the user's current binding — including remaps — and the menu teaches
 * the keyboard rather than replacing it.
 */

import { useMemo } from "react";
import {
  MessageSquarePlus,
  Search,
  FolderTree,
  GitBranch,
  Cpu,
  Brain,
  Workflow,
  TerminalSquare,
  Settings,
  Repeat,
  Scissors,
  FileText,
  Paperclip,
  Copy,
  Share2,
} from "lucide-react";
import { slashCommands as registryCommands } from "../lib/commands";
import { useShortcutsStore } from "../store/shortcutsStore";
import { parseBinding } from "../lib/keyboard/chord";
import { detectPlatform, formatBinding } from "../lib/keyboard/platform";
import type { SlashCommand } from "../components/Chat/SlashCommandMenu";

const ICONS: Record<string, React.ReactNode> = {
  "new-chat": <MessageSquarePlus className="h-4 w-4" />,
  "switch-chat": <Repeat className="h-4 w-4" />,
  "switch-project": <FolderTree className="h-4 w-4" />,
  "search-chats": <Search className="h-4 w-4" />,
  "change-model": <Cpu className="h-4 w-4" />,
  "change-thinking": <Brain className="h-4 w-4" />,
  "change-workflow": <Workflow className="h-4 w-4" />,
  "toggle-terminal": <TerminalSquare className="h-4 w-4" />,
  "show-changes": <GitBranch className="h-4 w-4" />,
  settings: <Settings className="h-4 w-4" />,
  // Composer-only actions — these have no keyboard shortcut by design.
  compact: <Scissors className="h-4 w-4" />,
  prompt: <FileText className="h-4 w-4" />,
  attach: <Paperclip className="h-4 w-4" />,
  "copy-transcript": <Copy className="h-4 w-4" />,
  branch: <Share2 className="h-4 w-4" />,
};

export function useSlashCommands(): SlashCommand[] {
  const shortcuts = useShortcutsStore((state) => state.shortcuts);

  return useMemo(() => {
    const { isMac, isDesktop } = detectPlatform();

    /** The live, user-visible binding for a shortcut handler, if it has one. */
    const shortcutFor = (handler?: string): string | undefined => {
      if (!handler) return undefined;
      const shortcut = Object.values(shortcuts).find(
        (s) => s.handler === handler,
      );
      if (!shortcut) return undefined;

      const authored =
        shortcut.currentBinding ??
        (isDesktop ? shortcut.defaultBinding : shortcut.defaultWebBinding);
      if (!authored) return undefined;

      return formatBinding(parseBinding(authored, isMac), isMac);
    };

    return registryCommands().map((command) => ({
      id: command.id,
      title: command.title,
      description: command.description,
      icon: ICONS[command.id],
      keywords: command.keywords,
      shortcut: shortcutFor(command.handler),
      action: () => {
        if (command.handler) {
          window.dispatchEvent(
            new CustomEvent("run-shortcut", {
              detail: { handler: command.handler },
            }),
          );
          return;
        }
        // Composer-scoped actions have no shortcut handler; they are addressed
        // by command id and handled where the composer lives.
        window.dispatchEvent(
          new CustomEvent("run-command", { detail: { id: command.id } }),
        );
      },
    }));
  }, [shortcuts]);
}
