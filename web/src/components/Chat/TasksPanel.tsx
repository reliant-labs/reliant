import { memo, useMemo, useState } from "react";
import { CheckCircle2, Circle, CircleDashed, AlertOctagon, Zap, Ban, ListTodo } from "lucide-react";
import { useTasksStore, type TaskItem } from "../../store/tasksStore";
import { useChatStore } from "../../store/chatStore";
import { cn } from "../../lib/utils";
import { SidebarSection, SidebarEmptyState } from "../RightSidebar/shared";

function statusBadge(status: TaskItem["status"]) {
  switch (status) {
    case "completed":
      return <CheckCircle2 className="w-4 h-4 text-success" />;
    case "in_progress":
      return <Zap className="w-4 h-4 text-primary animate-pulse" />;
    case "blocked":
      return <AlertOctagon className="w-4 h-4 text-warning" />;
    case "failed":
      return <AlertOctagon className="w-4 h-4 text-destructive" />;
    case "skipped":
      return <CircleDashed className="w-4 h-4 text-muted-foreground" />;
    case "cancelled":
      return <Ban className="w-4 h-4 text-muted-foreground" />;
    default:
      return <Circle className="w-4 h-4 text-muted-foreground" />;
  }
}

interface TasksPanelProps {
  chatId?: string;
}

function TasksPanelComponent({ chatId: propChatId }: TasksPanelProps) {
  // Use provided chatId or fall back to active chat
  const activeChatId = useChatStore((state) => state.activeChatId);
  const chatId = propChatId ?? activeChatId;

  // Subscribe to the tasks object for this chatId
  const tasksByChat = useTasksStore((state) => state.tasksByChat);
  const tasks = useMemo(
    () => (chatId && tasksByChat[chatId] ? Object.values(tasksByChat[chatId]) : []),
    [chatId, tasksByChat]
  );

  // Calculate stats from tasks
  const total = tasks.length;
  const completed = tasks.filter((t) => t.status === "completed").length;
  const percent = total > 0 ? Math.round((completed / total) * 100) : 0;
  const stats = { total, completed, percent };

  const sortedTasks = useMemo(() => {
    // Stable sort: incomplete first, then by createdAt (creation order)
    return [...tasks].sort((a, b) => {
      // 1. Incomplete tasks first
      const aDone = a.status === "completed";
      const bDone = b.status === "completed";
      if (aDone !== bDone) return aDone ? 1 : -1;

      // 2. Sort by creation time (earliest first) to maintain order
      const aCreated = a.createdAt ? new Date(a.createdAt).getTime() : 0;
      const bCreated = b.createdAt ? new Date(b.createdAt).getTime() : 0;
      if (aCreated !== bCreated) return aCreated - bCreated;

      // 3. Fallback to task ID for absolute stability
      return a.id.localeCompare(b.id);
    });
  }, [tasks]);

  // Optional: lightweight grouping by status for quick scanning
  const groups = useMemo(() => {
    const g: Record<string, TaskItem[]> = {};
    for (const t of sortedTasks) {
      const key = t.status;
      (g[key] ||= []).push(t);
    }
    return g;
  }, [sortedTasks]);

  // Track expanded sections - all expanded by default
  const [expandedSections, setExpandedSections] = useState<Set<string>>(
    new Set(["in_progress", "pending", "completed", "failed", "blocked", "skipped", "cancelled"])
  );

  const toggleSection = (status: string) => {
    setExpandedSections((prev) => {
      const next = new Set(prev);
      if (next.has(status)) {
        next.delete(status);
      } else {
        next.add(status);
      }
      return next;
    });
  };

  if (!chatId) {
    return (
      <SidebarEmptyState
        icon={ListTodo}
        title="No chat selected"
        description="Select a chat to view its tasks"
        className="h-full"
      />
    );
  }

  return (
    <div className="flex flex-col h-full">
      {/* Progress */}
      <div className="px-4 py-3 border-b border-border/60">
        <div className="h-2 w-full bg-muted rounded">
          <div className="h-2 bg-success rounded transition-all" style={{ width: `${stats.percent}%` }} />
        </div>
        <div className="mt-2 text-xs text-muted-foreground">
          {stats.total > 0 ? `${stats.percent}% complete (${stats.completed}/${stats.total})` : "No tasks yet"}
        </div>
      </div>

      {/* List */}
      <div className="flex-1 overflow-y-auto">
        {sortedTasks.length === 0 ? (
          <SidebarEmptyState
            icon={ListTodo}
            title="No tasks yet"
            description="Tasks will appear here as the agent works"
            size="sm"
          />
        ) : (
          Object.entries(groups).sort().map(([status, items]) => (
            <SidebarSection
              key={status}
              title={status.replace(/_/g, " ")}
              count={items.length}
              isExpanded={expandedSections.has(status)}
              onToggle={() => toggleSection(status)}
              variant={status === "in_progress" ? "highlighted" : "default"}
            >
              <div className="space-y-1.5 px-3">
                {items.map((task) => (
                  <div
                    key={task.id}
                    className={cn(
                      "p-2 border border-border/60 rounded bg-background/40",
                      task.status === "completed" && "opacity-80"
                    )}
                  >
                    <div className="flex items-start gap-2">
                      <div className="mt-0.5 flex-shrink-0">{statusBadge(task.status)}</div>
                      <div className="min-w-0 flex-1">
                        <div className="text-sm font-medium truncate" title={task.title}>
                          {task.title}
                        </div>
                        {task.description && (
                          <div className="text-xs text-muted-foreground mt-0.5 whitespace-pre-wrap break-words">
                            {task.description}
                          </div>
                        )}
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            </SidebarSection>
          ))
        )}
      </div>
    </div>
  );
}

export const TasksPanel = memo(TasksPanelComponent);
