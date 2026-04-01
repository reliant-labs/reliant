import { useState, useEffect, useMemo } from 'react'
import { 
  GitMerge, 
  RefreshCw, 
  GitBranch,
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

  // Fetch nodes via shared cache
  useEffect(() => {
    let cancelled = false
    ensureNodesCached()
      .then(cached => { if (!cancelled) setNodes(cached) })
      .catch(() => { if (!cancelled) setNodes([]) })
      .finally(() => { if (!cancelled) setLoadingNodes(false) })
    return () => { cancelled = true }
  }, [])

  // Group nodes by category
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

  // Get sorted categories that have activities
  const sortedCategories = useMemo(() => {
    return sortCategories(Object.keys(nodesByCategory))
  }, [nodesByCategory])

  const toggleCategory = (category: string) => {
    setExpandedCategories(prev => ({
      ...prev,
      [category]: !prev[category]
    }))
  }

  const renderCategorySection = (category: string, categoryNodes: NodeInfo[]) => {
    const isExpanded = expandedCategories[category] !== false
    const label = getCategoryLabel(category)
    
    return (
      <div key={category}>
        <button 
          onClick={() => toggleCategory(category)}
          className="flex items-center gap-1 text-xs font-semibold text-muted-foreground mb-2 px-2 hover:text-foreground transition-colors w-full"
        >
          {isExpanded ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
          {label}
        </button>
        
        {isExpanded && (
          <div className="space-y-1 mb-3">
            {categoryNodes.map((node) => {
              const Icon = getNodeIcon(node.id)
              const bgColor = getNodeBgColor(node.id)
              return (
                <button
                  key={node.id}
                  onClick={() => onAddStep(node.id)}
                  className="flex items-center gap-3 px-2 py-2.5 rounded-lg hover:bg-muted transition-colors text-left group w-full"
                  title={node.description}
                >
                  <div className={`w-9 h-9 ${bgColor} rounded-lg flex items-center justify-center flex-shrink-0`}>
                    <Icon className="w-5 h-5 text-white" />
                  </div>
                  <span className="text-sm font-medium text-foreground leading-none">{node.displayName}</span>
                </button>
              )
            })}
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-1 bg-card border border-border rounded-xl shadow-lg p-4 min-w-[180px] max-h-[calc(100vh-200px)] overflow-y-auto" data-onboarding="workflow-sidebar">
      {/* Control Flow Section */}
      <button 
        onClick={() => toggleCategory('control_flow')}
        className="flex items-center gap-1 text-xs font-semibold text-muted-foreground mb-2 px-2 hover:text-foreground transition-colors w-full"
      >
        {expandedCategories['control_flow'] !== false ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
        Control Flow
      </button>

      {expandedCategories['control_flow'] !== false && (
        <div className="space-y-1 mb-3">
          <button
            onClick={() => onAddStep('join')}
            className="flex items-center gap-3 px-2 py-2.5 rounded-lg hover:bg-muted transition-colors text-left w-full"
          >
            <div className="w-9 h-9 bg-teal-500 rounded-lg flex items-center justify-center flex-shrink-0">
              <GitMerge className="w-5 h-5 text-white" />
            </div>
            <span className="text-sm font-medium text-foreground leading-none">Join</span>
          </button>

          <button
            onClick={() => onAddStep('loop')}
            className="flex items-center gap-3 px-2 py-2.5 rounded-lg hover:bg-muted transition-colors text-left w-full"
          >
            <div className="w-9 h-9 bg-violet-500 rounded-lg flex items-center justify-center flex-shrink-0">
              <RefreshCw className="w-5 h-5 text-white" />
            </div>
            <span className="text-sm font-medium text-foreground leading-none">Loop</span>
          </button>

          <button
            onClick={onAddSwitch}
            className="flex items-center gap-3 px-2 py-2.5 rounded-lg hover:bg-muted transition-colors text-left w-full"
          >
            <div className="w-9 h-9 bg-sky-500 rounded-lg flex items-center justify-center flex-shrink-0">
              <GitBranch className="w-5 h-5 text-white" />
            </div>
            <span className="text-sm font-medium text-foreground leading-none">Switch</span>
          </button>
        </div>
      )}

      {/* Divider before nodes */}
      {!loadingNodes && sortedCategories.length > 0 && (
        <div className="border-t border-border my-2" />
      )}

      {/* Nodes grouped by category */}
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
