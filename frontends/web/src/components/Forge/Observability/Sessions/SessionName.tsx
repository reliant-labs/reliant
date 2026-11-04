// Copyright (c) 2025 Reliant Labs

/**
 * A session's human name where a view knows only its id — an error's event, a
 * trace's browser span. It reads the session's summary (the same cached query
 * the session page uses, so opening the link costs no second read) and shows
 * `<frontend> · <start>`. Until the summary arrives, or when the session is
 * gone or out of scope, it says "Session" and the short id — never the bare id
 * as a name.
 */

import { useSession } from "@/hooks/observability-session-queries";
import type { RumScope } from "@/services/controlPlane/observability/rumTypes";

import { shortId } from "../LogsView";
import { isReplayKnownUnplayable, sessionTitle } from "./sessionModel";

export function SessionName({ scope, sessionId, replayPrefix = false }: { scope: RumScope; sessionId: string; replayPrefix?: boolean }) {
  const session = useSession(scope, sessionId);
  if (session.data) {
    const replay = replayPrefix && session.data.hasReplay && !isReplayKnownUnplayable(sessionId);
    return (
      <span data-testid="session-name" data-state="ready" data-replay={replay ? "playable" : undefined}>
        {replay && "Replay · "}
        {sessionTitle(session.data.service, session.data.startedMs)}
      </span>
    );
  }
  return (
    <span data-testid="session-name" data-state={session.isLoading ? "loading" : "unresolved"}>
      Session <span className="font-mono">{shortId(sessionId)}</span>
    </span>
  );
}
