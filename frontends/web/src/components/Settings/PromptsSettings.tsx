import { useEffect, useMemo, useState } from "react";
import {
  useProjectPrompts,
  useSaveProjectPrompts,
  type Prompt as ProjectPrompt,
} from "../../hooks/project-queries";
import { useProjectStore } from "../../store/projectStore";

interface Prompt {
  id: string;
  title: string;
  content: string;
  default?: boolean;
  hotkey?: string;
  category?: string;
}
import { Plus, Trash2, Loader2 } from "lucide-react";
import { SettingsPageHeader } from "./SettingsPageHeader";

// The server's prompt as this page edits it.
function promptToView(p: ProjectPrompt): Prompt {
  return {
    id: p.id,
    title: p.name, // the server calls it 'name', this page 'title'
    content: p.content,
    category: "", // the server has no category
    default: false, // the server does not track a default flag
  };
}

function promptToProto(p: Prompt) {
  return {
    id: p.id,
    name: p.title,
    content: p.content,
    description: p.category || "", // category is saved as the description
  };
}

interface PromptsSettingsProps {
  projectId?: string;
}

export function PromptsSettings({ projectId }: PromptsSettingsProps) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [filter, setFilter] = useState("");

  const effectiveProjectId = projectId || currentProject?.id;

  const serverPrompts = useProjectPrompts(effectiveProjectId);
  const savePrompts = useSaveProjectPrompts();

  // The unsaved edit. Null while the page shows the server's prompts, so a
  // refetch can never overwrite an edit in progress and an untouched page
  // always shows the latest saved set.
  const [draft, setDraft] = useState<Prompt[] | null>(null);
  useEffect(() => {
    setDraft(null);
    setSaveError(null);
  }, [effectiveProjectId]);

  const saved = useMemo(() => serverPrompts.prompts.map(promptToView), [serverPrompts.prompts]);
  const prompts = draft ?? saved;
  const isDirty = draft !== null;
  const loading = !!effectiveProjectId && serverPrompts.isPending;
  const saving = savePrompts.isPending;
  const error = saveError ?? (serverPrompts.error ? "Failed to load prompts" : null);

  const edit = (change: (prev: Prompt[]) => Prompt[]) => setDraft((prev) => change(prev ?? saved));

  const handleAdd = () => {
    const idBase = `prompt_${Date.now()}`;
    edit((prev) => [
      ...prev,
      { id: idBase, title: "New Prompt", content: "", category: "General" },
    ]);
  };

  const handleDelete = (id: string) => {
    edit((prev) => prev.filter((p) => p.id !== id));
  };

  const handleChange = (id: string, field: keyof Prompt, value: string | boolean) => {
    edit((prev) => prev.map((p) => (p.id === id ? { ...p, [field]: value } : p)));
  };

  const handleSave = async () => {
    if (!effectiveProjectId) return;
    setSaveError(null);
    try {
      const cleaned = prompts
        .map((p) => ({ ...p, id: p.id.trim(), title: p.title.trim() }))
        .filter((p) => p.id && p.title);

      await savePrompts.mutateAsync({
        projectId: effectiveProjectId,
        prompts: cleaned.map(promptToProto),
      });
      setDraft(null);
    } catch (e) {
      console.error(e);
      setSaveError("Failed to save prompts");
    }
  };

  const handleDiscard = async () => {
    // Drop the edit and re-read the server's set.
    setDraft(null);
    setSaveError(null);
    await serverPrompts.refetch();
  };

  const filtered = useMemo(() => {
    const q = filter.toLowerCase();
    if (!q) return prompts;
    return prompts.filter(
      (p) => p.title.toLowerCase().includes(q) || p.content.toLowerCase().includes(q) || (p.category || "").toLowerCase().includes(q)
    );
  }, [prompts, filter]);

  return (
    <div className="space-y-4">
      <div data-onboarding="prompts-settings">
        <SettingsPageHeader
        title="Prompts"
        description="Save snippets of instructions you type often, like a code-review checklist or a house style, so you can drop them into any message instead of retyping them."
      />
      </div>

      <div className="flex gap-2 items-center">
        <input
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Search prompts…"
          className="flex-1 px-3 py-2 border border-input bg-background rounded-md text-sm"
        />
        <button onClick={handleAdd} disabled={!effectiveProjectId} className="px-3 py-2 text-xs font-medium bg-secondary text-secondary-foreground hover:bg-secondary/80 rounded-md disabled:opacity-50">
          <Plus className="w-3 h-3 inline mr-1" /> New
        </button>
        {saving && (
          <div className="text-xs min-w-[120px] text-right text-muted-foreground">
            <Loader2 className="w-3 h-3 inline animate-spin" /> Saving…
          </div>
        )}
      </div>

      {error && (
        <div className="p-3 border border-destructive/40 bg-destructive/10 text-destructive-ink rounded-md text-xs">{error}</div>
      )}

      <div className="space-y-3">
        {loading ? (
          <div className="p-6 text-sm text-muted-foreground">Loading prompts…</div>
        ) : filtered.length === 0 ? (
          <div className="p-6 text-sm text-muted-foreground rounded-lg border border-border bg-card">
            No prompts yet. Click New to add your first prompt.
          </div>
        ) : (
          filtered.map((p) => (
            <div key={p.id} className="rounded-lg border border-border bg-card p-3">
              <div className="flex items-center gap-2 mb-2">

                <input
                  value={p.title}
                  onChange={(e) => handleChange(p.id, "title", e.target.value)}
                  placeholder="Title"
                  className="flex-1 px-2 py-1 border border-input bg-background rounded text-sm"
                />
                <input
                  value={p.category || ""}
                  onChange={(e) => handleChange(p.id, "category", e.target.value)}
                  placeholder="Category"
                  className="w-40 px-2 py-1 border border-input bg-background rounded text-xs"
                />
                <label className="text-xs flex items-center gap-2 ml-2">
                  <input type="checkbox" checked={!!p.default} onChange={(e) => handleChange(p.id, "default", e.target.checked)} /> Default
                </label>
                <button onClick={() => handleDelete(p.id)} className="ml-2 p-2 rounded hover:bg-destructive/10 text-destructive-ink">
                  <Trash2 className="w-4 h-4" />
                </button>
              </div>
              <textarea
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

      {isDirty && (
        <div className="sticky bottom-0 left-0 right-0 bg-card border-t border-border/40 p-3 mt-2">
          <div className="max-w-4xl mx-auto flex items-center justify-between">
            <span className="text-xs text-muted-foreground">You have unsaved changes</span>
            <div className="flex items-center gap-2">
              <button
                onClick={handleDiscard}
                disabled={loading || saving}
                className="px-3 py-2 text-xs border border-input bg-background hover:bg-accent hover:text-accent-foreground rounded-md disabled:opacity-50"
              >
                Discard
              </button>
              <button
                onClick={handleSave}
                disabled={loading || saving || !effectiveProjectId}
                className="px-3 py-2 text-xs font-medium bg-primary text-primary-foreground hover:bg-primary/90 rounded-md disabled:opacity-50"
              >
                Save Changes
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}