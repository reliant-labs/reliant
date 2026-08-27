import { useEffect, useMemo, useState } from 'react'
import {
  GitMerge,
  RefreshCw,
  GitBranch,
  GitFork,
  ChevronDown,
  ChevronRight,
} from 'lucide-react'
import {
  ensureNodesCached,
  getCachedNodes,
  getNodeIcon,
  getNodeBgColor,
  getCategoryLabel,
  sortCategories,
  type NodeInfo,
} from '../../lib/node-metadata'
import { cn } from '../../lib/utils'

interface FloatingWorkflowSidebarProps {
  onAddStep: (type: string) => void
  onAddSwitch: () => void
}

export function FloatingWorkflowSidebar({
  onAddStep,
  onAddSwitch,
}: FloatingWorkflowSidebarProps) {
  const [nodes, setNodes] = useState<NodeInfo[]>(getCachedNodes)
  const [loadingNodes, setLoadingNodes] = useState(true)
  const [expandedCategories, setExpandedCategories] = useState<Record<string, boolean>>({
    'control_flow': true,
    'agentic': true,
    'utility': true,
    'git': true,
  })

  useEffect(() => {
    let cancelled = false
    ensureNodesCached()
      .then(cached => { if (!cancelled) setNodes(cached) })
      .catch(() => { if (!cancelled) setNodes([]) })
      .finally(() => { if (!cancelled) setLoadingNodes(false) })
    return () => { cancelled = true }
  }, [])

  const nodesByCategory = useMemo(() => {
    const grouped: Record<string, NodeInfo[]> = {}
    for (const node of nodes) {
      const category = node.category || 'utility'
      if (!grouped[category]) {
        grouped[category] = []
      }
      grouped[category].push(node)
    }
    return grouped
  }, [nodes])

  const sortedCategories = useMemo(() => {
    return sortCategories(Object.keys(nodesByCategory))
  }, [nodesByCategory])

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
                <button
                  key={node.id}
                  type="button"
                  onClick={() => onAddStep(node.id)}
                  className={nodeButtonClass}
                  title={node.description}
                >
                  <div className={cn(iconClass, bgColor)}>
                    <Icon className="w-4 h-4 text-white" />
                  </div>
                  <span className="text-sm font-medium leading-none text-foreground">{node.displayName}</span>
                </button>
              )
            })}
          </div>
        )}
      </div>
    )
  }

  return (
    <div
      className="flex max-h-[calc(100vh-200px)] min-w-[190px] flex-col gap-2 overflow-y-auto rounded-2xl border border-border/80 bg-card/95 p-3 shadow-xl shadow-black/10 backdrop-blur-sm"
      data-onboarding="workflow-sidebar"
    >
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

      {!loadingNodes && sortedCategories.length > 0 && (
        <div className="border-t border-border/70" />
      )}

      {loadingNodes ? (
        <div className="px-2 py-2 text-xs text-muted-foreground">Loading nodes...</div>
      ) : sortedCategories.length === 0 ? (
        <div className="px-2 py-2 text-xs text-muted-foreground">No nodes available</div>
      ) : (
        sortedCategories.map(category =>
          renderCategorySection(category, nodesByCategory[category])
        )
      )}
    </div>
  )
}
