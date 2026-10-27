// Copyright (c) 2025 Reliant Labs

/**
 * The app shell's home: the new-chat view, with the project's apps under its
 * controls and the app-building composer one click (or one onboarding choice)
 * away.
 *
 * `?start=app` (appStart.ts) is what makes it the app-building composer: the
 * heading asks what to build and the starter text is in the composer. It is
 * read from the URL on every render rather than copied into state, because the
 * shell remounts this view while it settles the project — a flag consumed on
 * the first mount was gone by the second. The param is dropped once the chat
 * it was for is created, so the next new chat is an ordinary one.
 */

import { useCallback, useRef, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import type { ComposerPrefill } from "../Chat/ChatInput";
import { MainNewChatView } from "../Chat/MainNewChatView";
import { HomeApps } from "./HomeApps";
import { BUILD_APP_PROMPT, START_APP } from "./appStart";

// Prefill ids, clear of the ones NewChatView's own prefills use (0…, and
// 1_000_000… for text handed back after a failed start). The build prefill
// has ONE id, so a remount applies the same text rather than a second copy.
const BUILD_PREFILL_ID = 2_000_000;
const DEPLOY_PREFILL_BASE_ID = 3_000_000;

export function HomeNewChat({ onNavigateToWorktrees }: { onNavigateToWorktrees?: () => void }) {
  const navigate = useNavigate();
  const { start } = useSearch({ strict: false }) as { start?: string };
  const building = start === START_APP;
  const [deployPrefill, setDeployPrefill] = useState<ComposerPrefill | undefined>(undefined);
  const nextDeployId = useRef(DEPLOY_PREFILL_BASE_ID);

  const setStart = useCallback(
    (value: typeof START_APP | undefined) =>
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

  const onPrefill = useCallback(
    (text: string) => {
      if (text === BUILD_APP_PROMPT) {
        setStart(START_APP);
        return;
      }
      nextDeployId.current += 1;
      setDeployPrefill({ text, id: nextDeployId.current });
      if (building) setStart(undefined);
    },
    [building, setStart]
  );

  return (
    <MainNewChatView
      tabId="new-chat"
      onNavigateToWorktrees={onNavigateToWorktrees}
      showProjectChip
      onChatCreated={building ? () => setStart(undefined) : undefined}
      composerPrefill={building ? { text: BUILD_APP_PROMPT, id: BUILD_PREFILL_ID } : deployPrefill}
      heading={
        building ? (
          <div className="space-y-1" data-testid="home-build-heading">
            <h1 className="text-xl font-semibold tracking-tight text-foreground">What do you want to build?</h1>
            <p className="text-sm text-muted-foreground">
              Finish the sentence below. Reliant builds it with forge and deploys it to a live URL.
            </p>
          </div>
        ) : undefined
      }
      welcomePanel={building ? undefined : <HomeApps onPrefill={onPrefill} />}
    />
  );
}
