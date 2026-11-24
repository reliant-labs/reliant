// Copyright (c) 2026 Reliant Labs

/**
 * The nav footer's Help row, beside Settings: a small menu of the ways to
 * learn the app. Take a tour opens the journey picker (`?tour=pick:menu`,
 * Onboarding/JOURNEYS.md); Docs opens docs.reliantlabs.io; Keyboard shortcuts
 * opens Settings › Keyboard shortcuts. While the Setup Guide is dismissed and
 * still has work left, "Show setup guide" brings it back into the footer.
 *
 * Before it, the picker's only entries were the command palette, Settings ›
 * About, the Setup Guide itself and a link on the chat home's first visit:
 * nothing in the shell's own chrome.
 */

import { BookOpen, CircleHelp, Compass, Keyboard, ListChecks } from "lucide-react";

import DropdownMenu, { type MenuGroup } from "@/components/forge-ui/dropdown_menu";
import { useSetupGuideRevivable } from "@/components/Onboarding/OnboardingChecklist";
import { DOCS_ORIGIN } from "@/lib/learnLinks";
import { settingsPath } from "@/lib/paths";
import { cn } from "@/lib/utils";
import { useOnboardingChecklistStore } from "@/store/onboardingChecklistStore";

import { externalMark } from "./navConfig/items";

export function HelpMenu({
  onNavigate,
  onOpenTours,
}: {
  /** The nav's navigate (a drawer closes on it). */
  onNavigate: (href: string) => void;
  /** Opens the journey picker on the page on screen (`?tour=pick:menu`); without it there is no tour item. */
  onOpenTours?: () => void;
}) {
  const revivable = useSetupGuideRevivable();

  const groups: MenuGroup[] = [
    {
      items: [
        ...(onOpenTours ? [{ label: "Take a tour", icon: <Compass className="size-4" />, onClick: onOpenTours, testId: "help-menu-tour" }] : []),
        {
          label: "Docs",
          icon: <BookOpen className="size-4" />,
          hint: externalMark,
          onClick: () => void window.open(`${DOCS_ORIGIN}/`, "_blank", "noopener,noreferrer"),
          testId: "help-menu-docs",
        },
        {
          label: "Keyboard shortcuts",
          icon: <Keyboard className="size-4" />,
          onClick: () => onNavigate(settingsPath("shortcuts")),
          testId: "help-menu-shortcuts",
        },
      ],
    },
    ...(revivable
      ? [
          {
            items: [
              {
                label: "Show setup guide",
                icon: <ListChecks className="size-4" />,
                onClick: () => void useOnboardingChecklistStore.getState().revive(),
                testId: "help-menu-setup-guide",
              },
            ],
          },
        ]
      : []),
  ];

  return (
    <DropdownMenu
      groups={groups}
      align="left"
      label="Help"
      density="compact"
      menuClassName="min-w-[200px]"
      trigger={(props) => (
        // A footer row (primitives SidebarRow's look); the menu opens above it when the window is short.
        <button
          {...props}
          type="button"
          data-testid="nav-help"
          className={cn(
            "flex h-[var(--row-h)] w-full min-w-0 items-center gap-2 rounded-md px-2 text-left text-ui text-foreground transition-colors hover:bg-foreground/5",
            "focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
            props["aria-expanded"] && "bg-foreground/5",
          )}
        >
          <span className="flex size-4 shrink-0 items-center justify-center text-muted-foreground" aria-hidden="true">
            <CircleHelp className="size-4" />
          </span>
          <span className="min-w-0 flex-1 truncate">Help</span>
        </button>
      )}
    />
  );
}
