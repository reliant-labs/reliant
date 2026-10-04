// Copyright (c) 2025 Reliant Labs

/**
 * /runs/$runId — watch or review one run without leaving the Runs area
 * (WORKFLOW_UI.md §4.2). `runId` is the run's chat id.
 *
 * The transcript is the SAME ChatContainer the project view and mobile use,
 * mounted standalone the way Mobile/MobileChatScreen does: every message, tool
 * card, approval, diagram panel and stream semantic comes with it. This page
 * adds only what a run needs on top — a header that says who started it and
 * offers the run's controls, and a card saying what fired it.
 */

import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { toast } from "sonner";

import type { Chat } from "@/api/client";
import { runErrorMessage } from "@/api/run-grpc";
import { useChat } from "@/hooks/chat-queries";
import { useAdoptRun, useRunControl } from "@/hooks/run-queries";
import { useProjectStore } from "@/store/projectStore";
import Card from "../forge-ui/card";
import { Button } from "../ui/Button";
import { ChatContainer } from "../Chat/ChatContainer";
import { RunHeader } from "./RunHeader";
import { useRunRoute } from "./RunRouteLoader";
import { RunsShell } from "./RunsPage";
import { TriggerCard } from "./TriggerCard";
import { useRerun } from "./useRerun";
import { useRunLaunch } from "./useRunLaunch";
import { RunMachineBanner } from "./MachineStatus";

export function RunDetailPage() {
  const { runId } = useParams({ strict: false }) as { runId?: string };
  return <RunsShell layout="fill">{runId ? <RunDetail chatId={runId} /> : <RunNotFound />}</RunsShell>;
}

export function RunDetail({ chatId }: { chatId: string }) {
  const route = useRunRoute(chatId);

  if (route.status === "loading") return <RunDetailSkeleton />;
  if (route.status === "not_found") return <RunNotFound />;
  if (route.status === "error") {
    return (
      <CenteredColumn>
        <Card padding="lg" role="alert">
          <p className="text-sm font-medium text-foreground">This run could not be loaded.</p>
          <p className="mt-1 text-sm text-muted-foreground">{route.message}</p>
          <Button className="mt-4" variant="outline" onClick={route.retry}>
            Try again
          </Button>
        </Card>
      </CenteredColumn>
    );
  }
  return <LoadedRun initialChat={route.chat} />;
}

function LoadedRun({ initialChat }: { initialChat: Chat }) {
  const navigate = useNavigate();
  // The detail cache is kept live by the update stream; the loaded chat only
  // covers the first render.
  const chat = useChat(initialChat.id).data ?? initialChat;
  const projectName = useProjectStore(
    (state) => state.projects.find((p) => p.id === chat.projectId)?.name,
  );
  const launch = useRunLaunch(chat);
  const triggerName = launch.triggerName;
  const rerun = useRerun(chat, launch.event, triggerName);

  const control = useRunControl();
  const adopt = useAdoptRun();
  const runAction = (action: "pause" | "resume" | "stop", verb: string) =>
    control.mutate(
      { chatId: chat.id, action },
      { onError: (error) => toast.error(`Could not ${verb} this run`, { description: runErrorMessage(error) }) },
    );

  const openInProject = () =>
    void navigate({ to: "/project/$projectId", params: { projectId: chat.projectId }, search: {} });

  const onOpenAsChat = () => {
    const isChat = !chat.launchKind || chat.launchKind === "chat.start" || chat.adoptedAt;
    if (isChat) {
      openInProject();
      return;
    }
    adopt.mutate(chat.id, {
      onSuccess: () => {
        toast.success("Added to your chats", { description: "It stays in the chat list from now on." });
        openInProject();
      },
      onError: (error) => toast.error("Could not open this run as a chat", { description: runErrorMessage(error) }),
    });
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="shrink-0 space-y-3 border-b border-border/60 bg-card px-6 py-4">
        <RunHeader
          chat={chat}
          triggerName={triggerName}
          parent={launch.parent}
          projectName={projectName}
          busy={control.isPending || adopt.isPending || rerun.busy}
          actions={{
            onPause: () => runAction("pause", "pause"),
            onResume: () => runAction("resume", "resume"),
            onStop: () => runAction("stop", "stop"),
            onOpenAsChat,
            onRerun: rerun.onRerun,
            onRunAutomationNow: rerun.onRunAutomationNow,
          }}
        />
        <RunMachineBanner chat={chat} />
        {/* Not until the launch event has loaded: the card must not show
            text that later changes meaning (§4.4). */}
        {!launch.loading && (
          <TriggerCard
            launchKind={chat.launchKind}
            triggerId={chat.triggerId}
            triggerName={triggerName}
            timezone={launch.timezone}
            parent={launch.parent}
            event={launch.event}
            prompt={rerun.prompt}
          />
        )}
      </div>
      {rerun.dialog}
      <div className="min-h-0 flex-1">
        <ChatContainer tabId={chat.id} hideChatTitle />
      </div>
    </div>
  );
}

function CenteredColumn({ children }: { children: React.ReactNode }) {
  return <div className="mx-auto w-full max-w-3xl px-6 py-8">{children}</div>;
}

function RunNotFound() {
  return (
    <CenteredColumn>
      <Card padding="lg" role="alert">
        <p className="text-sm font-medium text-foreground">This run doesn't exist or isn't yours.</p>
        <p className="mt-1 text-sm text-muted-foreground">It may have been deleted.</p>
        <Link
          to="/runs"
          className="mt-4 inline-flex h-9 items-center rounded-md border border-border px-3 text-sm font-medium text-foreground hover:bg-muted/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          All runs
        </Link>
      </Card>
    </CenteredColumn>
  );
}

function RunDetailSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading run" className="flex min-h-0 flex-1 flex-col">
      <div className="space-y-2 border-b border-border/60 bg-card px-6 py-4">
        <div className="h-6 w-72 max-w-full animate-pulse rounded bg-border motion-reduce:animate-none" />
        <div className="h-4 w-96 max-w-full animate-pulse rounded bg-border/60 motion-reduce:animate-none" />
      </div>
      <div className="mx-auto w-full max-w-3xl flex-1 space-y-4 px-6 py-6">
        {[0, 1, 2].map((row) => (
          <div key={row} className="h-16 animate-pulse rounded-lg bg-border/40 motion-reduce:animate-none" />
        ))}
      </div>
    </div>
  );
}
