// Copyright (c) 2025 Reliant Labs

/**
 * The app shell's home: the new-chat view, with the app-building composer one
 * click (or one onboarding choice) away on a project that is not a forge
 * project yet. A forge project's apps live in the sidebar, not here.
 *
 * `?start=app` (appStart.ts) is what makes it the app-building composer: the
 * heading asks what to build and the starter text is in the composer. It is
 * read from the URL on every render rather than copied into state, because the
 * shell remounts this view while it settles the project — a flag consumed on
 * the first mount was gone by the second. The param is dropped once the chat
 * it was for is created, so the next new chat is an ordinary one.
 */

import { useCallback } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import { pickerParam } from "@/components/Onboarding/tourParam";
import { trackEvent } from "@/lib/analytics";
import { useProjectStore } from "@/store/projectStore";
import { PendingProviderBanner } from "../OnboardingFlow/PendingProviderBanner";

import { MainNewChatView } from "../Chat/MainNewChatView";
import { HomeBuildApp } from "./HomeBuildApp";
import { START_APP, START_REPO, isStartValue, starterPrompt, type StartValue } from "./appStart";

// The prefill's id, clear of the ones NewChatView's own prefills use
// (0…, and 1_000_000… for text handed back after a failed start). It is ONE
// id, so a remount applies the same text rather than a second copy.
const STARTER_PREFILL_ID = 2_000_000;

function StarterHeading({ start, projectName, onTour }: { start: StartValue; projectName?: string; onTour: () => void }) {
  const project = projectName?.trim() || "your project";
  const [title, hint] =
    start === START_APP
      ? ["What do you want to build?", "Finish the sentence below. Reliant builds it with forge and deploys it to a live URL."]
      : start === START_REPO
        ? [`Ready to deploy ${project}?`, "Send this as is, or change it first. Reliant sets it up with forge and gives you a live URL."]
        : ["What should we work on first?", "Send this as is, or ask for something else. Reliant works in your project."];
  return (
    <div className="space-y-1" data-testid="home-build-heading">
      <h1 className="text-xl font-semibold tracking-tight text-foreground">{title}</h1>
      <p className="text-sm text-muted-foreground">{hint}</p>
      <button
        type="button"
        onClick={onTour}
        data-testid="home-take-tour"
        className="text-xs font-medium text-primary-ink hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        Prefer a quick tour first?
      </button>
    </div>
  );
}

export function HomeNewChat({ onNavigateToWorktrees }: { onNavigateToWorktrees?: () => void }) {
  const navigate = useNavigate();
  const { start: rawStart } = useSearch({ strict: false }) as { start?: string };
  const start = isStartValue(rawStart) ? rawStart : undefined;
  const projectName = useProjectStore((state) => state.currentProject?.name);
  const setStart = useCallback(
    (value: StartValue | undefined) =>
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => {
          const { start: _start, ...rest } = prev;
          return value ? { ...rest, start: value } : rest;
        },
        replace: true,
      }),
    [navigate]
  );
  // The tour stays one click away: it is what finishing onboarding used to
  // drop the user into, and some people do want it first. It opens as a menu
  // pick, so skipping it does not trigger the post-onboarding key prompt.
  const openTour = useCallback(() => {
    trackEvent("onboarding_tour_chosen", { from: "starter_heading" });
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({ ...prev, tour: pickerParam("menu") }),
    });
  }, [navigate]);

  return (
    <MainNewChatView
      tabId="new-chat"
      onNavigateToWorktrees={onNavigateToWorktrees}
      showProjectChip
      onChatCreated={start ? () => setStart(undefined) : undefined}
      composerPrefill={start ? { text: starterPrompt(start, projectName), id: STARTER_PREFILL_ID } : undefined}
      heading={
        <>
          <PendingProviderBanner />
          {start ? <StarterHeading start={start} projectName={projectName} onTour={openTour} /> : null}
        </>
      }
      welcomePanel={start ? undefined : <HomeBuildApp onStart={() => setStart(START_APP)} />}
    />
  );
}
