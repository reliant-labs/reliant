import { useEffect, useMemo, useState } from "react";
import { projectGrpc, type Prompt as GrpcPrompt } from "../../api/project-grpc";
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

interface PromptsSettingsProps {
  projectId?: string;
}

export function PromptsSettings({ projectId }: PromptsSettingsProps) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const [prompts, setPrompts] = useState<Prompt[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isDirty, setIsDirty] = useState(false);
  const [filter, setFilter] = useState("");

  const effectiveProjectId = projectId || currentProject?.id;

  // Helper function to convert gRPC prompts to frontend format
  const grpcToFrontend = (p: GrpcPrompt): Prompt => ({
    id: p.id,
    title: p.name, // gRPC uses 'name', frontend uses 'title'
    content: p.content,
    category: "", // gRPC doesn't have category, default to empty
    default: false, // gRPC doesn't track default flag
  });

  // Helper function to convert frontend prompts to gRPC format
  const frontendToGrpc = (p: Prompt): GrpcPrompt => ({
    id: p.id,
    name: p.title, // frontend uses 'title', gRPC uses 'name'
    content: p.content,
    description: p.category || "", // Map category to description
    created_at: "",
    updated_at: "",
  });

  useEffect(() => {
    if (!effectiveProjectId) {
      setPrompts([]);
      setIsDirty(false);
      return;
    }
    const load = async () => {
      setLoading(true);
      setError(null);
      try {
        const grpcPrompts = await projectGrpc.getPrompts(effectiveProjectId);
        setPrompts(grpcPrompts.map(grpcToFrontend));
        setIsDirty(false);
      } catch (e) {
        console.error(e);
        setError("Failed to load prompts");
      } finally {
        setLoading(false);
      }
    };
    load();
  }, [effectiveProjectId]);


  const handleAdd = () => {
    const idBase = `prompt_${Date.now()}`;
    setPrompts((prev) => [
      ...prev,
      { id: idBase, title: "New Prompt", content: "", category: "General" },
    ]);
    setIsDirty(true);
  };

  const handleDelete = (id: string) => {
    setPrompts((prev) => prev.filter((p) => p.id !== id));
    setIsDirty(true);
  };

  const handleChange = (id: string, field: keyof Prompt, value: string | boolean) => {
    setPrompts((prev) => prev.map((p) => (p.id === id ? { ...p, [field]: value } : p)));
    setIsDirty(true);
  };

  const handleSave = async () => {
    if (!effectiveProjectId) return;
    setSaving(true);
    setError(null);
    try {
      const cleaned = prompts
        .map((p) => ({ ...p, id: p.id.trim(), title: p.title.trim() }))
        .filter((p) => p.id && p.title);

      const grpcPrompts = cleaned.map(frontendToGrpc);
      const result = await projectGrpc.savePrompts(effectiveProjectId, grpcPrompts);
      setPrompts(result.prompts.map(grpcToFrontend));
      setIsDirty(false);
    } catch (e) {
      console.error(e);
      setError("Failed to save prompts");
    } finally {
      setSaving(false);
    }
  };

  const handleDiscard = async () => {
    // Reload from server
    if (!effectiveProjectId) return;
    setLoading(true);
    try {
      const grpcPrompts = await projectGrpc.getPrompts(effectiveProjectId);
      setPrompts(grpcPrompts.map(grpcToFrontend));
      setIsDirty(false);
    } catch (e) {
      console.error(e);
      setError("Failed to reload prompts");
    } finally {
      setLoading(false);
    }
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
        <h2 className="text-base font-semibold">Prompts</h2>
        <p className="text-sm text-muted-foreground">Create reusable preambles and instructions to quickly compose better messages.</p>
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
        <div className="p-3 border border-destructive/40 bg-destructive/10 text-destructive rounded-md text-xs">{error}</div>
      )}

      <div className="space-y-3">
        {loading ? (
          <div className="p-6 text-sm text-muted-foreground">Loading prompts…</div>
        ) : filtered.length === 0 ? (
          <div className="p-6 text-sm text-muted-foreground border border-border/40 rounded-md">
            No prompts yet. Click New to add your first prompt.
          </div>
        ) : (
          filtered.map((p) => (
            <div key={p.id} className="border border-border/40 rounded-lg p-3 bg-card">
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
                <button onClick={() => handleDelete(p.id)} className="ml-2 p-2 rounded hover:bg-destructive/10 text-destructive">
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