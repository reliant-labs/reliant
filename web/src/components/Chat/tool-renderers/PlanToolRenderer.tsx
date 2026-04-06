/**
 * Renderer for plan/task tools (create_plan, update_task, add_task)
 */

import { memo } from 'react';
import type { ToolContentProps } from './types';
import { MarkdownRenderer } from '../MarkdownRenderer';

// Type for task items
type TaskItem = string | {
  title?: string;
  description?: string;
}

function PlanToolRendererComponent({ ctx }: ToolContentProps) {
  const { toolName, input, worktreeId } = ctx;
  const toolNameLower = toolName.toLowerCase();

  // Parse input data
  let data = typeof input === 'string' ? {} : input;
  if (data?.command && typeof data.command === 'string') {
    try {
      data = JSON.parse(data.command);
    } catch {
      // Keep original
    }
  }

  if (toolNameLower === 'create_plan') {
    return <CreatePlanContent data={data} worktreeId={worktreeId} />;
  }

  if (toolNameLower === 'update_task' || toolNameLower === 'add_task') {
    return <TaskContent data={data} isAddTask={toolNameLower === 'add_task'} />;
  }

  return null;
}

function CreatePlanContent({ 
  data, 
  worktreeId 
}: { 
  data: Record<string, unknown>; 
  worktreeId?: string;
}) {
  const title = (data?.title || data?.Title) as string;
  const description = (data?.description || data?.Description) as string;
  const complexity = (data?.complexity || data?.Complexity) as string;
  const rawTasks = data?.tasks || data?.Tasks;
  const tasks = Array.isArray(rawTasks) ? rawTasks as TaskItem[] : undefined;

  if (!title && !description && (!tasks || tasks.length === 0)) {
    return (
      <div className="px-2 py-1.5 text-[11px] text-muted-foreground">
        Plan data is being prepared...
      </div>
    );
  }

  return (
    <div className="tool-content-plan">
      {/* Header */}
      <div className="px-2 py-1.5 border-b border-border/30 bg-muted/20">
        {title && (
          <h3 className="text-sm font-semibold text-foreground">
            {title}
          </h3>
        )}
        {complexity && (
          <div className="inline-flex items-center px-1.5 py-0.5 mt-1 rounded-full text-[10px] font-medium bg-info/10 text-info border border-info/30">
            {complexity.charAt(0).toUpperCase() + complexity.slice(1)} Complexity
          </div>
        )}
      </div>

      {/* Description */}
      {description && (
        <div className="px-2 py-1.5 border-b border-border/30 text-[11px]">
          <MarkdownRenderer content={description} worktreeId={worktreeId} />
        </div>
      )}

      {/* Tasks List */}
      {tasks && tasks.length > 0 && (
        <div className="px-2 py-1.5">
          <h4 className="font-medium text-foreground text-[11px] mb-1">
            Tasks ({tasks.length})
          </h4>
          <div className="space-y-1">
            {tasks.map((task, index) => (
              <div key={index} className="flex items-start gap-1.5">
                <div className="flex-shrink-0 w-3 h-3 mt-0.5 border-2 border-muted-foreground rounded-sm" />
                <span className="text-[11px] text-foreground leading-relaxed">
                  {typeof task === "string"
                    ? task
                    : task.title || task.description || JSON.stringify(task)}
                </span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function TaskContent({ 
  data, 
  isAddTask: _isAddTask 
}: { 
  data: Record<string, unknown>; 
  isAddTask: boolean;
}) {
  const description = (data?.description || data?.Description) as string;
  const metadata = data?.metadata as Record<string, unknown> | undefined;
  const notes = metadata?.notes as string | undefined;

  // Only show if there's meaningful content
  if (!description && !notes) {
    return null;
  }

  return (
    <div className="tool-content-task px-2 py-1.5 text-[11px] text-muted-foreground leading-relaxed">
      {description && <p>{description}</p>}
      {notes && (
        <p className={description ? "mt-1 text-[10px]" : ""}>
          {notes}
        </p>
      )}
    </div>
  );
}

export const PlanToolRenderer = memo(PlanToolRendererComponent);