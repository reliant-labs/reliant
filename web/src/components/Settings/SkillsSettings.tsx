import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { useProjectStore } from "../../store/projectStore";
import { filesystemGrpc } from "../../api/filesystem-grpc";
import { settingsGrpc } from "../../api/settings-grpc";
import { Button } from "../ui/Button";
import { Modal } from "../ui/Modal";
import { Toggle } from "../ui/Toggle";
import { Tooltip } from "../ui/Tooltip";
import { toast } from "sonner";
import {
  RefreshCw,
  FolderDown,
  FolderOpen,
  ShieldCheck,
  Link,
  Trash2,
  Settings,
  Pencil,
  Palette,
} from "lucide-react";
import { LuBookDown, LuBookPlus } from "react-icons/lu";
import { FaFileWord, FaFilePdf, FaFileExcel, FaFilePowerpoint } from "react-icons/fa";
import playwrightIcon from "../../assets/skill-icons/playwright.svg";
import { MarkdownRenderer } from "../Chat/MarkdownRenderer";
import { openExternalLink } from "../../lib/open-link";
import { McpIcon } from "../icons/McpIcon";
import type {
  InstalledSkill as InstalledSkillConfig,
  RecommendedSkill as RecommendedSkillConfig,
  SkillDiscoveryDiagnostic as SkillDiscoveryDiagnosticConfig,
} from "../../api/settings-grpc";

type SkillScope = "project_local" | "project" | "global" | "builtin";

type SkillItem = {
  id: string;
  name: string;
  description: string;
  scope: SkillScope;
  dirPath: string;
  definitionPath: string;
  active: boolean;
  shadowedByDefinitionPath?: string;
};

type InstallConflictPolicy = "skip" | "overwrite" | "rename";

type RecommendedSkillItem = RecommendedSkillConfig;

type SkillDiscoveryDiagnostic = SkillDiscoveryDiagnosticConfig;

type RenameUnsupportedContext =
  | { kind: "add"; dryRun: boolean }
  | { kind: "recommended"; skill: RecommendedSkillItem; scope: SkillScope };

type RecommendedDefinitionCacheEntry = {
  body: string;
  assets: Record<string, string>;
  assetPaths: string[];
};

type ImportSourcePreset = {
  id: string;
  label: string;
  path: string;
};

const IMPORT_SOURCE_PRESETS: ImportSourcePreset[] = [
  { id: "claude", label: "Claude", path: ".claude/skills" },
  { id: "codex", label: "Codex", path: ".codex/skills" },
  { id: "agents", label: "Agents", path: ".agents/skills" },
  { id: "cursor", label: "Cursor", path: ".cursor/skills" },
];

const SKILL_ICON_OVERRIDES: Record<string, ReactNode> = {
  "skill-creator": <Pencil className="w-5 h-5" aria-hidden="true" />,
  "playwright-cli": <img src={playwrightIcon} alt="" className="h-7 w-7 object-contain" aria-hidden="true" />,
  "playwrite-cli": <img src={playwrightIcon} alt="" className="h-7 w-7 object-contain" aria-hidden="true" />,
  playwright: <img src={playwrightIcon} alt="" className="h-7 w-7 object-contain" aria-hidden="true" />,
  "frontend-design": <Palette className="w-5 h-5" aria-hidden="true" />,
  "mcp-builder": <McpIcon className="w-6 h-6" aria-hidden="true" />,
  docx: <FaFileWord className="w-5 h-5" aria-hidden="true" />,
  pdf: <FaFilePdf className="w-5 h-5" aria-hidden="true" />,
  xlsx: <FaFileExcel className="w-5 h-5" aria-hidden="true" />,
  pptx: <FaFilePowerpoint className="w-5 h-5" aria-hidden="true" />,
};

const SKILL_TILE_COLORS = [
  "from-sky-500/30 to-blue-500/20",
  "from-emerald-500/30 to-teal-500/20",
  "from-violet-500/30 to-fuchsia-500/20",
  "from-amber-500/30 to-orange-500/20",
  "from-rose-500/30 to-pink-500/20",
  "from-cyan-500/30 to-indigo-500/20",
];

const SKILL_TILE_COLOR_OVERRIDES: Record<string, string> = {
  "skill-creator": "from-emerald-200/80 to-green-200/70",
  "frontend-design": "from-violet-200/80 to-purple-200/70",
  "mcp-builder": "from-teal-200/80 to-cyan-200/70",
  pptx: "from-red-200/80 to-rose-200/70",
  docx: "from-pink-200/80 to-rose-200/70",
  pdf: "from-sky-200/80 to-blue-200/70",
  xlsx: "from-orange-200/80 to-amber-200/70",
  "playwright-cli": "from-slate-200/80 to-zinc-200/70",
  "playwrite-cli": "from-slate-200/80 to-zinc-200/70",
};

type SkillDetailsState =
  | { kind: "installed"; skill: SkillItem }
  | {
      kind: "recommended";
      skill: RecommendedSkillItem & { installed: boolean; installedSkill: SkillItem | null };
    };

const IMAGE_ASSET_EXTENSIONS = new Set(["png", "jpg", "jpeg", "gif", "webp", "svg"]);


function scopeLabel(scope: SkillScope): string {
  switch (scope) {
    case "project_local":
      return "Project Local";
    case "project":
      return "Project";
    case "global":
      return "Global";
    case "builtin":
      return "Builtin";
    default:
      return scope;
  }
}

function normalizeDisplayName(raw?: string, fallback?: string): string {
  const v = (raw || "").trim();
  if (v) return v;
  return (fallback || "").trim() || "Unnamed skill";
}

function normalizeDescription(raw?: string): string {
  const v = (raw || "").trim();
  return v || "No description";
}

function normalizeSkillScope(raw: string): SkillScope {
  if (raw === "project_local" || raw === "project" || raw === "global" || raw === "builtin") {
    return raw;
  }
  return "project";
}

function mapInstalledSkill(skill: InstalledSkillConfig): SkillItem {
  return {
    id: skill.skill_id,
    name: normalizeDisplayName(skill.name, skill.skill_dir.split("/").filter(Boolean).pop()),
    description: normalizeDescription(skill.description),
    scope: normalizeSkillScope(skill.scope),
    dirPath: skill.skill_dir,
    definitionPath: skill.definition_path,
    active: skill.active,
    shadowedByDefinitionPath: skill.shadowed_by_definition_path,
  };
}

function toPosixPath(value: string): string {
  return value.replace(/\\/g, "/");
}

function deriveSkillLocationPath(skill: SkillItem): string {
  const definitionPath = toPosixPath((skill.definitionPath || "").trim());
  if (!definitionPath) return "";

  if (skill.scope === "builtin") {
    return `builtin://${skill.name}`;
  }

  const marker =
    skill.scope === "project_local"
      ? "/.reliant.local/skills/"
      : skill.scope === "project"
        ? "/.reliant/skills/"
        : skill.scope === "global"
          ? "/.reliant/skills/"
          : null;

  if (!marker) {
    return definitionPath;
  }

  const idx = definitionPath.lastIndexOf(marker);
  if (idx === -1) {
    return definitionPath;
  }

  const rel = definitionPath.slice(idx + 1);
  if (skill.scope === "global") {
    return `~/${rel}`;
  }
  return rel;
}

function deriveSkillDeletePath(skill: SkillItem): string | null {
  const candidates = [skill.dirPath, skill.definitionPath].map((v) => toPosixPath((v || "").trim()));

  const marker =
    skill.scope === "project_local"
      ? "/.reliant.local/skills/"
      : skill.scope === "project"
        ? "/.reliant/skills/"
        : skill.scope === "global"
          ? "/.reliant/skills/"
          : null;

  if (!marker) {
    return null;
  }

  for (const candidate of candidates) {
    if (!candidate) continue;

    const idx = candidate.lastIndexOf(marker);
    if (idx === -1) continue;

    let rel = candidate.slice(idx + 1);
    if (rel.toLowerCase().endsWith("/skill.md")) {
      rel = rel.slice(0, rel.lastIndexOf("/"));
    }

    if (!rel) continue;

    if (skill.scope === "global") {
      if (rel.startsWith(".reliant/skills/")) {
        rel = rel.slice(".reliant/skills/".length);
      }
      rel = rel.replace(/^\/+/, "");
    }

    if (rel) return rel;
  }

  return null;
}

function joinProjectPath(projectPath: string, relativePath: string): string {
  const base = projectPath.trim().replace(/[\\/]+$/, "");
  const rel = relativePath
    .trim()
    .replace(/^(\.\/|\.\\)+/, "")
    .replace(/^[/\\]+/, "");
  if (!base) return rel;
  if (!rel) return base;
  const separator = base.includes("\\") && !base.includes("/") ? "\\" : "/";
  return `${base}${separator}${rel}`;
}

function parseGitHubTreeLikeSource(raw: string): {
  cloneUrl: string;
  inferredRef: string;
  inferredSubpath: string;
} | null {
  const input = raw.trim();
  if (!input) return null;

  let parsed: URL;
  try {
    parsed = new URL(input);
  } catch {
    return null;
  }

  const host = parsed.hostname.toLowerCase();
  if (host !== "github.com" && host !== "www.github.com") {
    return null;
  }

  const parts = parsed.pathname
    .split("/")
    .filter(Boolean)
    .map((segment) => {
      try {
        return decodeURIComponent(segment);
      } catch {
        return segment;
      }
    });

  if (parts.length < 4) return null;
  const [owner, repoRaw, mode, ref, ...tail] = parts;
  const repo = repoRaw.replace(/\.git$/i, "");
  if (!owner || !repo) return null;
  if (mode !== "tree" && mode !== "blob") return null;
  if (!ref) return null;

  let inferredSubpath = tail.join("/");
  if (mode === "blob") {
    const idx = inferredSubpath.lastIndexOf("/");
    inferredSubpath = idx >= 0 ? inferredSubpath.slice(0, idx) : "";
  }

  return {
    cloneUrl: `https://github.com/${owner}/${repo}.git`,
    inferredRef: ref,
    inferredSubpath,
  };
}

function parseGitHubRepoSource(rawSource: string): { owner: string; repo: string } | null {
  const input = rawSource.trim();
  if (!input) return null;

  let parsed: URL;
  try {
    parsed = new URL(input);
  } catch {
    return null;
  }

  const host = parsed.hostname.toLowerCase();
  if (host !== "github.com" && host !== "www.github.com") {
    return null;
  }

  const parts = parsed.pathname.split("/").filter(Boolean);
  if (parts.length < 2) return null;

  const owner = parts[0];
  const repo = parts[1].replace(/\.git$/i, "");
  if (!owner || !repo) return null;

  return { owner, repo };
}

function encodePathSegments(pathValue: string): string {
  return pathValue
    .split("/")
    .filter(Boolean)
    .map((segment) => encodeURIComponent(segment))
    .join("/");
}

function buildRecommendedSkillSourceUrl(skill: RecommendedSkillItem): string {
  const parsed = parseGitHubRepoSource(skill.source);
  if (!parsed) {
    return skill.source;
  }

  const ref = (skill.ref || "main").trim() || "main";
  const subpath = (skill.source_subpath || "").trim().replace(/^\/+|\/+$/g, "");
  const base = `https://github.com/${parsed.owner}/${parsed.repo}`;

  if (!subpath) {
    return `${base}/tree/${ref}`;
  }

  return `${base}/tree/${ref}/${encodePathSegments(subpath)}`;
}

function buildRecommendedSkillDefinitionUrl(skill: RecommendedSkillItem): string | null {
  const parsed = parseGitHubRepoSource(skill.source);
  if (!parsed) {
    return null;
  }

  const ref = (skill.ref || "main").trim() || "main";
  const subpath = (skill.source_subpath || "").trim().replace(/^\/+|\/+$/g, "");
  const skillMdPath = subpath ? `${subpath}/SKILL.md` : "SKILL.md";

  return `https://raw.githubusercontent.com/${parsed.owner}/${parsed.repo}/${ref}/${encodePathSegments(skillMdPath)}`;
}

function buildRecommendedFallbackMarkdown(skill: RecommendedSkillItem): string {
  const sourceUrl = buildRecommendedSkillSourceUrl(skill);
  return `# ${skill.name}\n\n${skill.description}\n\n[Source](${sourceUrl})`;
}

const RENAME_UNSUPPORTED_MESSAGE = "rename conflict policy is not supported for skill.md skills";

function isRenameConflictUnsupported(message?: string): boolean {
  return (message || "").toLowerCase().includes(RENAME_UNSUPPORTED_MESSAGE);
}

function skillVisualSeed(...parts: string[]): number {
  const input = parts.join("|").toLowerCase();
  let hash = 0;
  for (let i = 0; i < input.length; i += 1) {
    hash = (hash * 31 + input.charCodeAt(i)) >>> 0;
  }
  return hash;
}

export function getSkillInitials(nameOrId: string): string {
  const words = (nameOrId || "").trim().split(/\s+|-/).filter(Boolean);
  const initials = words
    .slice(0, 2)
    .map((word) => word[0] || "")
    .join("")
    .toUpperCase();
  return initials || "S";
}

function inferSkillFallback(name: string, id?: string): ReactNode {
  const normId = (id || "").trim().toLowerCase();
  const normName = name.trim().toLowerCase();
  if (normId && SKILL_ICON_OVERRIDES[normId]) return SKILL_ICON_OVERRIDES[normId];
  if (SKILL_ICON_OVERRIDES[normName]) return SKILL_ICON_OVERRIDES[normName];

  return getSkillInitials(name || id || "");
}

function getSkillColorClass(name: string, id?: string, visualSeed?: number): string {
  const normId = (id || "").trim().toLowerCase();
  const normName = (name || "").trim().toLowerCase();

  if (normId && SKILL_TILE_COLOR_OVERRIDES[normId]) {
    return SKILL_TILE_COLOR_OVERRIDES[normId];
  }
  if (normName && SKILL_TILE_COLOR_OVERRIDES[normName]) {
    return SKILL_TILE_COLOR_OVERRIDES[normName];
  }

  const seed = visualSeed ?? skillVisualSeed(id || "", name || "");
  return SKILL_TILE_COLORS[seed % SKILL_TILE_COLORS.length];
}

function extractSkillMarkdownBody(content: string): string {
  const trimmed = content.trimStart();
  if (!trimmed.startsWith("---")) return content;
  const lines = content.split(/\r?\n/);
  if (!lines[0] || lines[0].trim() !== "---") return content;
  for (let i = 1; i < lines.length; i += 1) {
    if (lines[i].trim() === "---") {
      return lines.slice(i + 1).join("\n").trim();
    }
  }
  return content;
}

function collectImageAssetPaths(markdown: string): string[] {
  const pattern = /\(([^)]+\.(?:png|jpg|jpeg|gif|webp|svg)(?:\?[^)]*)?)\)/gi;
  const seen = new Set<string>();
  const paths: string[] = [];

  let match: RegExpExecArray | null;
  while ((match = pattern.exec(markdown)) !== null) {
    const raw = (match[1] || "").trim();
    if (!raw || raw.startsWith("http://") || raw.startsWith("https://")) continue;

    const cleaned = raw.split("?")[0].trim();
    const ext = cleaned.split(".").pop()?.toLowerCase() || "";
    if (!IMAGE_ASSET_EXTENSIONS.has(ext)) continue;

    if (!seen.has(cleaned)) {
      seen.add(cleaned);
      paths.push(cleaned);
    }
  }

  return paths;
}

function imageMimeTypeFromPath(pathValue: string): string {
  const ext = pathValue.split(".").pop()?.toLowerCase() || "";
  if (ext === "png") return "image/png";
  if (ext === "jpg" || ext === "jpeg") return "image/jpeg";
  if (ext === "gif") return "image/gif";
  if (ext === "webp") return "image/webp";
  if (ext === "svg") return "image/svg+xml";
  return "application/octet-stream";
}

function bytesToDataURL(bytes: Uint8Array, mimeType: string): string {
  if (bytes.length === 0) return "";
  const binary = Array.from(bytes, (b) => String.fromCharCode(b)).join("");
  const base64 = btoa(binary);
  return `data:${mimeType};base64,${base64}`;
}

export function pickBestAssetPath(candidates: string[]): string | null {
  if (candidates.length === 0) return null;
  const score = (path: string): number => {
    const file = path.toLowerCase();
    let s = 0;
    if (file.endsWith(".png")) s += 100;
    else if (file.endsWith(".svg")) s += 80;
    else if (file.endsWith(".webp")) s += 60;
    else if (file.endsWith(".jpg") || file.endsWith(".jpeg")) s += 50;
    else if (file.endsWith(".gif")) s += 20;

    if (file.includes("icon")) s += 15;
    if (file.includes("logo")) s += 10;
    if (file.includes("small")) s -= 10;
    return s;
  };
  return [...candidates].sort((a, b) => score(b) - score(a))[0] || null;
}

export function isPackagedAssetPath(path: string): boolean {
  return /(^|.*\/)assets\/.+\.(png|jpg|jpeg|gif|webp|svg)$/i.test(path);
}

export function buildSkillAvatarContainerClassName({
  hasImageIcon,
  colorClass,
  className,
}: {
  hasImageIcon: boolean;
  colorClass: string;
  className?: string;
}): string {
  return `h-11 w-11 rounded-xl border border-white/10 ${
    hasImageIcon ? "bg-transparent" : `bg-gradient-to-br ${colorClass} text-foreground/90`
  } flex items-center justify-center text-sm font-semibold shrink-0 overflow-hidden ${className || ""}`.trim();
}

function SkillAvatar({
  iconSrc,
  fallback,
  colorClass,
  alt,
  className,
}: {
  iconSrc?: string;
  fallback: ReactNode;
  colorClass: string;
  alt: string;
  className?: string;
}) {
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    setFailed(false);
  }, [iconSrc]);

  const hasImageIcon = Boolean(iconSrc && !failed);

  return (
    <div className={buildSkillAvatarContainerClassName({ hasImageIcon, colorClass, className })}>
      {hasImageIcon ? (
        <img
          src={iconSrc}
          alt={alt}
          className="h-full w-full object-cover"
          loading="lazy"
          onError={() => setFailed(true)}
        />
      ) : (
        fallback
      )}
    </div>
  );
}

export function SkillsSettings() {
  const currentProject = useProjectStore((state) => state.currentProject);

  const [skills, setSkills] = useState<SkillItem[]>([]);
  const [skillDiagnostics, setSkillDiagnostics] = useState<SkillDiscoveryDiagnostic[]>([]);
  const [loading, setLoading] = useState(false);

  const [isAddModalOpen, setIsAddModalOpen] = useState(false);
  const [isImportModalOpen, setIsImportModalOpen] = useState(false);
  const [showAddAdvanced, setShowAddAdvanced] = useState(false);
  const [collapsedInactive, setCollapsedInactive] = useState(false);

  const [source, setSource] = useState("");
  const [sourceSubpath, setSourceSubpath] = useState("");
  const [ref, setRef] = useState("");
  const [scope, setScope] = useState<SkillScope>("project");
  const [conflictPolicy, setConflictPolicy] = useState<InstallConflictPolicy>("rename");

  const [installing, setInstalling] = useState(false);
  const [installingAction, setInstallingAction] = useState<"dry_run" | "install" | null>(null);
  const [importingPresetId, setImportingPresetId] = useState<string | null>(null);
  const [importingAll, setImportingAll] = useState(false);
  const [selectedImportSource, setSelectedImportSource] = useState<string>("all");
  const [deletingSkillId, setDeletingSkillId] = useState<string | null>(null);
  const [togglingSkillId, setTogglingSkillId] = useState<string | null>(null);
  const [installingRecommendedSkillId, setInstallingRecommendedSkillId] = useState<string | null>(null);
  const [renameUnsupportedContext, setRenameUnsupportedContext] = useState<RenameUnsupportedContext | null>(null);
  const [recommendedSkills, setRecommendedSkills] = useState<RecommendedSkillItem[]>([]);
  const [loadingRecommendedSkills, setLoadingRecommendedSkills] = useState(false);
  const [lastRunResult, setLastRunResult] = useState<string>("");
  const [skillDetails, setSkillDetails] = useState<SkillDetailsState | null>(null);
  const [recommendedInstallScope, setRecommendedInstallScope] = useState<SkillScope>("project");
  const [skillDefinitionBody, setSkillDefinitionBody] = useState<string>("");
  const [skillAssetPaths, setSkillAssetPaths] = useState<string[]>([]);
  const [skillAssetMap, setSkillAssetMap] = useState<Record<string, string>>({});
  const [skillIconMap, setSkillIconMap] = useState<Record<string, string>>({});
  const [recommendedDefinitionCache, setRecommendedDefinitionCache] = useState<Record<string, RecommendedDefinitionCacheEntry>>({});
  const [loadingSkillDefinition, setLoadingSkillDefinition] = useState(false);

  const showRecommendedSkills = true;

  const normalizedSourceHint = useMemo(() => {
    const parsed = parseGitHubTreeLikeSource(source);
    if (!parsed) return null;
    return {
      cloneUrl: parsed.cloneUrl,
      ref: ref.trim() || parsed.inferredRef,
      subpath: sourceSubpath.trim() || parsed.inferredSubpath,
    };
  }, [source, sourceSubpath, ref]);

  const inactiveSkills = useMemo(() => {
    return skills.filter((skill) => !skill.active).sort((a, b) => a.name.localeCompare(b.name));
  }, [skills]);

  const normalizedInstalledSkillNames = useMemo(() => {
    return new Set(skills.map((skill) => skill.name.trim().toLowerCase()));
  }, [skills]);

  const recommendedSkillsWithStatus = useMemo(() => {
    return recommendedSkills.map((skill) => {
      const normalizedName = skill.name.trim().toLowerCase();
      const installedSkill = skills.find((entry) => entry.name.trim().toLowerCase() === normalizedName) || null;
      return {
        ...skill,
        installed: normalizedInstalledSkillNames.has(normalizedName),
        installedSkill,
      };
    });
  }, [normalizedInstalledSkillNames, recommendedSkills, skills]);

  const visibleRecommendedSkills = useMemo(() => {
    return recommendedSkillsWithStatus.filter((skill) => !skill.installed);
  }, [recommendedSkillsWithStatus]);

  const installDestinationClassName = "w-full h-10 px-3 border border-input bg-background rounded-md text-sm";

  const scanSkills = useCallback(async () => {
    if (!currentProject?.id) return;
    setLoading(true);
    try {
      const response = await settingsGrpc.listInstalledSkills(currentProject.id);
      setSkills(response.skills.map(mapInstalledSkill));
      setSkillDiagnostics(response.diagnostics || []);
    } catch (error) {
      console.error("Failed to scan skills", error);
      toast.error("Failed to scan skills");
      setSkills([]);
      setSkillDiagnostics([]);
    } finally {
      setLoading(false);
    }
  }, [currentProject?.id]);

  const loadRecommendedSkills = useCallback(async () => {
    if (!currentProject?.id) return;
    setLoadingRecommendedSkills(true);
    try {
      const response = await settingsGrpc.listRecommendedSkills(currentProject.id);
      setRecommendedSkills(response.recommended);
    } catch (error) {
      console.error("Failed to load recommended skills", error);
      toast.error("Failed to load recommended skills");
      setRecommendedSkills([]);
    } finally {
      setLoadingRecommendedSkills(false);
    }
  }, [currentProject?.id]);

  const hydrateInstalledSkillIcons = useCallback(
    async (installedSkills: SkillItem[]) => {
      if (!currentProject?.id) return;
      const updates: Record<string, string> = {};

      await Promise.all(
        installedSkills.map(async (skill) => {
          try {
            const response = await settingsGrpc.getInstalledSkillDefinition(currentProject.id, skill.id);
            const assets = response.assets || [];
            if (assets.length === 0) return;

            const byPath = new Map<string, string>();
            const candidatePaths: string[] = [];
            for (const asset of assets) {
              const rawPath = (asset.path || "").replace(/\\/g, "/").replace(/^\.\//, "").replace(/^\//, "");
              if (!rawPath) continue;
              const mimeType = asset.mime_type || imageMimeTypeFromPath(rawPath);
              const src = bytesToDataURL(asset.content, mimeType);
              if (!src) continue;
              byPath.set(rawPath, src);
              candidatePaths.push(rawPath);
            }
            if (candidatePaths.length === 0) return;

            const onlyAssets = candidatePaths.filter((path) => isPackagedAssetPath(path));
            const iconPath = pickBestAssetPath(onlyAssets) || pickBestAssetPath(candidatePaths) || "";

            if (!iconPath) return;
            const selectedSrc = byPath.get(iconPath);
            if (selectedSrc) {
              updates[skill.id] = selectedSrc;
            }
          } catch {
            // best-effort only
          }
        }),
      );

      if (Object.keys(updates).length > 0) {
        setSkillIconMap((prev) => ({ ...prev, ...updates }));
      }
    },
    [currentProject?.id],
  );

  const runInstall = useCallback(
    async (dryRun: boolean, policyOverride?: InstallConflictPolicy) => {
      const trimmedSource = source.trim();
      if (!currentProject?.id) {
        toast.error("No active project selected");
        return;
      }
      if (!trimmedSource) {
        toast.error("Source is required");
        return;
      }

      setInstalling(true);
      setInstallingAction(dryRun ? "dry_run" : "install");
      try {
        const response = await settingsGrpc.installSkill({
          project_id: currentProject.id,
          source: trimmedSource,
          source_subpath: sourceSubpath.trim() || undefined,
          ref: ref.trim() || undefined,
          scope,
          conflict_policy: policyOverride ?? conflictPolicy,
          dry_run: dryRun,
        });

        setLastRunResult(response.message);
        if (!response.success) {
          const errorMessage = response.message || "Skill install failed";
          if ((policyOverride ?? conflictPolicy) === "rename" && isRenameConflictUnsupported(errorMessage)) {
            setRenameUnsupportedContext({ kind: "add", dryRun });
            return;
          }
          toast.error(errorMessage);
          return;
        }

        const installedCount = response.result?.installed_files.length ?? 0;
        const skippedCount = response.result?.skipped_files.length ?? 0;
        if (dryRun) {
          toast.success("Dry-run complete");
        } else if ((policyOverride ?? conflictPolicy) === "skip" && installedCount === 0 && skippedCount > 0) {
          toast.message("Skipped adding — a skill with this name already exists");
        } else {
          toast.success("Skill installed");
        }
        if (!dryRun) {
          setIsAddModalOpen(false);
          await scanSkills();
        }
      } catch (error) {
        console.error("Failed to run skill install", error);
        toast.error(error instanceof Error ? error.message : "Skill install failed");
      } finally {
        setInstalling(false);
        setInstallingAction(null);
      }
    },
    [source, currentProject?.id, sourceSubpath, ref, scope, conflictPolicy, scanSkills],
  );

  const importPreset = useCallback(
    async (preset: ImportSourcePreset, suppressToast = false) => {
      if (!currentProject?.id || !currentProject.path) {
        if (!suppressToast) toast.error("No active project selected");
        return { imported: 0, failed: 0, found: false };
      }

      setImportingPresetId(preset.id);
      try {
        let nodes;
        try {
          nodes = await filesystemGrpc.getFileTree(currentProject.id, preset.path, true);
        } catch {
          if (!suppressToast) {
            toast.message(`${preset.label} skills folder not found in this project`);
          }
          return { imported: 0, failed: 0, found: false };
        }

        const skillDirs = nodes
          .filter((node) => node.type === "directory")
          .filter((node) => {
            const children = node.children || [];
            return children.some((child) => child.type === "file" && child.name === "SKILL.md");
          })
          .map((node) => node.path);

        if (skillDirs.length === 0) {
          if (!suppressToast) {
            toast.message(`No importable ${preset.label} skills found`);
          }
          return { imported: 0, failed: 0, found: true };
        }

        let imported = 0;
        let failed = 0;

        for (const dir of skillDirs) {
          const absoluteSource = joinProjectPath(currentProject.path, dir);
          try {
            const response = await settingsGrpc.installSkill({
              project_id: currentProject.id,
              source: absoluteSource,
              scope,
              conflict_policy: conflictPolicy,
              dry_run: false,
            });
            if (response.success) imported += 1;
            else failed += 1;
          } catch {
            failed += 1;
          }
        }

        if (!suppressToast) {
          if (failed === 0) {
            toast.success(`Imported ${imported} ${preset.label} skill${imported === 1 ? "" : "s"}`);
          } else {
            toast.warning(
              `Imported ${imported} ${preset.label} skill${imported === 1 ? "" : "s"}; ${failed} failed`,
            );
          }
        }

        return { imported, failed, found: true };
      } finally {
        setImportingPresetId(null);
      }
    },
    [currentProject?.id, currentProject?.path, scope, conflictPolicy],
  );

  const importAll = useCallback(async () => {
    setImportingAll(true);
    try {
      let totalImported = 0;
      let totalFailed = 0;
      let anyFound = false;

      for (const preset of IMPORT_SOURCE_PRESETS) {
        const result = await importPreset(preset, true);
        totalImported += result.imported;
        totalFailed += result.failed;
        anyFound = anyFound || result.found;
      }

      if (!anyFound) {
        toast.message("No external skills folders found in this project");
      } else if (totalFailed === 0) {
        toast.success(`Imported ${totalImported} skill${totalImported === 1 ? "" : "s"}`);
        if (totalImported > 0) {
          setIsImportModalOpen(false);
        }
      } else {
        toast.warning(`Imported ${totalImported} skill${totalImported === 1 ? "" : "s"}; ${totalFailed} failed`);
      }

      await scanSkills();
    } finally {
      setImportingAll(false);
    }
  }, [importPreset, scanSkills]);

  const installRecommendedSkill = useCallback(
    async (
      skill: RecommendedSkillItem,
      installScope: SkillScope,
      policyOverride?: InstallConflictPolicy,
    ): Promise<boolean> => {
      if (!currentProject?.id) {
        toast.error("No active project selected");
        return false;
      }

      setInstallingRecommendedSkillId(skill.id);
      try {
        const response = await settingsGrpc.installSkill({
          project_id: currentProject.id,
          source: skill.source,
          source_subpath: skill.source_subpath,
          ref: skill.ref,
          scope: installScope,
          conflict_policy: policyOverride ?? conflictPolicy,
          dry_run: false,
        });

        setLastRunResult(response.message);
        if (!response.success) {
          const errorMessage = response.message || `Failed to install ${skill.name}`;
          if ((policyOverride ?? conflictPolicy) === "rename" && isRenameConflictUnsupported(errorMessage)) {
            setRenameUnsupportedContext({ kind: "recommended", skill, scope: installScope });
            return false;
          }
          if (errorMessage.toLowerCase().includes("already exists")) {
            toast.message(`Skipped adding ${skill.name} — it already exists`);
            return false;
          }
          toast.error(errorMessage);
          return false;
        }

        const installedCount = response.result?.installed_files.length ?? 0;
        const skippedCount = response.result?.skipped_files.length ?? 0;
        if ((policyOverride ?? conflictPolicy) === "skip" && installedCount === 0 && skippedCount > 0) {
          toast.message(`Skipped adding ${skill.name} — it already exists`);
        } else {
          toast.success(`Installed ${skill.name}`);
        }
        await scanSkills();
        return true;
      } catch (error) {
        console.error("Failed to install recommended skill", error);
        toast.error(error instanceof Error ? error.message : `Failed to install ${skill.name}`);
        return false;
      } finally {
        setInstallingRecommendedSkillId(null);
      }
    },
    [currentProject?.id, conflictPolicy, scanSkills],
  );

  useEffect(() => {
    if (!currentProject?.id) return;
    scanSkills();
    if (showRecommendedSkills) {
      loadRecommendedSkills();
    }
  }, [currentProject?.id, scanSkills, loadRecommendedSkills, showRecommendedSkills]);

  useEffect(() => {
    if (!currentProject?.id || skills.length === 0) return;
    void hydrateInstalledSkillIcons(skills);
  }, [currentProject?.id, skills, hydrateInstalledSkillIcons]);

  const loadSkillDefinition = useCallback(
    async (skill: SkillItem) => {
      if (!currentProject?.id) {
        toast.error("No active project selected");
        return;
      }
      setLoadingSkillDefinition(true);
      try {
        const response = await settingsGrpc.getInstalledSkillDefinition(currentProject.id, skill.id);
        const body = extractSkillMarkdownBody(response.definition_content);
        setSkillDefinitionBody(body);
        const referenced = collectImageAssetPaths(body);
        setSkillAssetPaths(referenced);

        const map: Record<string, string> = {};
        for (const asset of response.assets || []) {
          const path = (asset.path || "").replace(/\\/g, "/").replace(/^\.\//, "").replace(/^\//, "");
          if (!path) continue;
          const mimeType = asset.mime_type || imageMimeTypeFromPath(path);
          const dataUrl = bytesToDataURL(asset.content, mimeType);
          if (!dataUrl) continue;
          map[path] = dataUrl;
          const basename = path.split("/").pop() || path;
          if (!map[basename]) {
            map[basename] = dataUrl;
          }
          const assetsPrefixed = `assets/${basename}`;
          if (!map[assetsPrefixed]) {
            map[assetsPrefixed] = dataUrl;
          }
        }
        setSkillAssetMap(map);
      } catch (error) {
        console.error("Failed to read skill definition", error);
        setSkillDefinitionBody("");
        setSkillAssetPaths([]);
        setSkillAssetMap({});
        toast.error(error instanceof Error ? error.message : "Failed to read skill definition");
      } finally {
        setLoadingSkillDefinition(false);
      }
    },
    [currentProject?.id],
  );

  const loadRecommendedSkillDefinition = useCallback(
    async (skill: RecommendedSkillItem) => {
      const cached = recommendedDefinitionCache[skill.id];
      if (cached) {
        setSkillDefinitionBody(cached.body);
        setSkillAssetMap(cached.assets);
        setSkillAssetPaths(cached.assetPaths);
        return;
      }

      const fallbackBody = buildRecommendedFallbackMarkdown(skill);
      setLoadingSkillDefinition(true);
      try {
        const definitionUrl = buildRecommendedSkillDefinitionUrl(skill);
        if (!definitionUrl) {
          setSkillDefinitionBody(fallbackBody);
          setSkillAssetMap({});
          setSkillAssetPaths([]);
          setRecommendedDefinitionCache((prev) => ({
            ...prev,
            [skill.id]: {
              body: fallbackBody,
              assets: {},
              assetPaths: [],
            },
          }));
          return;
        }

        const response = await fetch(definitionUrl);
        if (!response.ok) {
          throw new Error(`Failed to load SKILL.md (${response.status})`);
        }

        const rawBody = await response.text();
        const body = extractSkillMarkdownBody(rawBody || fallbackBody);
        const assetPaths = collectImageAssetPaths(body);
        const entry: RecommendedDefinitionCacheEntry = {
          body,
          assets: {},
          assetPaths,
        };

        setSkillDefinitionBody(entry.body);
        setSkillAssetMap(entry.assets);
        setSkillAssetPaths(entry.assetPaths);
        setRecommendedDefinitionCache((prev) => ({ ...prev, [skill.id]: entry }));
      } catch (error) {
        console.error("Failed to load recommended skill definition", error);
        setSkillDefinitionBody(fallbackBody);
        setSkillAssetMap({});
        setSkillAssetPaths([]);
      } finally {
        setLoadingSkillDefinition(false);
      }
    },
    [recommendedDefinitionCache],
  );

  const openInstalledSkillDetails = useCallback(
    (skill: SkillItem) => {
      setSkillDetails({ kind: "installed", skill });
      void loadSkillDefinition(skill);
    },
    [loadSkillDefinition],
  );

  const openRecommendedSkillDetails = useCallback(
    (skill: RecommendedSkillItem & { installed: boolean; installedSkill: SkillItem | null }) => {
      setRecommendedInstallScope("project");
      setSkillDetails({ kind: "recommended", skill });
      if (skill.installedSkill) {
        void loadSkillDefinition(skill.installedSkill);
      } else {
        void loadRecommendedSkillDefinition(skill);
      }
    },
    [loadRecommendedSkillDefinition, loadSkillDefinition],
  );

  const closeSkillDetails = useCallback(() => {
    setSkillDetails(null);
    setSkillDefinitionBody("");
    setSkillAssetPaths([]);
    setSkillAssetMap({});
    setLoadingSkillDefinition(false);
    setRecommendedInstallScope("project");
  }, []);

  const canOpenInstalledSkillFolder = useCallback(
    (skill: SkillItem) => {
      if (!currentProject?.path) return false;
      return skill.scope === "project" || skill.scope === "project_local";
    },
    [currentProject?.path],
  );

  const openInstalledSkillFolder = useCallback(
    async (skill: SkillItem) => {
      if (!canOpenInstalledSkillFolder(skill)) {
        toast.message("Open folder is available for project and project-local skills");
        return;
      }

      const deletePath = deriveSkillDeletePath(skill);
      if (!deletePath || !currentProject?.path) {
        toast.error(`Cannot determine folder path for ${skill.name}`);
        return;
      }

      if (typeof window === "undefined" || !window.electronAPI?.openProjectDirectory) {
        toast.message("Open folder is available in the desktop app");
        return;
      }

      const absolutePath = joinProjectPath(currentProject.path, deletePath);
      try {
        const result = await window.electronAPI.openProjectDirectory(absolutePath);
        if (!result?.success) {
          throw new Error(result?.error || "Failed to open folder");
        }
      } catch (error) {
        toast.error(error instanceof Error ? error.message : "Failed to open folder");
      }
    },
    [canOpenInstalledSkillFolder, currentProject?.path],
  );

  const deleteSkill = useCallback(
    async (skill: SkillItem): Promise<boolean> => {
      if (!currentProject?.id) return false;

      const deletePath = deriveSkillDeletePath(skill);
      if (!deletePath) {
        toast.error(`Cannot determine delete path for ${skill.name}`);
        return false;
      }

      const locationHint = skill.scope === "global" ? "~/.reliant/skills" : "project workspace";
      const confirmed = window.confirm(
        `Delete skill "${skill.name}"?\n\nThis will remove the folder from ${locationHint}:\n${deletePath}`,
      );
      if (!confirmed) return false;

      setDeletingSkillId(skill.id);
      try {
        if (skill.scope === "global") {
          const response = await settingsGrpc.deleteGlobalSkill(currentProject.id, deletePath);
          if (!response.success) {
            throw new Error(response.message || "Failed to delete global skill");
          }
        } else {
          await filesystemGrpc.deleteFileOrFolder(currentProject.id, deletePath);
        }
        toast.success(`Deleted ${skill.name}`);
        await scanSkills();
        return true;
      } catch (error) {
        console.error("Failed to delete skill", error);
        toast.error(error instanceof Error ? error.message : "Failed to delete skill");
        return false;
      } finally {
        setDeletingSkillId(null);
      }
    },
    [currentProject?.id, scanSkills],
  );

  const toggleSkillEnabled = useCallback(
    async (skill: SkillItem, enabled: boolean) => {
      if (!currentProject?.id) {
        toast.error("No active project selected");
        return;
      }
      if (skill.scope === "builtin") {
        toast.error("Builtin skills are managed automatically by precedence and cannot be toggled directly");
        return;
      }

      setTogglingSkillId(skill.id);
      try {
        const response = await settingsGrpc.setSkillEnabled(currentProject.id, skill.id, enabled);
        if (!response.success) {
          throw new Error(response.message || "Failed to update skill state");
        }
        toast.success(`${enabled ? "Active" : "Inactive"} ${skill.name}`);
        if (!enabled) {
          setCollapsedInactive(false);
        }
        await scanSkills();
      } catch (error) {
        console.error("Failed to update skill enabled state", error);
        toast.error(error instanceof Error ? error.message : "Failed to update skill state");
      } finally {
        setTogglingSkillId(null);
      }
    },
    [currentProject?.id, scanSkills],
  );

  const renderSkillCard = (skill: SkillItem) => {
    const isBuiltin = skill.scope === "builtin";
    const inactiveReason = !skill.active && skill.shadowedByDefinitionPath
      ? isBuiltin
        ? "Inactive: shadowed by a higher-priority skill. Builtin skills cannot be toggled directly; remove or rename the overriding skill to reactivate."
        : "Inactive: shadowed by a higher-priority skill."
      : isBuiltin
        ? "Inactive: builtin skills cannot be toggled directly."
        : "Inactive: disabled.";

    const visualSeed = skillVisualSeed(skill.id, skill.name, skill.scope);
    const visualColor = getSkillColorClass(skill.name, skill.id, visualSeed);
    const visualLabel = inferSkillFallback(skill.name, skill.id);
    const resolvedIcon = skillIconMap[skill.id];

    return (
      <div
        key={skill.id}
        className="group rounded-2xl border border-border/70 bg-card/70 hover:bg-card transition-colors px-4 py-3"
      >
        <div className="flex items-center justify-between gap-3">
          <button
            type="button"
            className="min-w-0 flex items-center gap-3 text-left flex-1"
            onClick={() => openInstalledSkillDetails(skill)}
          >
            <SkillAvatar
              iconSrc={resolvedIcon}
              fallback={visualLabel}
              colorClass={visualColor}
              alt={`${skill.name} icon`}
            />
            <div className="min-w-0">
              <div className="flex items-center gap-2 flex-wrap">
                <span className="font-medium text-sm truncate">{skill.name}</span>
                {isBuiltin ? (
                  <span className="text-[10px] px-2 py-0.5 rounded-full border border-indigo-500/40 text-indigo-600 dark:text-indigo-400">
                    Builtin
                  </span>
                ) : skill.active ? (
                  <span className="text-[10px] px-2 py-0.5 rounded-full border border-emerald-500/40 text-emerald-600 dark:text-emerald-400">
                    Active
                  </span>
                ) : (
                  <Tooltip content={inactiveReason} placement="top" delay={250}>
                    <span className="text-[10px] px-2 py-0.5 rounded-full border border-amber-500/40 text-amber-600 dark:text-amber-400 cursor-help">
                      Inactive
                    </span>
                  </Tooltip>
                )}
              </div>
              <p className="text-xs text-muted-foreground mt-1 line-clamp-1">{skill.description}</p>
            </div>
          </button>

          <div className="flex items-center gap-2 shrink-0">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-8 w-8 p-0 opacity-0 group-hover:opacity-100 focus-visible:opacity-100 transition-opacity"
              onClick={() => openInstalledSkillDetails(skill)}
              aria-label={`Skill settings for ${skill.name}`}
            >
              <Settings className="w-4 h-4" />
            </Button>

            {!isBuiltin && (
              <Toggle
                checked={skill.active}
                onChange={(enabled) => {
                  void toggleSkillEnabled(skill, enabled);
                }}
                disabled={
                  togglingSkillId === skill.id ||
                  (togglingSkillId !== null && togglingSkillId !== skill.id) ||
                  deletingSkillId === skill.id
                }
                srLabel={`Enable ${skill.name}`}
              />
            )}
          </div>
        </div>
      </div>
    );
  };

  if (!currentProject) {
    return (
      <div className="space-y-3">
        <h2 className="text-base font-semibold">Skills</h2>
        <p className="text-sm text-muted-foreground">
          Select a project to manage skills.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-6" data-onboarding="skills-settings">
      <div className="space-y-3">
        <h2 className="text-base font-semibold">Skills</h2>
        <p className="text-sm text-muted-foreground">
          Manage installed skills, add new ones, or import from other tools.
        </p>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            leftIcon={<LuBookPlus className="w-4 h-4" />}
            onClick={() => setIsAddModalOpen(true)}
          >
            Add Skill
          </Button>
          <Button
            variant="outline"
            size="sm"
            leftIcon={<LuBookDown className="w-4 h-4" />}
            onClick={() => setIsImportModalOpen(true)}
          >
            Import
          </Button>
          <Button
            variant="outline"
            size="sm"
            leftIcon={<RefreshCw className="w-4 h-4" />}
            onClick={scanSkills}
            loading={loading}
          >
            Refresh
          </Button>
        </div>
      </div>

      {skillDiagnostics.length > 0 && (
        <section className="rounded-lg border border-yellow-500/40 bg-yellow-500/5 p-4 space-y-3">
          <h3 className="text-sm font-semibold">Skill Spec Compliance Diagnostics</h3>
          <p className="text-xs text-muted-foreground">
            Some discovered skill definitions were ignored because they failed validation.
          </p>
          <div className="space-y-2">
            {skillDiagnostics.map((diag, idx) => (
              <div key={`${diag.path}-${idx}`} className="rounded border border-yellow-500/30 bg-background/80 p-2">
                <div className="text-xs font-medium break-all">{diag.path}</div>
                <div className="text-[11px] text-muted-foreground">scope: {diag.scope}</div>
                <div className="text-xs mt-1">{diag.message}</div>
              </div>
            ))}
          </div>
        </section>
      )}

      <section className="space-y-4">
        <div className="rounded-2xl border border-border/70 bg-card/40 p-4 space-y-4">
          <h3 className="text-sm font-semibold">Installed</h3>
          {loading ? (
            <div className="text-sm text-muted-foreground">Scanning skills…</div>
          ) : skills.length === 0 ? (
            <div className="text-sm text-muted-foreground">
              No skills found in .reliant.local/skills, .reliant/skills, or ~/.reliant/skills.
            </div>
          ) : (
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-2.5">
              {skills
                .filter((skill) => skill.active)
                .sort((a, b) => a.name.localeCompare(b.name))
                .map(renderSkillCard)}
            </div>
          )}

          {inactiveSkills.length > 0 && (
            <div className="space-y-2 pt-2 border-t border-border/40">
              <button
                type="button"
                className="inline-flex items-center gap-1 text-xs font-medium text-muted-foreground hover:text-foreground appearance-none border-0 bg-transparent p-0"
                onClick={() => setCollapsedInactive((prev) => !prev)}
              >
                {collapsedInactive ? "▸" : "▾"} Inactive ({inactiveSkills.length})
              </button>
              {!collapsedInactive && <div className="grid grid-cols-1 lg:grid-cols-2 gap-2.5">{inactiveSkills.map(renderSkillCard)}</div>}
            </div>
          )}
        </div>
      </section>

      {showRecommendedSkills && (
        <section className="space-y-3">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-semibold">Recommended</h3>
          </div>

          {loadingRecommendedSkills ? (
            <div className="text-sm text-muted-foreground">Loading recommended skills…</div>
          ) : visibleRecommendedSkills.length === 0 ? (
            <div className="text-sm text-muted-foreground">All recommended skills are already installed.</div>
          ) : (
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-2.5">
              {visibleRecommendedSkills.map((skill) => {
                const visualSeed = skillVisualSeed(skill.id, skill.name);
                const visualColor = getSkillColorClass(skill.name, skill.id, visualSeed);
                const visualLabel = inferSkillFallback(skill.name, skill.id);

                return (
                  <div
                    key={skill.id}
                    className="rounded-2xl border border-border/70 bg-card/50 hover:bg-card transition-colors px-4 py-3"
                  >
                    <button
                      type="button"
                      className="min-w-0 w-full flex items-center gap-3 text-left"
                      onClick={() => openRecommendedSkillDetails(skill)}
                    >
                      <SkillAvatar
                        fallback={visualLabel}
                        colorClass={visualColor}
                        alt={`${skill.name} icon`}
                      />
                      <div className="min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="font-medium text-sm truncate">{skill.name}</span>
                        </div>
                        <p className="text-xs text-muted-foreground mt-1 line-clamp-1">{skill.description}</p>
                      </div>
                    </button>
                  </div>
                );
              })}
            </div>
          )}
        </section>
      )}

      <Modal
        isOpen={skillDetails !== null}
        onClose={closeSkillDetails}
        title={skillDetails?.skill.name || "Skill Details"}
        titlePrefix={
          skillDetails ? (
            (() => {
              const targetSkill = skillDetails.kind === "installed" ? skillDetails.skill : skillDetails.skill.installedSkill;
              const iconSrc = targetSkill ? skillIconMap[targetSkill.id] : undefined;
              const fallbackLabel = inferSkillFallback(skillDetails.skill.name, skillDetails.skill.id);
              const colorClass = getSkillColorClass(skillDetails.skill.name, skillDetails.skill.id);

              return (
                <SkillAvatar
                  iconSrc={iconSrc}
                  fallback={fallbackLabel}
                  colorClass={colorClass}
                  alt={`${skillDetails.skill.name} icon`}
                  className="h-10 w-10"
                />
              );
            })()
          ) : undefined
        }
        headerActions={
          skillDetails?.kind === "installed" && skillDetails.skill.scope !== "builtin" ? (
            <Toggle
              checked={skillDetails.skill.active}
              onChange={(enabled) => {
                void toggleSkillEnabled(skillDetails.skill, enabled);
                setSkillDetails((prev) => {
                  if (!prev || prev.kind !== "installed" || prev.skill.id !== skillDetails.skill.id) return prev;
                  return {
                    ...prev,
                    skill: {
                      ...prev.skill,
                      active: enabled,
                    },
                  };
                });
              }}
              disabled={
                togglingSkillId === skillDetails.skill.id ||
                (togglingSkillId !== null && togglingSkillId !== skillDetails.skill.id) ||
                deletingSkillId === skillDetails.skill.id
              }
              srLabel={`Enable ${skillDetails.skill.name}`}
            />
          ) : undefined
        }
        size="lg"
      >
        {skillDetails && (
          <div className="space-y-4">
            <div className="flex items-start justify-between gap-3">
              <div className="space-y-1">
                <p className="text-sm text-muted-foreground">{skillDetails.skill.description}</p>
                {skillDetails.kind === "installed" ? (
                  <p className="text-xs text-muted-foreground font-mono break-all">
                    {deriveSkillLocationPath(skillDetails.skill)}
                  </p>
                ) : (
                  <button
                    type="button"
                    className="text-xs text-primary hover:underline"
                    onClick={() => void openExternalLink(buildRecommendedSkillSourceUrl(skillDetails.skill))}
                  >
                    Source
                  </button>
                )}
              </div>

              <div className="flex items-center gap-2">
                {skillDetails.kind === "installed" && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    leftIcon={<FolderOpen className="w-4 h-4" />}
                    onClick={() => void openInstalledSkillFolder(skillDetails.skill)}
                    disabled={!canOpenInstalledSkillFolder(skillDetails.skill)}
                  >
                    Open folder
                  </Button>
                )}
              </div>
            </div>

            <div className="rounded-xl border border-border/70 bg-card/40 p-4 space-y-3">
              <div className="flex items-center justify-between">
                <h4 className="text-sm font-semibold">Definition</h4>
                <span className="text-[11px] text-muted-foreground">SKILL.md + packaged assets</span>
              </div>

              {loadingSkillDefinition ? (
                <p className="text-sm text-muted-foreground">Loading definition…</p>
              ) : skillDefinitionBody ? (
                <>
                  {skillAssetPaths.length > 0 && (
                    <div className="space-y-1">
                      <p className="text-xs text-muted-foreground">Referenced image assets</p>
                      <div className="flex flex-wrap gap-1.5">
                        {skillAssetPaths.map((assetPath) => (
                          <span
                            key={assetPath}
                            className="text-[11px] rounded-md border border-border px-2 py-1 font-mono text-muted-foreground"
                          >
                            {assetPath}
                          </span>
                        ))}
                      </div>
                    </div>
                  )}
                  <div className="max-h-[55vh] overflow-y-auto pr-1">
                    <MarkdownRenderer content={skillDefinitionBody} localImages={skillAssetMap} />
                  </div>
                </>
              ) : (
                <p className="text-sm text-muted-foreground">No definition available for this skill.</p>
              )}
            </div>

            {skillDetails.kind === "recommended" && !skillDetails.skill.installed && (
              <div className="flex flex-col gap-3 md:flex-row md:items-end md:justify-end">
                <label className="space-y-1 md:w-80">
                  <span className="text-xs text-muted-foreground">Install destination</span>
                  <select
                    value={recommendedInstallScope}
                    onChange={(e) => setRecommendedInstallScope(e.target.value as SkillScope)}
                    className={installDestinationClassName}
                  >
                    <option value="project">{scopeLabel("project")} (.reliant/skills)</option>
                    <option value="project_local">{scopeLabel("project_local")} (.reliant.local/skills)</option>
                    <option value="global">{scopeLabel("global")} (~/.reliant/skills)</option>
                  </select>
                </label>
                <Button
                  type="button"
                  variant="primary"
                  size="sm"
                  leftIcon={<FolderDown className="w-4 h-4" />}
                  onClick={async () => {
                    const installed = await installRecommendedSkill(skillDetails.skill, recommendedInstallScope);
                    if (installed) {
                      closeSkillDetails();
                    }
                  }}
                  disabled={installingRecommendedSkillId !== null}
                  loading={installingRecommendedSkillId === skillDetails.skill.id}
                >
                  Install
                </Button>
              </div>
            )}

            {skillDetails.kind === "installed" && skillDetails.skill.scope !== "builtin" && (
              <div className="flex justify-end">
                <Button
                  type="button"
                  variant="destructive"
                  size="sm"
                  leftIcon={<Trash2 className="w-4 h-4" />}
                  onClick={async () => {
                    const deleted = await deleteSkill(skillDetails.skill);
                    if (deleted) {
                      closeSkillDetails();
                    }
                  }}
                  loading={deletingSkillId === skillDetails.skill.id}
                  disabled={
                    togglingSkillId === skillDetails.skill.id ||
                    (deletingSkillId !== null && deletingSkillId !== skillDetails.skill.id)
                  }
                >
                  Delete
                </Button>
              </div>
            )}
          </div>
        )}
      </Modal>

      <Modal
        isOpen={isAddModalOpen}
        onClose={() => {
          setIsAddModalOpen(false);
        }}
        title="Add Skill"
        size="lg"
      >
        <div className="space-y-4">
          <label className="space-y-1 block">
            <span className="text-xs text-muted-foreground">Skill source</span>
            <input
              value={source}
              onChange={(e) => setSource(e.target.value)}
              placeholder="Paste a GitHub URL or local folder path"
              className="w-full px-3 py-2 border border-input bg-background rounded-md text-sm"
            />
          </label>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <label className="space-y-1">
              <span className="text-xs text-muted-foreground">Scope</span>
              <select
                value={scope}
                onChange={(e) => setScope(e.target.value as SkillScope)}
                className="w-full h-10 px-3 border border-input bg-background rounded-md text-sm"
              >
                <option value="project">{scopeLabel("project")} (.reliant/skills)</option>
                <option value="project_local">{scopeLabel("project_local")} (.reliant.local/skills)</option>
                <option value="global">{scopeLabel("global")} (~/.reliant/skills)</option>
              </select>
            </label>
          </div>

          <div className="space-y-2">
            <button
              type="button"
              className="inline-flex items-center gap-1 text-xs font-medium text-muted-foreground hover:text-foreground appearance-none border-0 bg-transparent p-0"
              onClick={() => setShowAddAdvanced((v) => !v)}
            >
              {showAddAdvanced ? "▾ Hide advanced options" : "▸ Show advanced options"}
            </button>

            {showAddAdvanced && (
              <div className="rounded-md border border-border p-3 grid grid-cols-1 md:grid-cols-2 gap-3">
                <label className="space-y-1">
                  <span className="text-xs text-muted-foreground">Conflict policy</span>
                  <select
                    value={conflictPolicy}
                    onChange={(e) => setConflictPolicy(e.target.value as InstallConflictPolicy)}
                    className="w-full h-10 px-3 border border-input bg-background rounded-md text-sm"
                  >
                    <option value="rename">rename (install to suffixed directory)</option>
                    <option value="skip">skip (keep existing files)</option>
                    <option value="overwrite">overwrite (replace existing files)</option>
                  </select>
                </label>
                <label className="space-y-1">
                  <span className="text-xs text-muted-foreground">Source subpath (optional)</span>
                  <input
                    value={sourceSubpath}
                    onChange={(e) => setSourceSubpath(e.target.value)}
                    placeholder="skills/my-skill"
                    className="w-full h-10 px-3 border border-input bg-background rounded-md text-sm"
                  />
                </label>
                <label className="space-y-1 md:col-span-2">
                  <span className="text-xs text-muted-foreground">Git ref (optional)</span>
                  <input
                    value={ref}
                    onChange={(e) => setRef(e.target.value)}
                    placeholder="main"
                    className="w-full h-10 px-3 border border-input bg-background rounded-md text-sm"
                  />
                </label>
              </div>
            )}
          </div>

          {normalizedSourceHint && (
            <div className="rounded border border-border bg-muted/20 p-3 text-xs space-y-1">
              <div className="flex items-center gap-2 font-medium">
                <Link className="w-3 h-3" />
                GitHub URL detected — installer will auto-normalize
              </div>
              <div className="text-muted-foreground break-all">source → {normalizedSourceHint.cloneUrl}</div>
              <div className="text-muted-foreground">ref → {normalizedSourceHint.ref || "(none)"}</div>
              <div className="text-muted-foreground break-all">
                source_subpath → {normalizedSourceHint.subpath || "(repo root)"}
              </div>
            </div>
          )}

          <div className="flex justify-end gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setIsAddModalOpen(false);
              }}
            >
              Cancel
            </Button>

            <>
              <Button
                variant="outline"
                size="sm"
                leftIcon={<ShieldCheck className="w-4 h-4" />}
                onClick={() => runInstall(true)}
                disabled={!source.trim() || installing}
                loading={installingAction === "dry_run"}
              >
                Dry Run
              </Button>
              <Button
                variant="primary"
                size="sm"
                leftIcon={<FolderDown className="w-4 h-4" />}
                onClick={() => runInstall(false)}
                disabled={!source.trim() || installing}
                loading={installingAction === "install"}
              >
                Install
              </Button>
            </>
          </div>

          {lastRunResult && (
            <div className="rounded border border-border bg-muted/20 p-3">
              <div className="text-xs font-medium mb-1">Last Run Result</div>
              <pre className="text-xs whitespace-pre-wrap text-muted-foreground">{lastRunResult}</pre>
            </div>
          )}
        </div>
      </Modal>

      <Modal
        isOpen={renameUnsupportedContext !== null}
        onClose={() => {
          setRenameUnsupportedContext(null);
        }}
        title="Duplicate skill detected"
        size="md"
      >
        <div className="space-y-4">
          <p className="text-sm text-muted-foreground">
            A skill with this name is already installed. Choose whether to keep the existing one or replace it.
          </p>

          <div className="rounded border border-border bg-muted/20 p-3 text-xs text-muted-foreground space-y-1">
            <div>
              <span className="font-medium text-foreground">Skip Adding:</span> keep the existing skill and skip this install.
            </div>
            <div>
              <span className="font-medium text-foreground">Overwrite Existing:</span> replace files for the existing skill.
            </div>
          </div>

          <div className="flex justify-end gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setRenameUnsupportedContext(null);
              }}
            >
              Cancel
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={async () => {
                if (!renameUnsupportedContext) return;
                const ctx = renameUnsupportedContext;
                setRenameUnsupportedContext(null);
                if (ctx.kind === "add") {
                  await runInstall(ctx.dryRun, "skip");
                } else {
                  await installRecommendedSkill(ctx.skill, ctx.scope, "skip");
                }
              }}
            >
              Skip Adding
            </Button>
            <Button
              variant="primary"
              size="sm"
              onClick={async () => {
                if (!renameUnsupportedContext) return;
                const ctx = renameUnsupportedContext;
                setRenameUnsupportedContext(null);
                if (ctx.kind === "add") {
                  await runInstall(ctx.dryRun, "overwrite");
                } else {
                  await installRecommendedSkill(ctx.skill, ctx.scope, "overwrite");
                }
              }}
            >
              Overwrite Existing
            </Button>
          </div>
        </div>
      </Modal>

      <Modal
        isOpen={isImportModalOpen}
        onClose={() => setIsImportModalOpen(false)}
        title="Import Skills"
        size="lg"
      >
        <div className="space-y-4">
          <p className="text-sm text-muted-foreground">
            Import skills from common tool folders in this project.
          </p>

          <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
            <label className="space-y-1">
              <span className="text-xs text-muted-foreground">Import destination scope</span>
              <select
                value={scope}
                onChange={(e) => setScope(e.target.value as SkillScope)}
                className="w-full h-10 px-3 border border-input bg-background rounded-md text-sm"
              >
                <option value="project">{scopeLabel("project")} (.reliant/skills)</option>
                <option value="project_local">{scopeLabel("project_local")} (.reliant.local/skills)</option>
                <option value="global">{scopeLabel("global")} (~/.reliant/skills)</option>
              </select>
            </label>
            <label className="space-y-1">
              <span className="text-xs text-muted-foreground">Conflict policy</span>
              <select
                value={conflictPolicy}
                onChange={(e) => setConflictPolicy(e.target.value as InstallConflictPolicy)}
                className="w-full h-10 px-3 border border-input bg-background rounded-md text-sm"
              >
                <option value="skip">skip (keep existing files)</option>
                <option value="overwrite">overwrite (replace existing files)</option>
                <option value="rename">rename (install to suffixed directory)</option>
              </select>
            </label>
            <label className="space-y-1">
              <span className="text-xs text-muted-foreground">Import source</span>
              <select
                value={selectedImportSource}
                onChange={(e) => setSelectedImportSource(e.target.value)}
                className="w-full h-10 px-3 border border-input bg-background rounded-md text-sm"
              >
                <option value="all">All sources</option>
                {IMPORT_SOURCE_PRESETS.map((preset) => (
                  <option key={preset.id} value={preset.id}>
                    {preset.label}
                  </option>
                ))}
              </select>
            </label>
          </div>

          <div className="flex justify-end">
            <Button
              variant="primary"
              size="md"
              className="h-10 px-4"
              leftIcon={<FolderDown className="w-4 h-4" />}
              onClick={async () => {
                if (selectedImportSource === "all") {
                  await importAll();
                  return;
                }
                const preset = IMPORT_SOURCE_PRESETS.find((p) => p.id === selectedImportSource);
                if (!preset) {
                  toast.error("Invalid import source selected");
                  return;
                }
                await importPreset(preset);
                await scanSkills();
              }}
              disabled={!!importingPresetId || installing || importingAll}
              loading={importingAll || (!!importingPresetId && selectedImportSource !== "all")}
            >
              Import
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
