/**
 * Centralized node type metadata — icon, color, and display name lookups.
 *
 * Primary source: catalog API (NodeInfo from CatalogService.ListNodes).
 * Fallbacks: static maps keyed by node ID and category for robustness
 * when API data hasn't loaded or a node type is unknown.
 */

import {
  Zap, GitCommit, GitBranch, GitFork, FolderGit2, Cog, Database, FileText,
  Settings, Play, Check, Clock, Trash, Trash2, RefreshCw, Save, Upload,
  Download, Send, Lock, Unlock, Key, Search, Filter, Plus, Minus, Edit,
  Copy, Move, Archive, Unlink, Link, Eye, EyeOff, Bell, BellOff, Terminal,
  Code, Package, Cpu, Server, Cloud, Globe, Wifi, WifiOff, Power, PowerOff,
  AlertTriangle, AlertCircle, Info, HelpCircle, CheckCircle, XCircle,
  Bot, Wrench, Minimize2, GitMerge, Sparkles,
  FolderMinus,
  type LucideIcon,
} from 'lucide-react'
import { getCatalogClient } from '../api/grpc-client'
import type { NodeInfo } from '../gen/reliant/v1/catalog_pb'
import type { NodeTheme } from '../components/workflow/nodes/NodeStatusWrapper'

// ---------------------------------------------------------------------------
// Icon resolution: icon_hint string -> Lucide component
// ---------------------------------------------------------------------------

/** Maps icon_hint / node-id strings to Lucide icons. */
const ICON_MAP: Record<string, LucideIcon> = {
  // Node type IDs (snake_case) — primary lookups
  'call_llm': Bot,
  'execute_tools': Wrench,
  'compact': Minimize2,
  'approval': CheckCircle,
  'save_message': Save,
  'create_worktree': FolderGit2,
  'delete_worktree': Trash2,

  // Structural / control-flow node types
  'run': Terminal,
  'workflow': Bot,
  'join': GitMerge,
  'loop': RefreshCw,
  'switch': GitBranch,
  'router': GitFork,

  // Generic icon-hint values (kebab & lowercase)
  'git-commit': GitCommit,
  'git-branch': GitBranch,
  'git-fork': GitFork,
  'GitFork': GitFork,
  'git': GitCommit,
  'worktree': GitBranch,
  'worktree-create': FolderGit2,
  'worktree-delete': Trash2,
  'folder-minus': FolderMinus,
  'settings': Settings,
  'cog': Cog,
  'config': Settings,
  'database': Database,
  'db': Database,
  'file': FileText,
  'file-text': FileText,
  'document': FileText,
  'play': Play,
  'check': Check,
  'checkmark': Check,
  'clock': Clock,
  'timer': Clock,
  'trash': Trash,
  'delete': Trash,
  'refresh': RefreshCw,
  'reload': RefreshCw,
  'save': Save,
  'upload': Upload,
  'download': Download,
  'send': Send,
  'lock': Lock,
  'unlock': Unlock,
  'key': Key,
  'search': Search,
  'filter': Filter,
  'plus': Plus,
  'add': Plus,
  'minus': Minus,
  'remove': Minus,
  'edit': Edit,
  'copy': Copy,
  'move': Move,
  'archive': Archive,
  'link': Link,
  'unlink': Unlink,
  'eye': Eye,
  'eye-off': EyeOff,
  'visible': Eye,
  'hidden': EyeOff,
  'bell': Bell,
  'bell-off': BellOff,
  'notification': Bell,
  'terminal': Terminal,
  'code': Code,
  'package': Package,
  'cpu': Cpu,
  'server': Server,
  'cloud': Cloud,
  'globe': Globe,
  'wifi': Wifi,
  'wifi-off': WifiOff,
  'power': Power,
  'power-off': PowerOff,
  'warning': AlertTriangle,
  'alert': AlertCircle,
  'info': Info,
  'help': HelpCircle,
  'success': CheckCircle,
  'error': XCircle,
  'zap': Zap,
  'action': Zap,
  'sparkles': Sparkles,
  'bot': Bot,
}

/** Fallback icons by category when no exact match found. */
const CATEGORY_ICON_MAP: Record<string, LucideIcon> = {
  'agentic': Bot,
  'git': GitCommit,
  'utility': Terminal,
  'flow': GitBranch,
}

// ---------------------------------------------------------------------------
// Color resolution: category -> Tailwind color tokens
// ---------------------------------------------------------------------------

export interface NodeColorSet {
  bg: string
  text: string
  border: string
}

/**
 * Per–node-type color overrides (small, finite list).
 * When a node ID doesn't appear here we fall back to category-based colors.
 */
const NODE_COLOR_MAP: Record<string, NodeColorSet> = {
  'call_llm':         { bg: 'bg-emerald-500', text: 'text-emerald-600', border: 'border-emerald-500' },
  'execute_tools':    { bg: 'bg-orange-500',   text: 'text-orange-600',  border: 'border-orange-500' },
  'compact':          { bg: 'bg-pink-500',     text: 'text-pink-600',    border: 'border-pink-500' },
  'approval':         { bg: 'bg-amber-500',    text: 'text-amber-600',   border: 'border-amber-500' },
  'save_message':     { bg: 'bg-blue-500',     text: 'text-blue-600',    border: 'border-blue-500' },
  'create_worktree':  { bg: 'bg-cyan-500',     text: 'text-cyan-600',    border: 'border-cyan-500' },
  'delete_worktree':  { bg: 'bg-rose-500',     text: 'text-rose-600',    border: 'border-rose-500' },
  // Structural nodes
  'run':              { bg: 'bg-indigo-500',   text: 'text-indigo-600',  border: 'border-indigo-500' },
  'workflow':         { bg: 'bg-purple-500',   text: 'text-purple-600',  border: 'border-purple-500' },
  'join':             { bg: 'bg-teal-500',     text: 'text-teal-600',    border: 'border-teal-500' },
  'loop':             { bg: 'bg-violet-500',   text: 'text-violet-600',  border: 'border-violet-500' },
  'switch':           { bg: 'bg-sky-500',      text: 'text-sky-600',     border: 'border-sky-500' },
  'router':           { bg: 'bg-amber-500',    text: 'text-amber-600',   border: 'border-amber-500' },
}

/** Category-level fallback colors. */
const CATEGORY_COLOR_MAP: Record<string, NodeColorSet> = {
  'agentic':            { bg: 'bg-emerald-500', text: 'text-emerald-600', border: 'border-emerald-500' },
  'git':                { bg: 'bg-cyan-500',    text: 'text-cyan-600',    border: 'border-cyan-500' },
  'flow':               { bg: 'bg-purple-500',  text: 'text-purple-600',  border: 'border-purple-500' },
  'utility':            { bg: 'bg-indigo-500',  text: 'text-indigo-600',  border: 'border-indigo-500' },
}

const DEFAULT_COLOR: NodeColorSet = { bg: 'bg-emerald-500', text: 'text-emerald-600', border: 'border-emerald-500' }

// ---------------------------------------------------------------------------
// Theme mapping (for NodeStatusWrapper)
// ---------------------------------------------------------------------------

const NODE_THEME_MAP: Record<string, NodeTheme> = {
  'call_llm': 'emerald',
  'execute_tools': 'orange',
  'compact': 'pink',
  'approval': 'amber',
  'save_message': 'blue',
  'create_worktree': 'cyan',
  'delete_worktree': 'rose',
  'run': 'indigo',
  'workflow': 'purple',
  'join': 'teal',
  'loop': 'violet',
  'switch': 'sky',
  'router': 'amber',
}

const CATEGORY_THEME_MAP: Record<string, NodeTheme> = {
  'agentic': 'emerald',
  'git': 'cyan',
  'flow': 'purple',
  'utility': 'indigo',
}

// ---------------------------------------------------------------------------
// Display name formatting
// ---------------------------------------------------------------------------

const ACRONYMS: Record<string, string> = { llm: 'LLM', api: 'API', url: 'URL', id: 'ID', mcp: 'MCP' }

/** Convert a snake_case node type to a title-cased display name. */
function formatNodeTypeName(nodeType: string): string {
  return nodeType
    .split('_')
    .map((word: string) => ACRONYMS[word.toLowerCase()] || word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ')
}

// ---------------------------------------------------------------------------
// Catalog cache (singleton, fetched once)
// ---------------------------------------------------------------------------

let _cachedNodes: NodeInfo[] | null = null
let _fetchPromise: Promise<NodeInfo[]> | null = null

/** Fetch and cache the catalog nodes list. Safe to call multiple times. */
export async function ensureNodesCached(): Promise<NodeInfo[]> {
  if (_cachedNodes) return _cachedNodes
  if (_fetchPromise) return _fetchPromise
  _fetchPromise = (async () => {
    try {
      const client = getCatalogClient()
      const response = await client.listNodes({})
      _cachedNodes = response.nodes || []
    } catch (err) {
      console.error('Failed to fetch catalog nodes:', err)
      _cachedNodes = []
    }
    return _cachedNodes
  })()
  return _fetchPromise
}

/** Get cached nodes synchronously (returns [] if not yet fetched). */
export function getCachedNodes(): NodeInfo[] {
  return _cachedNodes ?? []
}

/** Lookup a single cached NodeInfo by id. */
function getCachedNode(nodeId: string): NodeInfo | undefined {
  return _cachedNodes?.find(n => n.id === nodeId)
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Resolve a Lucide icon component for a node type.
 *
 * Resolution order:
 *  1. Catalog `iconHint` (from API) looked up in ICON_MAP
 *  2. Exact node-id match in ICON_MAP
 *  3. Category-based fallback (from API or CATEGORY_ICON_MAP)
 *  4. Partial-match scan of ICON_MAP keys
 *  5. Default: Zap
 */
export function getNodeIcon(nodeId: string): LucideIcon {
  const key = nodeId.toLowerCase()

  // 1. Try catalog icon_hint
  const cached = getCachedNode(key)
  if (cached?.iconHint && ICON_MAP[cached.iconHint]) {
    return ICON_MAP[cached.iconHint]
  }

  // 2. Exact node-id match
  if (ICON_MAP[key]) return ICON_MAP[key]

  // 3. Category-based fallback
  const category = cached?.category
  if (category && CATEGORY_ICON_MAP[category]) {
    return CATEGORY_ICON_MAP[category]
  }

  // 4. Partial-match scan
  for (const [k, icon] of Object.entries(ICON_MAP)) {
    if (key.includes(k)) return icon
  }
  for (const [cat, icon] of Object.entries(CATEGORY_ICON_MAP)) {
    if (key.includes(cat)) return icon
  }

  return Zap
}

/**
 * Resolve color set (bg, text, border Tailwind classes) for a node type.
 *
 * Resolution: node-id -> catalog category -> category map -> default.
 */
export function getNodeColor(nodeId: string): NodeColorSet {
  const key = nodeId.toLowerCase()

  if (NODE_COLOR_MAP[key]) return NODE_COLOR_MAP[key]

  const category = getCachedNode(key)?.category
  if (category && CATEGORY_COLOR_MAP[category]) return CATEGORY_COLOR_MAP[category]

  return DEFAULT_COLOR
}

/** Convenience: return just the `bg` class string. */
export function getNodeBgColor(nodeId: string): string {
  return getNodeColor(nodeId).bg
}

/**
 * Resolve the theme string used by NodeStatusWrapper handle coloring.
 */
export function getNodeTheme(nodeId: string): NodeTheme {
  const key = nodeId.toLowerCase()

  if (NODE_THEME_MAP[key]) return NODE_THEME_MAP[key]

  const category = getCachedNode(key)?.category
  if (category && CATEGORY_THEME_MAP[category]) return CATEGORY_THEME_MAP[category]

  return 'emerald'
}

/**
 * Resolve a human-readable display name for a node type.
 *
 * Resolution: catalog displayName (from API) -> formatted snake_case -> raw type.
 */
export function getNodeDisplayName(nodeId: string): string {
  const key = nodeId.toLowerCase()
  const cached = getCachedNode(key)
  if (cached?.displayName) return cached.displayName

  return formatNodeTypeName(nodeId)
}

/**
 * Re-export cached nodes for components that need the full list
 * (e.g. FloatingWorkflowSidebar grouping by category).
 */
export { type NodeInfo }

// ---------------------------------------------------------------------------
// Sidebar-specific helpers
// ---------------------------------------------------------------------------

/** Human-readable category labels for the sidebar. */
const CATEGORY_LABELS: Record<string, string> = {
  'agentic': 'Agentic',
  'git': 'Git',
  'utility': 'Utilities',
  'flow': 'Control Flow',
}

/** Preferred display order for categories. */
const CATEGORY_ORDER = [
  'agentic',
  'utility',
  'git',
]

export function getCategoryLabel(category: string): string {
  return CATEGORY_LABELS[category] || category.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase())
}

export function sortCategories(categories: string[]): string[] {
  return [...categories].sort((a, b) => {
    const ai = CATEGORY_ORDER.indexOf(a)
    const bi = CATEGORY_ORDER.indexOf(b)
    if (ai !== -1 && bi !== -1) return ai - bi
    if (ai !== -1) return -1
    if (bi !== -1) return 1
    return a.localeCompare(b)
  })
}