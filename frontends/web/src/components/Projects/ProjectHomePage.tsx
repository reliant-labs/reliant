// Copyright (c) 2026 Reliant Labs

/**
 * A project's Home (U6b, prototype): the project shell's first row,
 * `/projects/<id>`. Kept light — page content is the restyle's — it says what
 * needs the person, what they were last doing, and how the app is:
 *
 *   Needs attention   firing alerts and the app's errors in prod, and chats
 *                     waiting on an answer;
 *   Recent chats      the project's last five, each to its `/c/<id>`;
 *   Health            the prod environment's verdict and one headline number,
 *                     when the project has a deployed app; else the way to
 *                     deploy it.
 */

import { useMemo, type ReactNode } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { ChevronRight, MessageSquare } from "lucide-react";

import Status from "@/components/forge-ui/status";
import { useChatListWithWorkspaces } from "@/hooks/chat-queries";
import { useAlerts, useObservabilityOrg } from "@/hooks/observability-queries";
import { chatPath, projectChatsPath } from "@/lib/paths";
import { formatRelativeTime } from "@/lib/relativeTime";
import { cn } from "@/lib/utils";
import { ChatActivity, useActivityStore } from "@/store/activityStore";

import { verdictBusy, verdictTone } from "../Apps/appParts";
import { AppSectionEmptyState } from "../Forge/Console/AppSectionEmptyState";
import { useAppHealth, VERDICT_WORDS } from "../Forge/Console/ProjectsOverview";
import { consolePath } from "../Forge/Console/paths";
import { formatCount, formatDuration } from "../Forge/Observability/format";
import { deployedAppOf, rowForRef, sectionsAppOf } from "../Layout/sidebar/shellProject";
import { useSelectorCatalog } from "./ProjectSelector";

const RECENT = 5;

function Section({ title, children, testId }: { title: string; children: ReactNode; testId?: string }) {
  return (
    <section className="mt-8" data-testid={testId}>
      <h2 className="text-ui font-medium text-muted-foreground">{title}</h2>
      <div className="mt-2">{children}</div>
    </section>
  );
}

/** An in-app link: a plain click stays in the app, a modified one opens a tab. */
function RowLink({ href, children, testId }: { href: string; children: ReactNode; testId?: string }) {
  const navigate = useNavigate();
  return (
    <a
      href={href}
      data-testid={testId}
      onClick={(event) => {
        if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
        event.preventDefault();
        void navigate({ href });
      }}
      className="no-color flex h-10 items-center gap-3 rounded-md px-3 text-ui text-foreground transition-colors hover:bg-foreground/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
    >
      {children}
    </a>
  );
}

export function ProjectHomePage() {
  const { projectId = "" } = useParams({ strict: false }) as { projectId?: string };
  const { catalog } = useSelectorCatalog(undefined);
  const row = rowForRef(catalog, { kind: "project", id: projectId });
  const deployed = deployedAppOf(row);
  const app = sectionsAppOf(row);
  const name = row?.name ?? "Project";

  const { data: list, isPending } = useChatListWithWorkspaces(projectId || null, { enabled: !!projectId });
  const activities = useActivityStore((state) => state.activities);
  const chats = useMemo(() => (list?.chats ?? []).filter((chat) => chat.projectId === projectId), [list?.chats, projectId]);
  const waiting = chats.filter((chat) => activities.get(chat.id) === ChatActivity.AWAITING_INPUT);
  const recent = useMemo(
    () => [...chats].sort((a, b) => Date.parse(b.updatedAt || "") - Date.parse(a.updatedAt || "")).slice(0, RECENT),
    [chats],
  );

  const health = useAppHealth(deployed ?? "", !!deployed);
  const org = useObservabilityOrg();
  const alerts = useAlerts(org.orgId);
  const firing = (alerts.data ?? []).filter((alert) => alert.state === "firing" && !!health.prod && alert.envId === health.prod.id);
  const attention = waiting.length + firing.length + (health.errors ? 1 : 0);

  return (
    <div className="h-full overflow-y-auto" data-testid="project-home">
      <div className="mx-auto max-w-[var(--page-max)] px-8 pb-12 pt-8 max-md:px-4">
        <h1 className="text-title text-foreground">{name}</h1>
        <p className="mt-1 text-ui text-muted-foreground">{deployed ? `Deploys ${deployed}` : "No app deployed yet"}</p>

        <Section title="Needs attention" testId="project-home-attention">
          {attention === 0 ? (
            <p className="px-3 text-ui text-muted-foreground">Nothing needs you right now.</p>
          ) : (
            <div className="space-y-0.5">
              {waiting.map((chat) => (
                <RowLink key={chat.id} href={chatPath(chat.id)} testId={`project-home-waiting-${chat.id}`}>
                  <span role="img" aria-label="Waiting on you" className="size-2 shrink-0 rounded-full bg-warning" />
                  <span className="min-w-0 flex-1 truncate">{chat.title || "Untitled chat"}</span>
                  <span className="shrink-0 text-meta text-muted-foreground">waiting on you</span>
                </RowLink>
              ))}
              {firing.map((alert) => (
                <RowLink key={`${alert.envId}:${alert.rule}`} href={consolePath(deployed!, { kind: "obs-view", view: "alerts" })}>
                  <span role="img" aria-label="Firing" className="size-2 shrink-0 rounded-full bg-destructive" />
                  <span className="min-w-0 flex-1 truncate">{alert.rule}</span>
                  <span className="shrink-0 text-meta text-muted-foreground">alert firing</span>
                </RowLink>
              ))}
              {deployed && !!health.errors && (
                <RowLink href={consolePath(deployed, { kind: "obs-view", view: "errors" })}>
                  <span role="img" aria-label="Errors" className="size-2 shrink-0 rounded-full bg-destructive" />
                  <span className="min-w-0 flex-1 truncate">{formatCount(health.errors)} errors in prod</span>
                  <span className="shrink-0 text-meta text-muted-foreground">last 24 hours</span>
                </RowLink>
              )}
            </div>
          )}
        </Section>

        <Section title="Recent chats" testId="project-home-recent">
          {isPending && !list ? (
            <div className="space-y-1.5 px-3">
              {Array.from({ length: 3 }, (_, i) => (
                <div key={i} className="h-7 animate-pulse rounded-md bg-foreground/5 motion-reduce:animate-none" />
              ))}
            </div>
          ) : recent.length === 0 ? (
            <p className="px-3 text-ui text-muted-foreground">No chats in {name} yet.</p>
          ) : (
            <div className="space-y-0.5">
              {recent.map((chat) => (
                <RowLink key={chat.id} href={chatPath(chat.id)} testId={`project-home-chat-${chat.id}`}>
                  <MessageSquare className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                  <span className="min-w-0 flex-1 truncate">{chat.title || "Untitled chat"}</span>
                  {chat.updatedAt && <span className="shrink-0 text-meta text-muted-foreground">{formatRelativeTime(chat.updatedAt)}</span>}
                </RowLink>
              ))}
              <RowLink href={projectChatsPath(projectId)}>
                <span className="min-w-0 flex-1 text-muted-foreground">All chats</span>
                <ChevronRight className="size-4 text-muted-foreground" aria-hidden="true" />
              </RowLink>
            </div>
          )}
        </Section>

        {deployed ? (
          <Section title="Health" testId="project-home-health">
            <RowLink href={consolePath(deployed, { kind: "overview" })}>
              {health.prod ? (
                <Status
                  tone={verdictTone(health.prod.verdict)}
                  busy={verdictBusy(health.prod.verdict)}
                  label={`prod · ${VERDICT_WORDS[health.prod.verdict] ?? "Unknown"}`}
                />
              ) : (
                <span className="text-muted-foreground">prod · No data</span>
              )}
              <span className="flex-1" />
              <span className={cn("tabular-nums text-muted-foreground")}>
                {health.requests === null ? "—" : `${formatCount(health.requests)} requests`}
                {health.p95 === null ? "" : ` · p95 ${formatDuration(health.p95)}`}
              </span>
              <ChevronRight className="size-4 text-muted-foreground" aria-hidden="true" />
            </RowLink>
          </Section>
        ) : (
          <Section title="Health" testId="project-home-deploy">
            <div className="forge-ui -mt-12">
              <AppSectionEmptyState page={{ kind: "overview" }} projectId={projectId} title={`${app ?? name} isn't deployed yet`} />
            </div>
          </Section>
        )}
      </div>
    </div>
  );
}
