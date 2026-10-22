import { useEffect, useMemo, useState } from 'react'
import { Tooltip } from "../ui/Tooltip";
import {
  GitMerge,
  RefreshCw,
  GitBranch,
  GitFork,
  ChevronDown,
  ChevronRight,
  Search,
} from 'lucide-react'
import {
  ADVANCED_GROUP,
  builderGroupKey,
  ensureNodesCached,
  getCachedNodes,
  getNodeIcon,
  getNodeBgColor,
  getCategoryLabel,
  groupPaletteNodes,
  type NodeInfo,
  type PaletteGroupKey,
} from '../../lib/node-metadata'
import { cn } from '../../lib/utils'
import { IntegrationLogoTile } from '../icons/IntegrationLogo'
import { useCatalogIntegrations } from './palette/useCatalogSearch'

interface FloatingWorkflowSidebarProps {
  onAddStep: (type: string) => void
  onAddSwitch: () => void
  /**
   * Which section a node is listed under. Defaults to its category, with the
   * agent building blocks apart under Advanced (builderGroupKey).
   */
  groupKey?: PaletteGroupKey<NodeInfo>
  /**
   * Open the step palette: every built-in step plus every integration action,
   * searchable. The list below stays as a quick-add shelf of built-ins.
   */
  onOpenPalette?: () => void
  /**
   * Open the palette on one integration, expanded to its actions; with no id,
   * on the integrations list.
   */
  onOpenIntegration?: (integrationId?: string) => void
  /** The palette's shortcut, as shown to the user ("⌘K"). */
  paletteShortcutLabel?: string
}

/** How many integrations the shelf shows before "All integrations". */
const SHELF_INTEGRATIONS = 4

export function FloatingWorkflowSidebar({
  onAddStep,
  onAddSwitch,
  groupKey = builderGroupKey,
  onOpenPalette,
  onOpenIntegration,
  paletteShortcutLabel,
}: FloatingWorkflowSidebarProps) {
  const [nodes, setNodes] = useState<NodeInfo[]>(getCachedNodes)
  const [loadingNodes, setLoadingNodes] = useState(true)
  const [expandedCategories, setExpandedCategories] = useState<Record<string, boolean>>({
    'integrations': true,
    'control_flow': true,
    'agentic': true,
    'utility': true,
    'git': true,
    // The building blocks the Agent step already runs: one click away, not in the way.
    [ADVANCED_GROUP]: false,
  })
  // The user's usable integrations come first, so the shelf's first page is
  // what they are most likely to add from.
  const integrations = useCatalogIntegrations({ kind: 'action', pageSize: SHELF_INTEGRATIONS, enabled: !!onOpenIntegration })

  useEffect(() => {
    let cancelled = false
    ensureNodesCached()
      .then(cached => { if (!cancelled) setNodes(cached) })
      .catch(() => { if (!cancelled) setNodes([]) })
      .finally(() => { if (!cancelled) setLoadingNodes(false) })
    return () => { cancelled = true }
  }, [])

  // The generic `action` node is added by choosing an action in the palette,
  // which sets its `uses`; added bare it would run nothing. Control-flow nodes
  // have their own section above, which also carries the canvas-only Switch.
  const groups = useMemo(
    () => groupPaletteNodes(nodes.filter((node) => node.id !== 'action' && node.category !== 'flow'), groupKey),
    [nodes, groupKey],
  )

  const toggleCategory = (category: string) => {
    setExpandedCategories(prev => ({
      ...prev,
      [category]: !prev[category]
    }))
  }

  const categoryButtonClass = 'flex w-full items-center gap-1.5 rounded-md px-2 py-1 text-xs font-semibold uppercase tracking-[0.08em] text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground'
  const nodeButtonClass = 'group flex w-full items-center gap-2.5 rounded-lg px-2 py-2 text-left transition-colors hover:bg-muted/70'
  const iconClass = 'flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-lg shadow-sm shadow-black/10 ring-1 ring-white/10'

  const renderCategorySection = (category: string, categoryNodes: NodeInfo[]) => {
    const isExpanded = expandedCategories[category] !== false
    const label = getCategoryLabel(category)

    return (
      <div key={category} className="space-y-1.5">
        <button
          type="button"
          onClick={() => toggleCategory(category)}
          className={categoryButtonClass}
        >
          {isExpanded ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
          {label}
        </button>

        {isExpanded && (
          <div className="space-y-1 pb-2">
            {categoryNodes.map((node) => {
              const Icon = getNodeIcon(node.id)
              const bgColor = getNodeBgColor(node.id)
              return (
                <Tooltip key={node.id} content={node.description} placement="bottom" delay={300} wrapperClassName="inline-flex">
<button
                  type="button"
                  onClick={() => onAddStep(node.id)}
                  className={nodeButtonClass} aria-label={node.description}>
                  <div className={cn(iconClass, bgColor)}>
                    <Icon className="w-4 h-4 text-white" />
                  </div>
                  <span className="text-sm font-medium leading-none text-foreground">{node.displayName}</span>
                </button>
</Tooltip>
              )
            })}
          </div>
        )}
      </div>
    )
  }

  return (
    <div
      // A fixed width, the 220px useFitViewWithPanels keeps clear: shrink-wrapped
      // inside the builder's absolute column, the long tooltips' inline-flex
      // wrappers stretched it to ~770px, covering the canvas's left side and
      // the start node.
      className="flex max-h-[calc(100vh-200px)] w-[220px] flex-col gap-2 overflow-y-auto overflow-x-hidden rounded-2xl border border-border/80 bg-card/95 p-3 shadow-xl shadow-black/10 backdrop-blur-sm"
      data-onboarding="workflow-sidebar"
    >
      {onOpenPalette && (
        <button
          type="button"
          onClick={onOpenPalette}
          className="flex w-full items-center gap-2 rounded-lg border border-border/70 bg-background px-2.5 py-2 text-left text-sm text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Search className="h-4 w-4 flex-shrink-0" aria-hidden />
          <span className="flex-1 font-medium">Add step…</span>
          {paletteShortcutLabel && (
            <kbd className="rounded border border-border/60 px-1 font-mono text-xs text-muted-foreground">{paletteShortcutLabel}</kbd>
          )}
        </button>
      )}
      {onOpenIntegration && (integrations.isLoading || integrations.isError || integrations.integrations.length > 0) && (
        <div className="space-y-1.5">
          <button
            type="button"
            onClick={() => toggleCategory('integrations')}
            className={categoryButtonClass}
          >
            {expandedCategories['integrations'] !== false ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
            Integrations
          </button>
          {expandedCategories['integrations'] !== false && (
            <div className="space-y-1 pb-2">
              {integrations.isLoading ? (
                <div role="status" className="px-2 py-1 text-xs text-muted-foreground">Loading integrations…</div>
              ) : integrations.isError ? (
                <div role="alert" className="flex items-center gap-2 px-2 py-1 text-xs">
                  <span className="text-muted-foreground">Couldn't load integrations.</span>
                  <button type="button" onClick={integrations.refetch} className="font-medium text-primary hover:underline">
                    Retry
                  </button>
                </div>
              ) : (
                <>
                  {integrations.integrations.map(({ integration, connected }) => (
                    <button
                      key={integration.id}
                      type="button"
                      onClick={() => onOpenIntegration(integration.id)}
                      className={nodeButtonClass}
                    >
                      <IntegrationLogoTile icon={integration.icon || integration.id} />
                      <span className="min-w-0 flex-1 truncate text-sm font-medium leading-none text-foreground">{integration.displayName}</span>
                      {connected && (
                        <span className="h-1.5 w-1.5 flex-shrink-0 rounded-full bg-success" aria-label="Connected" role="img" />
                      )}
                    </button>
                  ))}
                  {integrations.totalSize > integrations.integrations.length && (
                    <button
                      type="button"
                      onClick={() => onOpenIntegration()}
                      className="w-full rounded-md px-2 py-1 text-left text-xs font-medium text-primary transition-colors hover:bg-muted/60"
                    >
                      All {integrations.totalSize} integrations…
                    </button>
                  )}
                </>
              )}
            </div>
          )}
        </div>
      )}
      <div className="space-y-1.5">
        <button
          type="button"
          onClick={() => toggleCategory('control_flow')}
          className={categoryButtonClass}
        >
          {expandedCategories['control_flow'] !== false ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
          Control Flow
        </button>

        {expandedCategories['control_flow'] !== false && (
          <div className="space-y-1 pb-2">
            <button
              type="button"
              onClick={() => onAddStep('join')}
              className={nodeButtonClass}
            >
              <div className={cn(iconClass, 'bg-teal-500')}>
                <GitMerge className="w-4 h-4 text-white" />
              </div>
              <span className="text-sm font-medium leading-none text-foreground">Join</span>
            </button>

            <button
              type="button"
              onClick={() => onAddStep('loop')}
              className={nodeButtonClass}
            >
              <div className={cn(iconClass, 'bg-violet-500')}>
                <RefreshCw className="w-4 h-4 text-white" />
              </div>
              <span className="text-sm font-medium leading-none text-foreground">Loop</span>
            </button>

            <button
              type="button"
              onClick={onAddSwitch}
              className={nodeButtonClass}
            >
              <div className={cn(iconClass, 'bg-sky-500')}>
                <GitBranch className="w-4 h-4 text-white" />
              </div>
              <span className="text-sm font-medium leading-none text-foreground">Switch</span>
            </button>

            <button
              type="button"
              onClick={() => onAddStep('router')}
              className={nodeButtonClass}
            >
              <div className={cn(iconClass, 'bg-amber-500')}>
                <GitFork className="w-4 h-4 text-white" />
              </div>
              <span className="text-sm font-medium leading-none text-foreground">Router</span>
            </button>
          </div>
        )}
      </div>

      {!loadingNodes && groups.length > 0 && (
        <div className="border-t border-border/70" />
      )}

      {loadingNodes ? (
        <div className="px-2 py-2 text-xs text-muted-foreground">Loading nodes...</div>
      ) : groups.length === 0 ? (
        <div className="px-2 py-2 text-xs text-muted-foreground">No nodes available</div>
      ) : (
        groups.map(group => renderCategorySection(group.key, group.nodes))
      )}
    </div>
  )
}
