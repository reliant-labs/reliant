import { Modal } from "../ui/Modal";
import { Textarea } from "../ui/Textarea";
import { Calendar, Lock } from "lucide-react";

interface WorkflowInfoPopoverProps {
  isOpen: boolean;
  onClose: () => void;
  /** What people see in every list. */
  title: string;
  /** The `name:` that references, links and automations use. */
  slug: string;
  /** The slug as last saved, to warn when an edit would change it. */
  savedSlug?: string;
  description: string;
  /** Absent when the workflow is read-only. */
  onChange?: (patch: { title?: string; slug?: string; description?: string }) => void;
  /** Applied to the slug when its field loses focus. */
  normalizeSlug?: (value: string) => string;
  createdAt?: string;
}

function formatDate(dateString?: string): string {
  if (!dateString) return "—";
  try {
    const date = new Date(dateString);
    return date.toLocaleDateString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  } catch {
    return "—";
  }
}

const inputClass =
  "w-full rounded-lg border border-border bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20";

export function WorkflowInfoPopover({
  isOpen,
  onClose,
  title,
  slug,
  savedSlug,
  description,
  onChange,
  normalizeSlug = (value) => value,
  createdAt,
}: WorkflowInfoPopoverProps) {
  const isEditable = !!onChange;
  const slugChanged = !!savedSlug && slug !== savedSlug;
  return (
    <Modal isOpen={isOpen} onClose={onClose} title="Workflow Info" size="sm">
      <div className="space-y-4">
        <div className="space-y-1.5">
          <label htmlFor="workflow-info-title" className="block text-sm font-medium text-muted-foreground">
            Title
          </label>
          {isEditable ? (
            <input
              id="workflow-info-title"
              type="text"
              value={title}
              onChange={(e) => onChange({ title: e.target.value })}
              placeholder={slug}
              className={inputClass}
            />
          ) : (
            <p className="text-sm text-foreground">{title || slug}</p>
          )}
        </div>

        <div className="space-y-1.5">
          <label htmlFor="workflow-info-slug" className="block text-sm font-medium text-muted-foreground">
            Slug
          </label>
          {isEditable ? (
            <input
              id="workflow-info-slug"
              type="text"
              value={slug}
              onChange={(e) => onChange({ slug: e.target.value })}
              onBlur={() => onChange({ slug: normalizeSlug(slug) })}
              className={`${inputClass} font-mono`}
            />
          ) : (
            <p className="font-mono text-sm text-foreground">{slug}</p>
          )}
          <p className={slugChanged ? "text-xs text-warning-ink" : "text-xs text-muted-foreground"}>
            {slugChanged
              ? `Saving moves it from “${savedSlug}”. Workflows and automations that use the old slug will stop finding it.`
              : "How other workflows, automations and links refer to this one."}
          </p>
        </div>

        <div className="space-y-1.5">
          <label htmlFor="workflow-info-description" className="block text-sm font-medium text-muted-foreground">
            Description
          </label>
          {isEditable ? (
            <Textarea
              id="workflow-info-description"
              value={description}
              onChange={(e) => onChange({ description: e.target.value })}
              placeholder="Add a description for this workflow..."
              className="resize-none"
              rows={3}
            />
          ) : (
            <p className="text-sm text-foreground">
              {description || <span className="text-muted-foreground italic">No description</span>}
            </p>
          )}
        </div>

        {createdAt && (
          <div className="grid grid-cols-2 gap-4 pt-2 border-t border-border">
            <div>
              <span className="block text-xs font-medium text-muted-foreground mb-1.5">Created</span>
              <div className="flex items-center gap-1.5 text-sm text-foreground">
                <Calendar className="w-4 h-4 text-muted-foreground" />
                <span>{formatDate(createdAt)}</span>
              </div>
            </div>
          </div>
        )}

        {!isEditable && (
          <p className="text-xs text-muted-foreground flex items-center gap-1.5 pt-2 border-t border-border">
            <Lock className="w-3 h-3" />
            This workflow is read-only
          </p>
        )}
      </div>
    </Modal>
  );
}
