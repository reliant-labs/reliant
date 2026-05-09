import { useState, useEffect, useCallback, useMemo, useRef, createContext, useContext, type ReactNode } from "react";
import { packageCommandsGrpc } from "../../api/package-commands-grpc";
import { Play, Square, ChevronDown, ChevronRight, RefreshCw, Terminal, Loader2, CheckCircle, XCircle, ExternalLink, Hammer, Package, Zap, Clock, Activity, RotateCw, Star, X, Globe, Layers } from "lucide-react";
import { useContainerWidth } from "../../hooks/useContainerWidth";
import { useUnifiedProcessCounts } from "../../hooks/useUnifiedProcesses";
import { usePackageCommands, usePackageProcesses, useRunCommand, useKillProcess, type PackageCommand, type PackageType, type PackageProcess } from "../../hooks/package-queries";
import { useProcessStore, type BackgroundProcess } from "../../store/processStore";
import { useProjectStore } from "../../store/projectStore";
import { useBrowserStore } from "../../store/browserStore";
import { useViewerStore } from "../../store/viewerStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { ProcessLogsViewer } from "./ProcessLogsViewer";
import { TerminalOutput, type ProcessInfo } from "../shared/TerminalOutput";
import { PortsDisplay } from "../shared/PortsDisplay";
import { toast } from "sonner";
import { cn } from "../../lib/utils";
import { BackgroundProcessStatus } from "../../api/background-grpc";
import { processMatchesCommand, findCommandForProcess as findCommandForProcessUtil } from "./commandMatching";
import {
  SidebarHeader,
  SidebarHeaderButton,
  SidebarSearchInput,
  SidebarEmptyState,
} from "../RightSidebar/shared";

interface CommandsViewerTabProps {
  worktreeId?: string;
  processId?: string;
}

// Responsive layout context for child components
// Simplified to just compact vs normal - no more confusing three-tier system
interface ResponsiveContext {
  isCompact: boolean;   // < 320px - compact layout (smaller text, tighter spacing)
}

const ResponsiveCtx = createContext<ResponsiveContext>({
  isCompact: false,
});

const useResponsive = () => useContext(ResponsiveCtx);

// Icons for package types
const packageTypeIcons: Record<PackageType, ReactNode> = {
  makefile: <Hammer className="w-3.5 h-3.5" />,
  npm: <Package className="w-3.5 h-3.5" />,
  taskfile: <Zap className="w-3.5 h-3.5" />,
};

// Unified process type that combines PackageProcess and BackgroundProcess
interface UnifiedProcess {
  id: string;
  command: string;
  status: BackgroundProcessStatus;
  start_time: string;
  end_time?: string;
  exit_code?: number;
  working_dir: string;
  worktree_id?: string;
  ports?: { port: number; protocol?: string }[];
  // Source tracking
  source: "package" | "background";
}

// Convert PackageProcess to UnifiedProcess
function toUnifiedProcess(p: PackageProcess): UnifiedProcess {
  return {
    ...p,
    ports: undefined,
    source: "package" as const,
  };
}

// Convert BackgroundProcess to UnifiedProcess
function backgroundToUnified(p: BackgroundProcess): UnifiedProcess {
  return {
    id: p.id,
    command: p.command,
    status: p.status,
    start_time: p.start_time,
    end_time: p.end_time,
    exit_code: p.exit_code,
    working_dir: p.working_dir,
    worktree_id: p.worktree_id,
    ports: p.ports?.map(port => ({ port: port.port, protocol: port.protocol })),
    source: "background" as const,
  };
}

// Helper to generate a unique key for a command (includes relative_path for uniqueness)
function getCommandKey(cmd: PackageCommand): string {
  return cmd.relative_path 
    ? `${cmd.package_type}:${cmd.relative_path}:${cmd.name}`
    : `${cmd.package_type}:${cmd.name}`;
}

// Storage key for dismissed processes
const DISMISSED_STORAGE_KEY = 'reliant-dismissed-processes';

// Storage key for expanded types
const EXPANDED_TYPES_STORAGE_KEY = 'reliant-expanded-types';

// Storage key for favorites expanded state
const FAVORITES_EXPANDED_STORAGE_KEY = 'reliant-favorites-expanded';

// Storage key for recent expanded state
const RECENT_EXPANDED_STORAGE_KEY = 'reliant-recent-expanded';

// Storage key for running expanded state
const RUNNING_EXPANDED_STORAGE_KEY = 'reliant-running-expanded';

// Storage key for collapsed directories within package types
// We store collapsed state, default is expanded
const COLLAPSED_DIRS_STORAGE_KEY = 'reliant-collapsed-dirs';

// Helper to get collapsed directories from localStorage
function getCollapsedDirs(): Set<string> {
  try {
    const stored = localStorage.getItem(COLLAPSED_DIRS_STORAGE_KEY);
    if (stored) {
      return new Set(JSON.parse(stored));
    }
  } catch (e) {
    console.error('Failed to load collapsed dirs:', e);
  }
  return new Set();
}

// Helper to save collapsed directories to localStorage
function saveCollapsedDirs(collapsed: Set<string>): void {
  try {
    localStorage.setItem(COLLAPSED_DIRS_STORAGE_KEY, JSON.stringify([...collapsed]));
  } catch (e) {
    console.error('Failed to save collapsed dirs:', e);
  }
}

// Group commands by directory (relative_path)
interface CommandsByDirectory {
  path: string;  // relative_path or "" for root
  displayName: string;  // "." for root, or the path
  commands: PackageCommand[];
}

function groupCommandsByDirectory(commands: PackageCommand[]): CommandsByDirectory[] {
  const byDir = new Map<string, PackageCommand[]>();
  
  for (const cmd of commands) {
    const path = cmd.relative_path || '';
    const existing = byDir.get(path) || [];
    existing.push(cmd);
    byDir.set(path, existing);
  }
  
  // Convert to array and sort (root first, then alphabetically)
  const result: CommandsByDirectory[] = [];
  const paths = Array.from(byDir.keys()).sort((a, b) => {
    if (a === '') return -1;
    if (b === '') return 1;
    return a.localeCompare(b);
  });
  
  for (const path of paths) {
    result.push({
      path,
      displayName: path || '.',
      commands: byDir.get(path) || [],
    });
  }
  
  return result;
}

// Helper to get dismissed processes from localStorage
function getDismissedProcesses(): Set<string> {
  try {
    const stored = localStorage.getItem(DISMISSED_STORAGE_KEY);
    if (stored) {
      return new Set(JSON.parse(stored));
    }
  } catch (e) {
    console.error('Failed to load dismissed processes:', e);
  }
  return new Set();
}

// Helper to save dismissed processes to localStorage
function saveDismissedProcesses(dismissed: Set<string>): void {
  try {
    localStorage.setItem(DISMISSED_STORAGE_KEY, JSON.stringify([...dismissed]));
  } catch (e) {
    console.error('Failed to save dismissed processes:', e);
  }
}

// Helper to get expanded types from localStorage
function getExpandedTypes(): Set<PackageType> {
  try {
    const stored = localStorage.getItem(EXPANDED_TYPES_STORAGE_KEY);
    if (stored) {
      return new Set(JSON.parse(stored) as PackageType[]);
    }
  } catch (e) {
    console.error('Failed to load expanded types:', e);
  }
  return new Set();
}

// Helper to save expanded types to localStorage
function saveExpandedTypes(expanded: Set<PackageType>): void {
  try {
    localStorage.setItem(EXPANDED_TYPES_STORAGE_KEY, JSON.stringify([...expanded]));
  } catch (e) {
    console.error('Failed to save expanded types:', e);
  }
}

function hasStoredExpandedTypes(): boolean {
  try {
    return localStorage.getItem(EXPANDED_TYPES_STORAGE_KEY) !== null;
  } catch {
    return false;
  }
}

// Helper to get favorites expanded state from localStorage
function getFavoritesExpanded(): boolean {
  try {
    const stored = localStorage.getItem(FAVORITES_EXPANDED_STORAGE_KEY);
    if (stored !== null) {
      return JSON.parse(stored);
    }
  } catch (e) {
    console.error('Failed to load favorites expanded state:', e);
  }
  return true; // Default to expanded
}

// Helper to save favorites expanded state to localStorage
function saveFavoritesExpanded(expanded: boolean): void {
  try {
    localStorage.setItem(FAVORITES_EXPANDED_STORAGE_KEY, JSON.stringify(expanded));
  } catch (e) {
    console.error('Failed to save favorites expanded state:', e);
  }
}

// Helper to get recent expanded state from localStorage
function getRecentExpanded(): boolean {
  try {
    const stored = localStorage.getItem(RECENT_EXPANDED_STORAGE_KEY);
    if (stored !== null) {
      return JSON.parse(stored);
    }
  } catch (e) {
    console.error('Failed to load recent expanded state:', e);
  }
  return true; // Default to expanded
}

// Helper to save recent expanded state to localStorage
function saveRecentExpanded(expanded: boolean): void {
  try {
    localStorage.setItem(RECENT_EXPANDED_STORAGE_KEY, JSON.stringify(expanded));
  } catch (e) {
    console.error('Failed to save recent expanded state:', e);
  }
}

// Helper to get running expanded state from localStorage
function getRunningExpanded(): boolean {
  try {
    const stored = localStorage.getItem(RUNNING_EXPANDED_STORAGE_KEY);
    if (stored !== null) {
      return JSON.parse(stored);
    }
  } catch (e) {
    console.error('Failed to load running expanded state:', e);
  }
  return true; // Default to expanded
}

// Helper to save running expanded state to localStorage
function saveRunningExpanded(expanded: boolean): void {
  try {
    localStorage.setItem(RUNNING_EXPANDED_STORAGE_KEY, JSON.stringify(expanded));
  } catch (e) {
    console.error('Failed to save running expanded state:', e);
  }
}

// Get relative time string
function getRelativeTime(timestamp: string): string {
  const now = new Date();
  const past = new Date(timestamp);
  const diffMs = now.getTime() - past.getTime();
  const diffSecs = Math.floor(diffMs / 1000);
  const diffMins = Math.floor(diffMs / 60000);
  const diffHours = Math.floor(diffMs / 3600000);

  if (diffSecs < 60) return "just now";
  if (diffMins < 60) return `${diffMins}m ago`;
  if (diffHours < 24) return `${diffHours}h ago`;
  return `${Math.floor(diffHours / 24)}d ago`;
}

// Format duration in human readable format
function formatDuration(startTime: string, endTime?: string): string {
  const start = new Date(startTime).getTime();
  const end = endTime ? new Date(endTime).getTime() : Date.now();
  const durationMs = Math.max(0, end - start);
  
  const totalSeconds = Math.floor(durationMs / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  
  if (hours > 0) return `${hours}h ${minutes}m ${seconds}s`;
  if (minutes > 0) return `${minutes}m ${seconds}s`;
  return `${seconds}s`;
}

// Hook for live updating duration
function useLiveDuration(startTime: string, endTime?: string, isRunning?: boolean): string {
  const [duration, setDuration] = useState(() => formatDuration(startTime, endTime));
  
  useEffect(() => {
    if (!isRunning || endTime) {
      setDuration(formatDuration(startTime, endTime));
      return;
    }
    
    const interval = setInterval(() => {
      setDuration(formatDuration(startTime, endTime));
    }, 1000);
    
    return () => clearInterval(interval);
  }, [startTime, endTime, isRunning]);
  
  return duration;
}

export function CommandsViewerTab({ worktreeId, processId: initialProcessId }: CommandsViewerTabProps) {
  // Container width tracking for responsive layouts
  const { width, containerRef } = useContainerWidth();
  const responsiveCtx = useMemo<ResponsiveContext>(() => ({
    isCompact: width > 0 && width < 320, // Compact mode for narrow containers
  }), [width]);

  // Background processes
  const backgroundProcesses = useProcessStore((state) => state.processes);
  const isLoadingBackground = useProcessStore((state) => state.isLoading);
  const backgroundError = useProcessStore((state) => state.error);
  const fetchBackgroundProcesses = useProcessStore((state) => state.fetchProcesses);
  const killBackgroundProcess = useProcessStore((state) => state.killProcess);
  const fetchProcessOutput = useProcessStore((state) => state.fetchProcessOutput);
  const processOutput = useProcessStore((state) => state.processOutput);
  const isLoadingOutput = useProcessStore((state) => state.isLoadingOutput);

  // Get project path as fallback when no worktree is selected
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectPath = currentProject?.path;

  // Browser store for opening port URLs
  const createBrowserTab = useBrowserStore((state) => state.createTab);
  const openBrowserViewer = useViewerStore((state) => state.openBrowserViewer);
  const getWorktreeTabs = useBrowserStore((state) => state.getWorktreeTabs);

  // Get workspace name from worktree store
  // Default to main worktree when no worktreeId is provided
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const mainWorktree = worktrees.find(w => w.is_main);
  const effectiveWorktreeId = worktreeId || mainWorktree?.id;
  const currentWorktree = effectiveWorktreeId ? worktrees.find(w => w.id === effectiveWorktreeId) : null;

  // Package commands via React Query (needs effectiveWorktreeId/projectPath)
  const commandsQuery = usePackageCommands(effectiveWorktreeId, projectPath);
  const commands = commandsQuery.data?.commands ?? ({} as Record<PackageType, PackageCommand[]>);
  const detectedTypes = commandsQuery.data?.detected_types ?? [];
  const isLoadingCommands = commandsQuery.isLoading;
  const packageError = commandsQuery.error?.message ?? null;

  // Package processes via React Query
  const processesQuery = usePackageProcesses();
  const packageProcesses = processesQuery.data ?? [];
  const isLoadingPackageProcesses = processesQuery.isLoading;

  // Mutations
  const runCommandMutation = useRunCommand();
  const killProcessMutation = useKillProcess();

  const [selectedProcessId, setSelectedProcessId] = useState<string | null>(initialProcessId || null);
  const [selectedProcessSource, setSelectedProcessSource] = useState<"package" | "background" | null>(null);
  const [expandedTypes, setExpandedTypes] = useState<Set<PackageType>>(() => getExpandedTypes());
  const [collapsedDirs, setCollapsedDirs] = useState<Set<string>>(() => getCollapsedDirs());
  const [runningCommandKey, setRunningCommandKey] = useState<string | null>(null);
  const [favorites, setFavorites] = useState<Set<string>>(new Set());
  const [, setFavoritesLoading] = useState(false);
  const [favoritesExpanded, setFavoritesExpanded] = useState(() => getFavoritesExpanded());
  const [recentExpanded, setRecentExpanded] = useState(() => getRecentExpanded());
  const [runningExpanded, setRunningExpanded] = useState(() => getRunningExpanded());
  const [dismissedProcesses, setDismissedProcesses] = useState<Set<string>>(() => getDismissedProcesses());
  const [searchQuery, setSearchQuery] = useState("");
  const [showAllWorkspaces, setShowAllWorkspaces] = useState(false);

  // When searching, we temporarily auto-expand relevant accordions and then restore.
  const preSearchExpansionRef = useRef<{
    expandedTypes: Set<PackageType>;
    collapsedDirs: Set<string>;
  } | null>(null);

  // Track the latest expansion state without re-triggering search effects.
  const latestExpandedTypesRef = useRef(expandedTypes);
  const latestCollapsedDirsRef = useRef(collapsedDirs);

  useEffect(() => {
    latestExpandedTypesRef.current = expandedTypes;
  }, [expandedTypes]);

  useEffect(() => {
    latestCollapsedDirsRef.current = collapsedDirs;
  }, [collapsedDirs]);

  // While searching, remember what the user explicitly collapsed so we don't auto-reopen it.
  const searchUserCollapsedRef = useRef<{
    types: Set<PackageType>;
    dirs: Set<string>;
  }>({
    types: new Set<PackageType>(),
    dirs: new Set<string>(),
  });

  // Get unified process counts for the "All Workspaces" button indicator
  // This uses ALL processes across all workspaces (stores are populated with unfiltered data)
  const {
    hasRunningInOtherWorkspaces,
    hasAnyProcesses,
  } = useUnifiedProcessCounts(effectiveWorktreeId);

  // Helper to get worktree name by ID
  const getWorktreeName = useCallback((wtId?: string) => {
    if (!wtId) return "Unknown";
    const wt = worktrees.find((w) => w.id === wtId);
    return wt?.name || wt?.branch || "Unknown";
  }, [worktrees]);

  // Helper to get relative path from a process's working_dir
  // Compares against the worktree path to extract the subdirectory
  const getProcessRelativePath = useCallback((process: UnifiedProcess) => {
    if (!process.working_dir) return undefined;
    
    // Get the worktree for this process
    const wt = process.worktree_id 
      ? worktrees.find(w => w.id === process.worktree_id)
      : currentWorktree;
    
    if (!wt?.path) return undefined;
    
    // Normalize paths for comparison
    const wtPath = wt.path.replace(/\/+$/, '');
    const procDir = process.working_dir.replace(/\/+$/, '');
    
    // If process ran from a subdirectory, extract the relative path
    if (procDir.startsWith(wtPath + '/')) {
      const relativePath = procDir.slice(wtPath.length + 1);
      return relativePath || undefined;
    }
    
    return undefined;
  }, [worktrees, currentWorktree]);

  const isLoading = isLoadingCommands || isLoadingPackageProcesses || isLoadingBackground;
  const error = packageError || backgroundError || null;

  // Merge and deduplicate processes
  const allProcesses = useMemo(() => {
    const packageIds = new Set(packageProcesses.map(p => p.id));
    // Create a map of background process IDs to their port info
    const backgroundPortsMap = new Map(
      backgroundProcesses.map(bp => [bp.id, bp.ports])
    );
    
    // Convert package processes, enriching with port info from background processes
    const unified = packageProcesses.map(p => {
      const unifiedProcess = toUnifiedProcess(p);
      // Enrich with ports from matching background process if available
      const ports = backgroundPortsMap.get(p.id);
      if (ports && ports.length > 0) {
        unifiedProcess.ports = ports.map(port => ({ port: port.port, protocol: port.protocol }));
      }
      return unifiedProcess;
    });
    
    // Add background processes that aren't already in package processes
    for (const bp of backgroundProcesses) {
      if (!packageIds.has(bp.id)) {
        unified.push(backgroundToUnified(bp));
      }
    }
    
    // Sort by start time (newest first)
    let sorted = unified.sort((a, b) => 
      new Date(b.start_time).getTime() - new Date(a.start_time).getTime()
    );
    
    // Filter by worktree if not showing all
    if (!showAllWorkspaces && effectiveWorktreeId) {
      sorted = sorted.filter(p => p.worktree_id === effectiveWorktreeId);
    }
    
    return sorted;
  }, [packageProcesses, backgroundProcesses, showAllWorkspaces, effectiveWorktreeId]);

  // Build a map of command keys to their associated processes
  const commandProcessMap = useMemo(() => {
    const map = new Map<string, { running?: UnifiedProcess; recent?: UnifiedProcess }>();
    
    const allCommands: PackageCommand[] = [];
    for (const type of detectedTypes) {
      allCommands.push(...(commands[type] || []));
    }
    
    for (const cmd of allCommands) {
      const key = getCommandKey(cmd);
      const matchingProcesses = allProcesses.filter(p => processMatchesCommand(p, cmd));
      
      const running = matchingProcesses.find(
        (p) => p.status === BackgroundProcessStatus.RUNNING,
      );
      const recent = matchingProcesses
        .filter((p) => p.status !== BackgroundProcessStatus.RUNNING)
        .sort((a, b) => new Date(b.start_time).getTime() - new Date(a.start_time).getTime())[0];
      
      if (running || recent) {
        map.set(key, { running, recent });
      }
    }
    
    return map;
  }, [commands, detectedTypes, allProcesses]);

  // Load background processes on mount
  // Package commands and processes are auto-fetched by React Query hooks above
  useEffect(() => {
    fetchBackgroundProcesses(undefined, false);
  }, [fetchBackgroundProcesses]);

  // Load command favorites from backend when project changes
  useEffect(() => {
    const projectId = currentProject?.id;
    if (!projectId) {
      setFavorites(new Set());
      return;
    }

    let cancelled = false;
    setFavoritesLoading(true);

    packageCommandsGrpc.getCommandFavorites(projectId)
      .then((commandKeys) => {
        if (!cancelled) {
          setFavorites(new Set(commandKeys));
        }
      })
      .catch((err) => {
        console.error('Failed to load command favorites:', err);
        if (!cancelled) {
          setFavorites(new Set());
        }
      })
      .finally(() => {
        if (!cancelled) {
          setFavoritesLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [currentProject?.id]);

  // Process list updates are now handled via WebSocket events in globalUpdatesStore
  // No polling needed - process_started/completed/failed events update the stores in real-time

  // Update selected process if initial processId changes
  useEffect(() => {
    if (initialProcessId) {
      setSelectedProcessId(initialProcessId);
      // Determine source
      const isPackage = packageProcesses.some(p => p.id === initialProcessId);
      setSelectedProcessSource(isPackage ? "package" : "background");
    }
  }, [initialProcessId, packageProcesses]);

  const handleRunCommand = useCallback(async (cmd: PackageCommand) => {
    const key = `${cmd.package_type}:${cmd.name}:${cmd.relative_path || ''}`;
    setRunningCommandKey(key);
    try {
      // Pass the command's working_dir so it runs from the correct directory
      const result = await runCommandMutation.mutateAsync({
        worktreeId: effectiveWorktreeId,
        path: projectPath,
        commandName: cmd.name,
        packageType: cmd.package_type,
        workingDir: cmd.working_dir,
      });
      
      if (result.process_id) {
        const displayName = cmd.relative_path ? `${cmd.relative_path}/${cmd.name}` : cmd.name;
        toast.success(`Started: ${displayName}`, {
          duration: 4000,
          action: {
            label: "View Logs",
            onClick: () => {
              setSelectedProcessId(result.process_id);
              setSelectedProcessSource("package");
            },
          },
        });
      }
    } finally {
      setRunningCommandKey(null);
    }
  }, [runCommandMutation, effectiveWorktreeId, projectPath]);

  const handleKillProcess = useCallback(async (processId: string, source: "package" | "background", processWorktreeId?: string) => {
    if (source === "package") {
      await killProcessMutation.mutateAsync(processId);
    } else {
      // Use the process's own worktree_id, falling back to effectiveWorktreeId
      const wtId = processWorktreeId || effectiveWorktreeId;
      if (wtId) {
        await killBackgroundProcess(processId, wtId);
      } else {
        toast.error('Cannot stop process: unknown workspace');
      }
    }
  }, [killProcessMutation, killBackgroundProcess, effectiveWorktreeId]);

  const handleSelectProcess = useCallback((process: UnifiedProcess) => {
    setSelectedProcessId(process.id);
    setSelectedProcessSource(process.source);
    if (process.source === "background") {
      fetchProcessOutput(process.id, false);
    }
  }, [fetchProcessOutput]);

  const toggleType = (type: PackageType) => {
    const isSearching = !!searchQuery.trim();
    setExpandedTypes((prev) => {
      const next = new Set(prev);
      const wasExpanded = prev.has(type);
      if (wasExpanded) {
        next.delete(type);
      } else {
        next.add(type);
      }
      if (isSearching) {
        if (wasExpanded) {
          searchUserCollapsedRef.current.types.add(type);
        } else {
          searchUserCollapsedRef.current.types.delete(type);
        }
      }
      saveExpandedTypes(next);
      return next;
    });
  };

  // Toggle expansion for a directory within a package type
  // Key format: "packageType:relativePath" (e.g., "npm:frontend" or "npm:" for root)
  // We track "collapsed" dirs - default is expanded, set contains collapsed dirs
  const toggleDir = (type: PackageType, relativePath: string) => {
    const key = `${type}:${relativePath}`;
    const isSearching = !!searchQuery.trim();
    setCollapsedDirs((prev) => {
      const next = new Set(prev);
      const wasCollapsed = prev.has(key);
      if (wasCollapsed) {
        // Was collapsed, now expand (remove from set)
        next.delete(key);
      } else {
        // Was expanded, now collapse (add to set)
        next.add(key);
      }
      if (isSearching) {
        if (!wasCollapsed) {
          searchUserCollapsedRef.current.dirs.add(key);
        } else {
          searchUserCollapsedRef.current.dirs.delete(key);
        }
      }
      saveCollapsedDirs(next);
      return next;
    });
  };

  // Default to expanded - set contains collapsed dirs
  const isDirExpanded = (type: PackageType, relativePath: string) => {
    const key = `${type}:${relativePath}`;
    return !collapsedDirs.has(key);
  };

  const toggleFavoritesExpanded = () => {
    setFavoritesExpanded((prev) => {
      const next = !prev;
      saveFavoritesExpanded(next);
      return next;
    });
  };

  const toggleRecentExpanded = () => {
    setRecentExpanded((prev) => {
      const next = !prev;
      saveRecentExpanded(next);
      return next;
    });
  };

  const toggleRunningExpanded = () => {
    setRunningExpanded((prev) => {
      const next = !prev;
      saveRunningExpanded(next);
      return next;
    });
  };

  const handleRefresh = useCallback(async () => {
    commandsQuery.refetch();
    processesQuery.refetch();
    fetchBackgroundProcesses(undefined, false);
    // Reload favorites from backend
    const projectId = currentProject?.id;
    if (projectId) {
      try {
        const commandKeys = await packageCommandsGrpc.getCommandFavorites(projectId);
        setFavorites(new Set(commandKeys));
      } catch (err) {
        console.error('Failed to refresh command favorites:', err);
      }
    }
  }, [commandsQuery, processesQuery, fetchBackgroundProcesses, currentProject?.id]);

  const toggleFavorite = useCallback(async (commandKey: string) => {
    const projectId = currentProject?.id;
    if (!projectId) {
      toast.error('No project selected');
      return;
    }

    const isCurrentlyFavorite = favorites.has(commandKey);
    const newIsFavorite = !isCurrentlyFavorite;

    // Optimistically update UI
    setFavorites(prev => {
      const next = new Set(prev);
      if (newIsFavorite) {
        next.add(commandKey);
      } else {
        next.delete(commandKey);
      }
      return next;
    });

    try {
      await packageCommandsGrpc.setCommandFavorite(projectId, commandKey, newIsFavorite);
    } catch (err) {
      console.error('Failed to update command favorite:', err);
      // Revert on error
      setFavorites(prev => {
        const next = new Set(prev);
        if (isCurrentlyFavorite) {
          next.add(commandKey);
        } else {
          next.delete(commandKey);
        }
        return next;
      });
      toast.error('Failed to update favorite');
    }
  }, [currentProject?.id, favorites]);

  const dismissProcess = useCallback((processId: string) => {
    setDismissedProcesses(prev => {
      const next = new Set(prev);
      next.add(processId);
      saveDismissedProcesses(next);
      return next;
    });
  }, []);

  // Find matching command for a process to enable rerun
  const findCommandForProcess = useCallback((process: UnifiedProcess): PackageCommand | undefined => {
    return findCommandForProcessUtil(process, commands, detectedTypes);
  }, [commands, detectedTypes]);

  const handleRerunProcess = useCallback(async (process: UnifiedProcess) => {
    const cmd = findCommandForProcess(process);
    if (cmd) {
      await handleRunCommand(cmd);
    } else {
      toast.error('Cannot rerun: command not found');
    }
  }, [findCommandForProcess, handleRunCommand]);

  const handleOpenPort = useCallback(async (port: number) => {
    const url = `http://localhost:${port}`;
    const projectId = currentProject?.id;
    if (projectId && effectiveWorktreeId) {
      const tabId = await createBrowserTab(effectiveWorktreeId, url, projectId);
      await openBrowserViewer(projectId, effectiveWorktreeId, tabId);
    }
  }, [currentProject?.id, effectiveWorktreeId, createBrowserTab, openBrowserViewer]);

  const runningProcesses = allProcesses.filter(
    (p) => p.status === BackgroundProcessStatus.RUNNING,
  );
  const recentProcesses = allProcesses
    .filter(
      (p) =>
        p.status !== BackgroundProcessStatus.RUNNING &&
        !dismissedProcesses.has(p.id),
    )
    .slice(0, 5);

  // Get favorited commands
  const favoriteCommands = useMemo(() => {
    const result: PackageCommand[] = [];
    for (const type of detectedTypes) {
      const typeCommands = commands[type] || [];
      for (const cmd of typeCommands) {
        const key = getCommandKey(cmd);
        if (favorites.has(key)) {
          result.push(cmd);
        }
      }
    }
    return result;
  }, [commands, detectedTypes, favorites]);

  // Filter commands by search query
  const matchesSearch = useCallback((cmd: PackageCommand) => {
    if (!searchQuery.trim()) return true;
    const query = searchQuery.toLowerCase();
    return (
      cmd.name.toLowerCase().includes(query) ||
      cmd.command.toLowerCase().includes(query) ||
      cmd.description?.toLowerCase().includes(query) ||
      cmd.category?.toLowerCase().includes(query)
    );
  }, [searchQuery]);

  // Default UI: expand the first detected package type (only if user has no stored preference yet).
  useEffect(() => {
    if (detectedTypes.length === 0) return;
    if (hasStoredExpandedTypes()) return;

    setExpandedTypes((prev) => {
      if (prev.size > 0) return prev;
      const next = new Set<PackageType>([detectedTypes[0]]);
      saveExpandedTypes(next);
      return next;
    });
  }, [detectedTypes]);

  // When searching, expand any closed sections that contain matches (without persisting).
  useEffect(() => {
    const hasQuery = !!searchQuery.trim();

    if (!hasQuery) {
      if (preSearchExpansionRef.current) {
        setExpandedTypes(preSearchExpansionRef.current.expandedTypes);
        setCollapsedDirs(preSearchExpansionRef.current.collapsedDirs);
        preSearchExpansionRef.current = null;
      }
      // Clear any per-search overrides.
      searchUserCollapsedRef.current.types = new Set<PackageType>();
      searchUserCollapsedRef.current.dirs = new Set<string>();
      return;
    }

    if (!preSearchExpansionRef.current) {
      preSearchExpansionRef.current = {
        expandedTypes: new Set(latestExpandedTypesRef.current),
        collapsedDirs: new Set(latestCollapsedDirsRef.current),
      };
    }

    const typesWithMatches = detectedTypes.filter((type) =>
      (commands[type] || []).some(matchesSearch)
    );

    // Expand the package types that have any matching commands.
    setExpandedTypes((prev) => {
      const next = new Set(prev);
      for (const t of typesWithMatches) {
        if (!searchUserCollapsedRef.current.types.has(t)) {
          next.add(t);
        }
      }
      return next;
    });

    // Expand any matching directories under those package types.
    setCollapsedDirs((prev) => {
      const next = new Set(prev);

      for (const type of typesWithMatches) {
        const matchedCommands = (commands[type] || []).filter(matchesSearch);
        const commandsByDir = groupCommandsByDirectory(matchedCommands);

        for (const dir of commandsByDir) {
          const key = `${type}:${dir.path}`;
          if (!searchUserCollapsedRef.current.dirs.has(key)) {
            next.delete(key); // remove from "collapsed" set => expanded
          }
        }
      }

      return next;
    });
  }, [searchQuery, detectedTypes, commands, matchesSearch]);

  // Auto-open browser tabs for processes with ports when commands appear in search results
  useEffect(() => {
    if (!searchQuery.trim() || !currentProject?.id) return;
    
    const projectId = currentProject.id;
    
    // Find all processes that match the search (check both processes and their associated commands)
    const query = searchQuery.toLowerCase();
    const processesWithPorts: UnifiedProcess[] = [];
    
    // First, check all processes directly
    for (const process of allProcesses) {
      if (process.command.toLowerCase().includes(query)) {
        if (process.ports && process.ports.length > 0 && process.worktree_id) {
          processesWithPorts.push(process);
        }
      }
    }
    
    // Then, check processes associated with matching commands
    for (const type of detectedTypes) {
      const typeCommands = commands[type] || [];
      for (const cmd of typeCommands) {
        if (matchesSearch(cmd)) {
          const key = getCommandKey(cmd);
          const processInfo = commandProcessMap.get(key);
          
          // Check both running and recent processes
          const process = processInfo?.running || processInfo?.recent;
          if (process && process.ports && process.ports.length > 0 && process.worktree_id) {
            // Avoid duplicates
            if (!processesWithPorts.find(p => p.id === process.id)) {
              processesWithPorts.push(process);
            }
          }
        }
      }
    }

    // Open tabs for all processes with ports
    const openTabsForProcesses = async () => {
      const openedUrls = new Set<string>(); // Track URLs we've already opened
      
      for (const process of processesWithPorts) {
        if (!process.worktree_id) continue;
        
        const worktreeId = process.worktree_id;
        const existingTabs = getWorktreeTabs(worktreeId);
        
        for (const portInfo of process.ports || []) {
          const port = portInfo.port;
          const url = `http://localhost:${port}`;
          
          // Skip if we've already opened this URL
          if (openedUrls.has(url)) continue;
          openedUrls.add(url);
          
          try {
            // Check if a tab already exists for this URL
            const existingTab = existingTabs.find(tab => tab.url === url);
            
            if (existingTab) {
              // Tab exists - always open it (openBrowserViewer will handle if already open)
              await openBrowserViewer(projectId, worktreeId, existingTab.id);
            } else {
              // Tab doesn't exist - create and open it
              const tabId = await createBrowserTab(worktreeId, url, projectId);
              await openBrowserViewer(projectId, worktreeId, tabId);
            }
          } catch (err) {
            console.error(`Failed to open tab for port ${port}:`, err);
          }
        }
      }
    };

    // Reduced debounce for faster response
    const timeoutId = setTimeout(() => {
      openTabsForProcesses();
    }, 300);

    return () => clearTimeout(timeoutId);
  }, [searchQuery, allProcesses, detectedTypes, commands, matchesSearch, commandProcessMap, currentProject?.id, getWorktreeTabs, createBrowserTab, openBrowserViewer]);

  // If a process is selected, show logs
  if (selectedProcessId) {
    // For package processes, use ProcessLogsViewer
    if (selectedProcessSource === "package") {
      // Try to find ports from unified processes (which may have port info from background process)
      const unifiedProcess = allProcesses.find(p => p.id === selectedProcessId);
      return (
        <ProcessLogsViewer
          processId={selectedProcessId}
          ports={unifiedProcess?.ports}
          onBack={() => {
            setSelectedProcessId(null);
            setSelectedProcessSource(null);
          }}
          onOpenPort={handleOpenPort}
        />
      );
    }
    
    // For background processes, use TerminalOutput with processStore output
    const selectedProcess = backgroundProcesses.find(p => p.id === selectedProcessId);
    if (selectedProcess) {
      const processInfo: ProcessInfo = {
        id: selectedProcess.id,
        command: selectedProcess.command,
        status: selectedProcess.status,
        start_time: selectedProcess.start_time,
        end_time: selectedProcess.end_time,
        exit_code: selectedProcess.exit_code,
        working_dir: selectedProcess.working_dir,
        ports: selectedProcess.ports,
      };
      
      return (
        <TerminalOutput
          process={processInfo}
          output={processOutput}
          isLoading={isLoadingOutput}
          onBack={() => {
            setSelectedProcessId(null);
            setSelectedProcessSource(null);
          }}
          onRefresh={() => fetchProcessOutput(selectedProcessId, false)}
          onKill={
            selectedProcess.status === BackgroundProcessStatus.RUNNING
              ? () => handleKillProcess(selectedProcessId, "background")
              : undefined
          }
          onOpenPort={handleOpenPort}
        />
      );
    }
  }

  return (
    <ResponsiveCtx.Provider value={responsiveCtx}>
    <div ref={containerRef} className="h-full flex flex-col overflow-hidden bg-background">
      {/* Header */}
      <SidebarHeader
        searchInput={
          showAllWorkspaces ? (
            <span className="text-sm font-medium text-foreground">
              All processes
            </span>
          ) : (
            <SidebarSearchInput
              value={searchQuery}
              onChange={setSearchQuery}
              placeholder="Search commands..."
            />
          )
        }
        statusBadge={
          runningProcesses.length > 0 ? (
            <span className="px-1.5 py-0.5 text-xs rounded-full bg-green-500/20 text-green-500 font-medium">
              {runningProcesses.length} running
            </span>
          ) : undefined
        }
        actions={
          <>
            {/* All Workspaces button - show when:
                1. There are running processes in other workspaces (main use case)
                2. Already showing all workspaces and there are any processes to show
            */}
            {(hasRunningInOtherWorkspaces || (showAllWorkspaces && hasAnyProcesses)) && (
              <SidebarHeaderButton
                icon={
                  <span className="relative">
                    <Layers className={cn("w-4 h-4", showAllWorkspaces && "text-primary")} />
                    {/* Show indicator when other workspaces have running processes */}
                    {!showAllWorkspaces && hasRunningInOtherWorkspaces && (
                      <span className="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full bg-green-500 animate-pulse" />
                    )}
                  </span>
                }
                onClick={() => setShowAllWorkspaces(!showAllWorkspaces)}
                tooltip={showAllWorkspaces ? "Showing all workspaces" : "Show all workspaces"}
              />
            )}
            <SidebarHeaderButton
              icon={<RefreshCw className={cn("w-4 h-4", isLoading && "animate-spin")} />}
              onClick={handleRefresh}
              tooltip="Refresh"
            />
          </>
        }
      />

      <div className="flex-1 overflow-auto">
        {/* Error message */}
        {error && (
          <div className="px-4 py-2 bg-destructive/10 text-destructive text-sm">
            {error}
          </div>
        )}

        {/* Running Processes - Collapsible */}
        {runningProcesses.length > 0 && (
          <div className="border-b border-border">
            <button
              onClick={toggleRunningExpanded}
              className={cn(
                "w-full flex items-center bg-green-500/5 hover:bg-green-500/10 transition-colors",
                responsiveCtx.isCompact ? "gap-1.5 px-2 py-1.5" : "gap-2 px-4 py-2"
              )}
            >
              {runningExpanded ? (
                <ChevronDown className="w-3 h-3 text-green-600 dark:text-green-400" />
              ) : (
                <ChevronRight className="w-3 h-3 text-green-600 dark:text-green-400" />
              )}
              <Activity className={cn(
                "text-green-600 dark:text-green-400",
                responsiveCtx.isCompact ? "w-3 h-3" : "w-3.5 h-3.5"
              )} />
              <span className={cn(
                "font-medium text-green-600 dark:text-green-400 uppercase tracking-wider",
                responsiveCtx.isCompact ? "text-[10px]" : "text-xs"
              )}>
                {responsiveCtx.isCompact ? "Run" : "Running"}
              </span>
              <span className="w-1.5 h-1.5 rounded-full bg-green-500 animate-pulse" />
              <span className={cn(
                "text-green-600 dark:text-green-400 ml-auto",
                responsiveCtx.isCompact ? "text-[10px]" : "text-xs"
              )}>
                {runningProcesses.length}{!responsiveCtx.isCompact && (runningProcesses.length === 1 ? ' process' : ' processes')}
              </span>
            </button>
            {runningExpanded && (
              <div className="divide-y divide-border">
                {runningProcesses.map((process) => (
                  <ProcessRow
                    key={process.id}
                    process={process}
                    onViewLogs={() => handleSelectProcess(process)}
                    onKill={() => handleKillProcess(process.id, process.source, process.worktree_id)}
                    onOpenPort={handleOpenPort}
                    showWorktree={showAllWorkspaces}
                    worktreeName={getWorktreeName(process.worktree_id)}
                    relativePath={getProcessRelativePath(process)}
                  />
                ))}
              </div>
            )}
          </div>
        )}

        {/* Recent Processes - Collapsible - only show when not viewing all workspaces */}
        {!showAllWorkspaces && recentProcesses.length > 0 && (
          <div className="border-b border-border">
            <button
              onClick={toggleRecentExpanded}
              className={cn(
                "w-full flex items-center bg-muted/30 hover:bg-muted/50 transition-colors",
                responsiveCtx.isCompact ? "gap-1.5 px-2 py-1.5" : "gap-2 px-4 py-2"
              )}
            >
              {recentExpanded ? (
                <ChevronDown className="w-3 h-3 text-muted-foreground" />
              ) : (
                <ChevronRight className="w-3 h-3 text-muted-foreground" />
              )}
              <Clock className={cn(
                "text-muted-foreground",
                responsiveCtx.isCompact ? "w-3 h-3" : "w-3.5 h-3.5"
              )} />
              <span className={cn(
                "font-medium text-muted-foreground uppercase tracking-wider",
                responsiveCtx.isCompact ? "text-[10px]" : "text-xs"
              )}>
                Recent
              </span>
              <span className={cn(
                "text-muted-foreground ml-auto",
                responsiveCtx.isCompact ? "text-[10px]" : "text-xs"
              )}>
                {recentProcesses.length}{!responsiveCtx.isCompact && (recentProcesses.length === 1 ? ' process' : ' processes')}
              </span>
            </button>
            {recentExpanded && (
              <div className="divide-y divide-border">
                {recentProcesses.map((process) => {
                  const canRerun = !!findCommandForProcess(process);
                  return (
                    <ProcessRow
                      key={process.id}
                      process={process}
                      onViewLogs={() => handleSelectProcess(process)}
                      canRerun={canRerun}
                      onRerun={canRerun ? () => handleRerunProcess(process) : undefined}
                      onDismiss={() => dismissProcess(process.id)}
                      onOpenPort={handleOpenPort}
                      showWorktree={showAllWorkspaces}
                      worktreeName={getWorktreeName(process.worktree_id)}
                      relativePath={getProcessRelativePath(process)}
                    />
                  );
                })}
              </div>
            )}
          </div>
        )}

        {/* Favorite Commands - Collapsible - only show when not viewing all workspaces */}
        {!showAllWorkspaces && (() => {
          const filteredFavorites = favoriteCommands.filter(matchesSearch);
          if (filteredFavorites.length === 0) return null;
          return (
            <div className="border-b border-border">
              <button
                onClick={toggleFavoritesExpanded}
                className={cn(
                  "w-full flex items-center bg-muted/30 hover:bg-muted/50 transition-colors",
                  responsiveCtx.isCompact ? "gap-1.5 px-2 py-1.5" : "gap-2 px-4 py-2"
                )}
              >
                {favoritesExpanded ? (
                  <ChevronDown className="w-3 h-3 text-muted-foreground" />
                ) : (
                  <ChevronRight className="w-3 h-3 text-muted-foreground" />
                )}
                <Star className={cn(
                  "text-muted-foreground",
                  responsiveCtx.isCompact ? "w-3 h-3" : "w-3.5 h-3.5"
                )} />
                <span className={cn(
                  "font-medium text-muted-foreground uppercase tracking-wider",
                  responsiveCtx.isCompact ? "text-[10px]" : "text-xs"
                )}>
                  {responsiveCtx.isCompact ? "Favs" : "Favorites"}
                </span>
                <span className={cn(
                  "text-muted-foreground ml-auto",
                  responsiveCtx.isCompact ? "text-[10px]" : "text-xs"
                )}>
                  {filteredFavorites.length}{!responsiveCtx.isCompact && (filteredFavorites.length === 1 ? ' command' : ' commands')}
                </span>
              </button>

              {favoritesExpanded && (
                <div className="divide-y divide-border/50">
                  {filteredFavorites.map((cmd) => {
                    const key = getCommandKey(cmd);
                    const isStarting = runningCommandKey === key;
                    const processInfo = commandProcessMap.get(key);
                    return (
                      <CommandRow
                        key={key}
                        command={cmd}
                        isStarting={isStarting}
                        runningProcess={undefined}
                        recentProcess={processInfo?.recent}
                        isFavorite={true}
                        onRun={() => handleRunCommand(cmd)}
                        onViewLogs={(process) => handleSelectProcess(process)}
                        onToggleFavorite={() => toggleFavorite(key)}
                        onOpenPort={handleOpenPort}
                        showPath={true}
                      />
                    );
                  })}
                </div>
              )}
            </div>
          );
        })()}

        {/* Commands by Package Type - only show when not viewing all workspaces */}
        {!showAllWorkspaces && detectedTypes.map((type) => {
          const typeCommands = (commands[type] || []).filter(matchesSearch);
          const commandsByDir = groupCommandsByDirectory(typeCommands);
          
          // Skip this type if no commands match the search
          if (typeCommands.length === 0) return null;
          
          // Check if we have multiple directories (need nested view)
          const hasMultipleDirs = commandsByDir.length > 1;
          
          return (
            <div key={type} className="border-b border-border">
              <button
                onClick={() => toggleType(type)}
                className={cn(
                  "w-full flex items-center bg-muted/30 hover:bg-muted/50 transition-colors",
                  responsiveCtx.isCompact ? "gap-1.5 px-2 py-1.5" : "gap-2 px-4 py-2"
                )}
              >
                {expandedTypes.has(type) ? (
                  <ChevronDown className="w-3 h-3 text-muted-foreground" />
                ) : (
                  <ChevronRight className="w-3 h-3 text-muted-foreground" />
                )}
                <span className="text-muted-foreground">{packageTypeIcons[type]}</span>
                <span className={cn(
                  "font-medium text-muted-foreground uppercase tracking-wider",
                  responsiveCtx.isCompact ? "text-[10px]" : "text-xs"
                )}>
                  {type}
                </span>
                <span className={cn(
                  "text-muted-foreground ml-auto",
                  responsiveCtx.isCompact ? "text-[10px]" : "text-xs"
                )}>
                  {typeCommands.length}{!responsiveCtx.isCompact && (typeCommands.length === 1 ? ' command' : ' commands')}
                </span>
              </button>

              {expandedTypes.has(type) && (
                <div>
                  {commandsByDir.map(({ path, displayName, commands: dirCommands }) => {
                    const filteredCmds = dirCommands.filter(matchesSearch);
                    if (filteredCmds.length === 0) return null;
                    
                    const dirKey = `${type}:${path}`;
                    const isExpanded = isDirExpanded(type, path);
                    
                    // If only one directory, don't show collapsible header
                    if (!hasMultipleDirs) {
                      return (
                        <div key={dirKey} className="divide-y divide-border/50">
                          {filteredCmds.map((cmd) => {
                            const key = getCommandKey(cmd);
                            const isStarting = runningCommandKey === key;
                            const processInfo = commandProcessMap.get(key);
                            const isFavorite = favorites.has(key);
                            return (
                              <CommandRow
                                key={key}
                                command={cmd}
                                isStarting={isStarting}
                                runningProcess={processInfo?.running}
                                recentProcess={processInfo?.recent}
                                isFavorite={isFavorite}
                                onRun={() => handleRunCommand(cmd)}
                                onViewLogs={(process) => handleSelectProcess(process)}
                                onToggleFavorite={() => toggleFavorite(key)}
                                onOpenPort={handleOpenPort}
                              />
                            );
                          })}
                        </div>
                      );
                    }
                    
                    // Multiple directories - show collapsible sections
                    return (
                      <div key={dirKey} className="border-t border-border/30 first:border-t-0">
                        <button
                          onClick={() => toggleDir(type, path)}
                          className={cn(
                            "w-full flex items-center hover:bg-muted/30 transition-colors",
                            responsiveCtx.isCompact ? "gap-1.5 px-3 py-1.5" : "gap-2 px-5 py-1.5"
                          )}
                        >
                          {isExpanded ? (
                            <ChevronDown className="w-2.5 h-2.5 text-muted-foreground" />
                          ) : (
                            <ChevronRight className="w-2.5 h-2.5 text-muted-foreground" />
                          )}
                          <Layers className="w-3 h-3 text-muted-foreground/70" />
                          <span className={cn(
                            "font-medium text-muted-foreground",
                            responsiveCtx.isCompact ? "text-[10px]" : "text-xs"
                          )}>
                            {displayName === '.' ? '(root)' : `${displayName}/`}
                          </span>
                          <span className={cn(
                            "text-muted-foreground/60 ml-auto",
                            responsiveCtx.isCompact ? "text-[9px]" : "text-[10px]"
                          )}>
                            {filteredCmds.length}
                          </span>
                        </button>
                        
                        {isExpanded && (
                          <div className="divide-y divide-border/50">
                            {filteredCmds.map((cmd) => {
                              const key = getCommandKey(cmd);
                              const isStarting = runningCommandKey === key;
                              const processInfo = commandProcessMap.get(key);
                              const isFavorite = favorites.has(key);
                              return (
                                <CommandRow
                                  key={key}
                                  command={cmd}
                                  isStarting={isStarting}
                                  runningProcess={processInfo?.running}
                                  recentProcess={processInfo?.recent}
                                  isFavorite={isFavorite}
                                  onRun={() => handleRunCommand(cmd)}
                                  onViewLogs={(process) => handleSelectProcess(process)}
                                  onToggleFavorite={() => toggleFavorite(key)}
                                  onOpenPort={handleOpenPort}
                                  indentLevel={2}
                                />
                              );
                            })}
                          </div>
                        )}
                      </div>
                    );
                  })}
                </div>
              )}
            </div>
          );
        })}

        {/* Empty state */}
        {showAllWorkspaces && allProcesses.length === 0 && !isLoading && !error && (
          <SidebarEmptyState
            icon={Terminal}
            title="No running processes"
            description="Start a process in any workspace to see it here"
          />
        )}
        {!showAllWorkspaces && detectedTypes.length === 0 && allProcesses.length === 0 && !isLoading && !error && (
          <SidebarEmptyState
            icon={Terminal}
            title="No processes or commands"
            description="Add a Makefile, package.json, or Taskfile.yml to see commands"
          />
        )}
      </div>
    </div>
    </ResponsiveCtx.Provider>
  );
}

interface CommandRowProps {
  command: PackageCommand;
  isStarting: boolean;
  runningProcess?: UnifiedProcess;
  recentProcess?: UnifiedProcess;
  isFavorite: boolean;
  onRun: () => void;
  onViewLogs: (process: UnifiedProcess) => void;
  onToggleFavorite: () => void;
  onOpenPort: (port: number) => void;
  indentLevel?: number;  // 0 = no indent, 1 = under package type, 2 = under directory
  showPath?: boolean;    // Show directory badge (for favorites section)
}

function CommandRow({ command, isStarting, runningProcess, recentProcess, isFavorite, onRun, onViewLogs, onToggleFavorite, onOpenPort, indentLevel = 0, showPath = false }: CommandRowProps) {
  const { isCompact } = useResponsive();
  const isRunning = !!runningProcess;
  const isDisabled = isStarting || isRunning;
  
  const getStatusIndicator = (compact = false) => {
    if (isStarting) {
      return (
        <span className="flex items-center gap-1 text-xs text-blue-500 flex-shrink-0">
          <Loader2 className="w-3 h-3 animate-spin" />
          {!compact && <span>Starting...</span>}
        </span>
      );
    }
    
    if (isRunning) {
      return (
        <span className="flex items-center gap-1 text-xs text-green-500 flex-shrink-0">
          <span className="w-1.5 h-1.5 rounded-full bg-green-500 animate-pulse" />
          {!compact && <span>Running</span>}
        </span>
      );
    }
    
    if (recentProcess) {
      const isKilled =
        recentProcess.status === BackgroundProcessStatus.KILLED ||
        recentProcess.status === BackgroundProcessStatus.KILLED_EXTERNALLY;
      const isSuccess =
        recentProcess.status === BackgroundProcessStatus.COMPLETED &&
        (recentProcess.exit_code === 0 || recentProcess.exit_code === undefined);
      const isFailed =
        recentProcess.status === BackgroundProcessStatus.FAILED ||
        (!isKilled &&
          recentProcess.exit_code !== undefined &&
          recentProcess.exit_code !== 0);
      const relativeTime = getRelativeTime(recentProcess.end_time || recentProcess.start_time);
      
      if (isSuccess) {
        return (
          <span className="flex items-center gap-1 text-xs text-muted-foreground flex-shrink-0" title={`Completed ${relativeTime}`}>
            <CheckCircle className="w-3 h-3 text-green-500" />
            {!compact && <span className="hidden group-hover:inline">{relativeTime}</span>}
          </span>
        );
      }
      
      if (isFailed) {
        return (
          <span className="flex items-center gap-1 text-xs text-muted-foreground flex-shrink-0" title={`Failed ${relativeTime} (exit ${recentProcess.exit_code})`}>
            <XCircle className="w-3 h-3 text-red-500" />
            {!compact && <span className="hidden group-hover:inline">{relativeTime}</span>}
          </span>
        );
      }
      
      // Killed or other status - show as neutral (yellow/stopped)
      return (
        <span className="flex items-center gap-1 text-xs text-muted-foreground flex-shrink-0" title={`${isKilled ? 'Stopped' : recentProcess.status} ${relativeTime}`}>
          <Square className="w-3 h-3 text-yellow-500" />
          {!compact && <span className="hidden group-hover:inline">{relativeTime}</span>}
        </span>
      );
    }
    
    return null;
  };

  // Compute left padding based on indent level
  const getLeftPadding = () => {
    if (isCompact) return indentLevel >= 2 ? 'pl-5 pr-2' : 'px-3';
    return indentLevel >= 2 ? 'pl-7 pr-3' : 'px-3';
  };

  // Compact/Wide layout
  return (
    <div className={cn(
      "flex items-center hover:bg-muted/30 transition-colors group",
      isRunning && "bg-green-500/5 hover:bg-green-500/10",
      isCompact ? "gap-2 py-1.5" : "gap-3 py-2",
      getLeftPadding()
    )}>
      {/* Run button */}
      <button
        onClick={onRun}
        disabled={isDisabled}
        className={cn(
          "rounded transition-colors flex-shrink-0",
          isRunning ? "bg-green-500/20 text-green-500" : "bg-primary/10 hover:bg-primary/20 text-primary",
          isDisabled && "opacity-50 cursor-not-allowed",
          isCompact ? "p-1" : "p-1.5"
        )}
        title={isRunning ? `${command.name} is running` : `Run ${command.name}`}
      >
        {isStarting ? (
          <Loader2 className="w-3 h-3 animate-spin" />
        ) : isRunning ? (
          <span className="w-3 h-3 flex items-center justify-center">
            <span className="w-2 h-2 rounded-full bg-green-500 animate-pulse" />
          </span>
        ) : (
          <Play className="w-3 h-3" />
        )}
      </button>
      
      {/* Command info */}
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-1.5">
          <span className={cn("font-mono truncate", isCompact ? "text-sm" : "text-sm")}>{command.name}</span>
          {/* Show directory path badge in favorites */}
          {showPath && command.relative_path && (
            <span className="text-[10px] text-muted-foreground bg-muted/70 border border-border/50 px-1.5 py-0.5 rounded flex-shrink-0">
              {command.relative_path}
            </span>
          )}
          <button
            onClick={(e) => { e.stopPropagation(); onToggleFavorite(); }}
            className={cn(
              "p-0.5 rounded transition-colors flex-shrink-0",
              isFavorite ? "text-yellow-500 hover:text-yellow-400" : "text-muted-foreground/20 hover:text-yellow-500 group-hover:text-muted-foreground/50"
            )}
            title={isFavorite ? "Remove from favorites" : "Add to favorites"}
          >
            <Star className={cn(isCompact ? "w-3 h-3" : "w-3.5 h-3.5", isFavorite && "fill-current")} />
          </button>
          {getStatusIndicator(isCompact)}
          {/* Show ports for running processes */}
          {runningProcess?.ports && runningProcess.ports.length > 0 && !isCompact && (
            <span className="flex items-center gap-1">
              {runningProcess.ports.slice(0, 2).map((p) => (
                <button
                  key={p.port}
                  onClick={(e) => { e.stopPropagation(); onOpenPort(p.port); }}
                  className="flex items-center gap-0.5 text-xs text-primary hover:text-primary/80 hover:underline"
                  title={`Open localhost:${p.port} in browser`}
                >
                  <Globe className="w-3 h-3" />
                  <span>{p.port}</span>
                </button>
              ))}
            </span>
          )}
        </div>
        {command.description && !isCompact && (
          <div className="text-xs text-muted-foreground truncate">
            {command.description}
          </div>
        )}
      </div>
      
      {/* Actions */}
      <div className="flex items-center gap-1 flex-shrink-0">
        {(runningProcess || recentProcess) && (
          <button
            onClick={() => onViewLogs(runningProcess || recentProcess!)}
            className={cn(
              "rounded transition-colors flex items-center gap-1",
              runningProcess 
                ? "bg-green-500/20 hover:bg-green-500/30 text-green-500" 
                : "hover:bg-muted text-muted-foreground hover:text-foreground opacity-0 group-hover:opacity-100",
              isCompact ? "p-1" : "p-1.5"
            )}
            title="View logs"
          >
            <ExternalLink className="w-3 h-3" />
            {runningProcess && !isCompact && <span className="text-xs">Logs</span>}
          </button>
        )}
        
        {!isCompact && (
          <code className={cn(
            "text-[10px] text-muted-foreground/70 font-mono truncate max-w-[100px]",
            (runningProcess || recentProcess) ? "hidden" : "hidden group-hover:block"
          )}>
            {command.command}
          </code>
        )}
      </div>
    </div>
  );
}

interface ProcessRowProps {
  process: UnifiedProcess;
  onViewLogs: () => void;
  onKill?: () => Promise<void> | void;
  onRerun?: () => void;
  canRerun?: boolean;
  onDismiss?: () => void;
  onOpenPort?: (port: number) => void;
  worktreeName?: string;
  showWorktree?: boolean;
  relativePath?: string;  // Subdirectory path (e.g., "web") for commands from subdirectories
}

function ProcessRow({ process, onViewLogs, onKill, onRerun, canRerun, onDismiss, onOpenPort, worktreeName, showWorktree, relativePath }: ProcessRowProps) {
  const { isCompact } = useResponsive();
  const [isCanceling, setIsCanceling] = useState(false);
  const isRunning = process.status === BackgroundProcessStatus.RUNNING;
  const duration = useLiveDuration(process.start_time, process.end_time, isRunning);

  // Reset canceling state when process stops running
  useEffect(() => {
    if (!isRunning) {
      setIsCanceling(false);
    }
  }, [isRunning]);

  const handleKill = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (isCanceling || !onKill) return;
    setIsCanceling(true);
    try {
      await onKill();
    } catch {
      setIsCanceling(false);
    }
  };
  
  // Extract command name from full command
  const getShortCommand = (cmd: string) => {
    const parts = cmd.split(/\s+/);
    if (parts[0] === "npm" && parts[1] === "run" && parts[2]) {
      return parts[2];
    }
    if (parts[0] === "make" && parts[1]) {
      return parts[1];
    }
    if (parts[0] === "task" && parts[1]) {
      return parts[1];
    }
    return cmd.length > 30 ? cmd.substring(0, 30) + "..." : cmd;
  };

  const getStatusConfig = () => {
    switch (process.status) {
      case BackgroundProcessStatus.RUNNING:
        return {
          bg: "bg-green-500/10",
          border: "border-green-500/30",
          dot: "bg-green-500 animate-pulse",
          text: "text-green-500",
          label: "Running",
          shortLabel: "Run",
        };
      case BackgroundProcessStatus.COMPLETED:
        return {
          bg: "bg-muted/30",
          border: "border-border",
          dot: "bg-muted-foreground",
          text: "text-muted-foreground",
          label: process.exit_code === 0 ? "Completed" : `Exit ${process.exit_code}`,
          shortLabel: process.exit_code === 0 ? "OK" : `E${process.exit_code}`,
        };
      case BackgroundProcessStatus.FAILED:
        return {
          bg: "bg-red-500/5",
          border: "border-red-500/20",
          dot: "bg-red-500",
          text: "text-red-500",
          label: `Failed (${process.exit_code})`,
          shortLabel: `Fail`,
        };
      case BackgroundProcessStatus.KILLED:
      case BackgroundProcessStatus.KILLED_EXTERNALLY:
        return {
          bg: "bg-yellow-500/5",
          border: "border-yellow-500/20",
          dot: "bg-yellow-500",
          text: "text-yellow-500",
          label: "Killed",
          shortLabel: "Kill",
        };
      default:
        return {
          bg: "bg-muted/30",
          border: "border-border",
          dot: "bg-muted-foreground",
          text: "text-muted-foreground",
          label: "Unknown",
          shortLabel: "Unk",
        };
    }
  };

  const config = getStatusConfig();
  const shortCommand = getShortCommand(process.command);

  // Single unified layout that adapts to compact mode
  return (
    <div 
      onClick={onViewLogs}
      className={cn(
        "flex items-center cursor-pointer transition-all group",
        "border-l-2 hover:bg-muted/50",
        config.bg,
        isRunning ? "border-l-green-500" : "border-l-transparent hover:border-l-primary/50",
        isCompact ? "gap-2 px-2 py-2" : "gap-3 px-4 py-3"
      )}
    >
      {/* Status dot */}
      <div className={cn("rounded-full flex-shrink-0", config.dot, isCompact ? "w-2 h-2" : "w-2.5 h-2.5")} />
      
      {/* Process info */}
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className={cn("font-mono font-medium truncate", isCompact ? "text-sm" : "text-sm")} title={shortCommand}>
            {shortCommand}
          </span>
          {/* Show relative path (directory) for subdirectory commands */}
          {relativePath && (
            <span className={cn(
              "text-muted-foreground bg-muted/70 border border-border/50 rounded flex-shrink-0",
              isCompact ? "text-[9px] px-1 py-0.5" : "text-[10px] px-1.5 py-0.5"
            )}>
              {relativePath}
            </span>
          )}
          {showWorktree && worktreeName && (
            <span className={cn(
              "rounded bg-accent/50 text-foreground/70 truncate",
              isCompact ? "text-[10px] px-1 py-0.5 max-w-[60px]" : "text-xs px-1.5 py-0.5 max-w-[80px]"
            )} title={worktreeName}>
              {worktreeName}
            </span>
          )}
          <span className={cn(
            "rounded flex-shrink-0",
            config.text, config.bg,
            isCompact ? "text-[10px] px-1 py-0.5" : "text-xs px-1.5 py-0.5"
          )}>
            {isCompact ? config.shortLabel : config.label}
          </span>
          {process.ports && process.ports.length > 0 && onOpenPort && (
            <PortsDisplay 
              ports={process.ports} 
              onOpenPort={onOpenPort}
              compact
              maxVisible={isCompact ? 1 : 2}
            />
          )}
        </div>
        <div className={cn(
          "flex items-center mt-0.5 text-xs text-muted-foreground",
          isCompact ? "gap-2" : "gap-3"
        )}>
          <span className="flex items-center gap-1 font-mono flex-shrink-0">
            <Clock className="w-3 h-3" />
            {duration}
          </span>
          {!isCompact && (
            <span className="truncate opacity-60" title={process.command}>
              {process.command}
            </span>
          )}
        </div>
      </div>
      
      {/* Actions */}
      <div className="flex items-center gap-1 flex-shrink-0">
        {!isRunning && canRerun && onRerun && (
          <button
            onClick={(e) => { e.stopPropagation(); onRerun(); }}
            className={cn("rounded hover:bg-primary/20 text-primary transition-colors", isCompact ? "p-1" : "p-1.5")}
            title="Rerun command"
          >
            <RotateCw className={isCompact ? "w-3 h-3" : "w-3.5 h-3.5"} />
          </button>
        )}
        <button
          onClick={(e) => { e.stopPropagation(); onViewLogs(); }}
          className={cn(
            "rounded transition-colors flex items-center gap-1",
            isRunning ? "bg-green-500/20 hover:bg-green-500/30 text-green-500" : "hover:bg-muted text-muted-foreground",
            isCompact ? "p-1" : "p-1.5"
          )}
          title="View logs"
        >
          <ExternalLink className={isCompact ? "w-3 h-3" : "w-3.5 h-3.5"} />
          {isRunning && !isCompact && <span className="text-xs font-medium">Logs</span>}
        </button>
        {isRunning && onKill && (
          <button
            onClick={handleKill}
            disabled={isCanceling}
            className={cn(
              "rounded transition-colors",
              isCanceling ? "text-yellow-500 cursor-wait" : "hover:bg-destructive/20 text-destructive",
              isCompact ? "p-1" : "p-1.5"
            )}
            title={isCanceling ? "Stopping..." : "Stop process"}
          >
            {isCanceling ? (
              <Loader2 className={isCompact ? "w-3 h-3 animate-spin" : "w-3.5 h-3.5 animate-spin"} />
            ) : (
              <Square className={isCompact ? "w-3 h-3" : "w-3.5 h-3.5"} />
            )}
          </button>
        )}
        {!isRunning && onDismiss && (
          <button
            onClick={(e) => { e.stopPropagation(); onDismiss(); }}
            className={cn(
              "rounded hover:bg-muted text-muted-foreground/50 hover:text-muted-foreground transition-colors",
              isCompact ? "p-1" : "p-1.5"
            )}
            title="Remove from list"
          >
            <X className={isCompact ? "w-3 h-3" : "w-3.5 h-3.5"} />
          </button>
        )}
      </div>
    </div>
  );
}