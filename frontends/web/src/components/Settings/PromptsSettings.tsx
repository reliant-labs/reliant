import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Check, Loader2, Plus, Trash2 } from "lucide-react";
import Button from "@/components/forge-ui/button";
import {
  useProjectPrompts,
  useSaveProjectPrompts,
  type Prompt as ProjectPrompt,
} from "../../hooks/project-queries";
import { toast } from "../../lib/toast-manager";
import { useProjectStore } from "../../store/projectStore";
import { SettingsPageHeader } from "./SettingsPageHeader";
import { SettingsProjectScope } from "./SettingsProjectScope";

interface Prompt {
  id: string;
  title: string;
  content: string;
  category?: string;
}

/** How long typing must pause before an edit saves. */
const PROMPT_AUTOSAVE_DELAY_MS = 800;

// The server's prompt as this page edits it.
function promptToView(p: ProjectPrompt): Prompt {
  return {
    id: p.id,
    title: p.name, // the server calls it 'name', this page 'title'
    content: p.content,
    category: p.description, // category is saved as the description
  };
}

function promptToProto(p: Prompt) {
  return {
    id: p.id,
    name: p.title,
    content: p.content,
    description: p.category || "",
  };
}

type SaveState = "idle" | "saving" | "saved" | "error";

interface SaveJob {
  projectId: string;
  next: Prompt[];
  /** editSeq when queued: a newer edit makes this save's outcome stale. */
  seq: number;
  /** scopeGen when queued: a project switch makes it stale too. */
  gen: number;
}

interface PromptsSettingsProps {
  projectId?: string;
}

/**
 * Settings → Prompts, for the project in the header's scope picker.
 *
 * Edits save themselves, like every other setting: a pause in typing saves
 * the set, the status beside the search says Saving… / Saved, and a toast
 * confirms it (one toast, updated in place). A failed save says so with a
 * Retry, and the edit stays on screen until it lands.
 */
export function PromptsSettings({ projectId }: PromptsSettingsProps) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const [filter, setFilter] = useState("");

  const effectiveProjectId = projectId || currentProject?.id;

  const serverPrompts = useProjectPrompts(effectiveProjectId);
  const savePrompts = useSaveProjectPrompts();

  // The edit not yet confirmed by the server. Null while the page shows the
  // server's prompts, so a refetch never overwrites an edit in progress.
  const [draft, setDraft] = useState<Prompt[] | null>(null);
  const [saveState, setSaveState] = useState<SaveState>("idle");
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  // The edit the timer will save, so a project switch or unmount can save it
  // now instead of dropping it.
  const pendingEdit = useRef<Prompt[] | null>(null);
  // Each edit bumps this; a save that finishes after a newer edit leaves the
  // newer edit alone.
  const editSeq = useRef(0);
  // Bumped when the project changes: a save for the previous project still
  // lands, but no longer touches this page's state.
  const scopeGen = useRef(0);
  // Every save sends the whole set, so saves run one at a time: two in flight
  // could land out of order and an older set overwrite a newer one. Edits made
  // while one is in flight wait here, newest snapshot per project only.
  const queue = useRef<SaveJob[]>([]);
  const draining = useRef(false);
  const mutateRef = useRef(savePrompts.mutateAsync);
  mutateRef.current = savePrompts.mutateAsync;

  const drain = useCallback(async () => {
    if (draining.current) return;
    draining.current = true;
    try {
      for (let job = queue.current.shift(); job; job = queue.current.shift()) {
        const current = () => job.gen === scopeGen.current && job.seq === editSeq.current && queue.current.length === 0;
        if (job.gen === scopeGen.current) setSaveState("saving");
        // A prompt still without a title is kept on screen but not saved yet.
        const complete = job.next
          .map((p) => ({ ...p, id: p.id.trim(), title: p.title.trim() }))
          .filter((p) => p.id && p.title);
        try {
          await mutateRef.current({ projectId: job.projectId, prompts: complete.map(promptToProto) });
          if (!current()) continue;
          setSaveState("saved");
          toast.success("Prompts saved", { id: "prompts-autosave" });
          // The cache now holds what was saved; show it, unless an untitled
          // prompt is still being written.
          if (complete.length === job.next.length) setDraft(null);
        } catch (e) {
          console.error(e);
          if (!current()) continue;
          setSaveState("error");
          toast.error("Couldn't save prompts", { id: "prompts-autosave" });
        }
      }
    } finally {
      draining.current = false;
    }
  }, []);

  const enqueue = useCallback(
    (projectId: string, next: Prompt[]) => {
      const job = { projectId, next, seq: editSeq.current, gen: scopeGen.current };
      queue.current = [...queue.current.filter((j) => j.projectId !== projectId), job];
      void drain();
    },
    [drain],
  );

  useEffect(() => {
    setDraft(null);
    setSaveState("idle");
    return () => {
      // Leaving this project (or the page): save its unsaved edit now, then
      // let nothing from it touch the next project's state.
      if (timer.current) clearTimeout(timer.current);
      timer.current = null;
      if (effectiveProjectId && pendingEdit.current) enqueue(effectiveProjectId, pendingEdit.current);
      pendingEdit.current = null;
      scopeGen.current += 1;
    };
  }, [effectiveProjectId, enqueue]);

  const saved = useMemo(() => serverPrompts.prompts.map(promptToView), [serverPrompts.prompts]);
  const prompts = draft ?? saved;
  const loading = !!effectiveProjectId && serverPrompts.isPending;

  const commit = useCallback(
    (next: Prompt[]) => {
      if (!effectiveProjectId) return;
      enqueue(effectiveProjectId, next);
    },
    [effectiveProjectId, enqueue],
  );

  const edit = (change: (prev: Prompt[]) => Prompt[]) => {
    const next = change(draft ?? saved);
    editSeq.current += 1;
    setDraft(next);
    setSaveState("idle");
    if (timer.current) clearTimeout(timer.current);
    pendingEdit.current = next;
    timer.current = setTimeout(() => {
      timer.current = null;
      pendingEdit.current = null;
      commit(next);
    }, PROMPT_AUTOSAVE_DELAY_MS);
  };

  const handleAdd = () => {
    const idBase = `prompt_${Date.now()}`;
    edit((prev) => [...prev, { id: idBase, title: "New prompt", content: "", category: "" }]);
  };

  const handleDelete = (id: string) => {
    edit((prev) => prev.filter((p) => p.id !== id));
  };

  const handleChange = (id: string, field: "title" | "content" | "category", value: string) => {
    edit((prev) => prev.map((p) => (p.id === id ? { ...p, [field]: value } : p)));
  };

  const filtered = useMemo(() => {
    const q = filter.toLowerCase();
    if (!q) return prompts;
    return prompts.filter(
      (p) =>
        p.title.toLowerCase().includes(q) ||
        p.content.toLowerCase().includes(q) ||
        (p.category || "").toLowerCase().includes(q),
    );
  }, [prompts, filter]);

  const loadError = serverPrompts.error ? "Couldn't load prompts." : null;

  return (
    <div className="space-y-4">
      <div data-onboarding="prompts-settings">
        <SettingsPageHeader
          title="Prompts"
          description="Save snippets of instructions you type often, like a code-review checklist or a house style, so you can drop them into any message instead of retyping them."
          meta={<SettingsProjectScope hint="Prompts are saved with this project" />}
        />
      </div>

      <div className="flex items-center gap-2">
        <input
          type="search"
          aria-label="Search prompts"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Search prompts…"
          className="flex-1 px-3 py-2 border border-input bg-background rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-ring"
        />
        <Button
          variant="secondary"
          size="sm"
          onClick={handleAdd}
          disabled={!effectiveProjectId}
          title={effectiveProjectId ? undefined : "Pick a project first: prompts are saved with a project"}
          leftIcon={<Plus className="h-3.5 w-3.5" />}
        >
          New prompt
        </Button>
        <SaveStatus state={saveState} onRetry={() => commit(prompts)} />
      </div>

      {loadError && (
        <div className="p-3 border border-destructive/40 bg-destructive/10 text-destructive-ink rounded-md text-xs">
          {loadError}
        </div>
      )}

      <div className="space-y-3">
        {!effectiveProjectId ? (
          <div className="p-6 text-sm text-muted-foreground rounded-lg border border-border bg-card">
            Pick a project above to see and add its prompts.
          </div>
        ) : loading ? (
          <div className="p-6 text-sm text-muted-foreground">Loading prompts…</div>
        ) : filtered.length === 0 ? (
          <div className="p-6 text-sm text-muted-foreground rounded-lg border border-border bg-card">
            {filter ? "No prompts match your search." : "No prompts yet. Choose New prompt to add your first one."}
          </div>
        ) : (
          filtered.map((p) => (
            <div key={p.id} className="rounded-lg border border-border bg-card p-3">
              <div className="flex items-center gap-2 mb-2">
                <input
                  aria-label="Prompt title"
                  value={p.title}
                  onChange={(e) => handleChange(p.id, "title", e.target.value)}
                  placeholder="Title"
                  className="flex-1 px-2 py-1 border border-input bg-background rounded text-sm"
                />
                <input
                  aria-label="Category"
                  value={p.category || ""}
                  onChange={(e) => handleChange(p.id, "category", e.target.value)}
                  placeholder="Category"
                  className="w-40 px-2 py-1 border border-input bg-background rounded text-xs"
                />
                <button
                  type="button"
                  onClick={() => handleDelete(p.id)}
                  aria-label={`Delete ${p.title || "prompt"}`}
                  className="ml-2 p-2 rounded hover:bg-destructive/10 text-destructive-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <Trash2 className="w-4 h-4" />
                </button>
              </div>
              <textarea
                aria-label="Prompt content"
                value={p.content}
                onChange={(e) => handleChange(p.id, "content", e.target.value)}
                placeholder="Prompt content…"
                rows={4}
                className="w-full px-2 py-2 border border-input bg-background rounded text-sm"
              />
            </div>
          ))
        )}
      </div>
    </div>
  );
}

function SaveStatus({ state, onRetry }: { state: SaveState; onRetry: () => void }) {
  return (
    <span role="status" aria-live="polite" className="min-w-[7rem] text-right text-xs text-muted-foreground">
      {state === "saving" && (
        <span className="inline-flex items-center gap-1">
          <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" /> Saving…
        </span>
      )}
      {state === "saved" && (
        <span className="inline-flex items-center gap-1">
          <Check className="h-3 w-3" aria-hidden="true" /> Saved
        </span>
      )}
      {state === "error" && (
        <span className="inline-flex items-center gap-2 text-destructive-ink">
          Couldn't save
          <button type="button" onClick={onRetry} className="font-medium underline">
            Retry
          </button>
        </span>
      )}
    </span>
  );
}
