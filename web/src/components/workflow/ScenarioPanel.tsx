/**
 * ScenarioPanel - Panel for managing and running workflow test scenarios
 * 
 * Displays a list of scenarios for the current workflow with:
 * - Pass/fail status indicators
 * - Run button for each scenario
 * - Execution details (nodes reached, errors, duration)
 * - Delete action
 */

import { useState, useEffect, useCallback, useRef } from 'react'
import { 
  Play, 
  Trash2, 
  CheckCircle, 
  XCircle, 
  AlertCircle,
  Clock,
  ChevronDown,
  ChevronRight,
  RefreshCw,
  TestTube2,
  Upload,
  Download,
  FolderOpen,
  FileCode,
} from 'lucide-react'
import { 
  listScenarios, 
  runScenario, 
  deleteScenario,
  uploadScenario,
  exportScenario,
  type Scenario,
  type ScenarioResult,
} from '../../api/scenarios'
import { ScenarioDetailView } from './ScenarioDetails'
import { toast } from 'sonner'
import { cn } from '../../lib/utils'

interface ScenarioPanelProps {
  /** Project ID */
  projectId: string
  /** Workflow slug (name) */
  workflowSlug: string
  /** Whether the panel is in read-only mode */
  isReadOnly?: boolean
}

/** Status badge component */
function StatusBadge({ status }: { status: string | undefined }) {
  if (!status) {
    return (
      <span className="flex items-center gap-1 text-xs text-muted-foreground">
        <Clock className="w-3 h-3" />
        Not run
      </span>
    )
  }

  switch (status) {
    case 'passed':
      return (
        <span className="flex items-center gap-1 text-xs text-emerald-600">
          <CheckCircle className="w-3 h-3" />
          Passed
        </span>
      )
    case 'failed':
      return (
        <span className="flex items-center gap-1 text-xs text-red-600">
          <XCircle className="w-3 h-3" />
          Failed
        </span>
      )
    case 'error':
      return (
        <span className="flex items-center gap-1 text-xs text-amber-600">
          <AlertCircle className="w-3 h-3" />
          Error
        </span>
      )
    default:
      return (
        <span className="flex items-center gap-1 text-xs text-muted-foreground">
          <Clock className="w-3 h-3" />
          {status}
        </span>
      )
  }
}

/** Execution details component */
function ExecutionDetails({ result }: { result: ScenarioResult }) {
  const execution = result.execution
  const hasError = result.status === 'error' || result.status === 'failed'
  
  return (
    <div className="mt-2 space-y-2 text-xs">
      {/* Duration */}
      {execution?.durationMs != null && (
        <div className="flex items-center gap-2 text-muted-foreground">
          <Clock className="w-3 h-3" />
          <span>{Number(execution.durationMs)}ms</span>
        </div>
      )}
      
      {/* Nodes reached */}
      {execution?.nodesReached && execution.nodesReached.length > 0 && (
        <div>
          <span className="text-muted-foreground">Nodes reached: </span>
          <span className="font-mono text-foreground">
            {execution.nodesReached.join(' → ')}
          </span>
        </div>
      )}
      
      {/* Error info - error is on execution.error not result.error */}
      {hasError && execution?.error && (
        <div className="p-2 rounded bg-red-50 dark:bg-red-950/30 border border-red-200 dark:border-red-900">
          <div className="font-medium text-red-700 dark:text-red-400">
            {execution.error.node && <span>Error at {execution.error.node}: </span>}
            {execution.error.message}
          </div>
          {execution.error.expression && (
            <div className="mt-1 font-mono text-xs text-red-600 dark:text-red-400">
              Expression: {execution.error.expression}
            </div>
          )}
        </div>
      )}
      
      {/* Mismatches */}
      {result.mismatches && result.mismatches.length > 0 && (
        <div className="p-2 rounded bg-amber-50 dark:bg-amber-950/30 border border-amber-200 dark:border-amber-900">
          <div className="font-medium text-amber-700 dark:text-amber-400 mb-1">
            Expectation mismatches:
          </div>
          <ul className="list-disc list-inside space-y-0.5">
            {result.mismatches.map((mismatch, idx) => (
              <li key={idx} className="text-amber-600 dark:text-amber-500">
                {mismatch}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

/** Individual scenario item */
function ScenarioItem({ 
  scenario, 
  projectId,
  onRun, 
  onDelete,
  onExport,
  isRunning,
  isReadOnly,
}: { 
  scenario: Scenario
  projectId: string
  onRun: () => void
  onDelete: () => void
  onExport: () => void
  isRunning: boolean
  isReadOnly?: boolean
}) {
  const [isExpanded, setIsExpanded] = useState(false)
  const hasResult = !!scenario.lastRunResult
  const result = scenario.lastRunResult
  const isProjectScenario = scenario.source === 'project'

  return (
    <div className="border border-border rounded-lg overflow-hidden">
      {/* Header row */}
      <div 
        className={cn(
          "flex items-center gap-2 px-3 py-2 cursor-pointer hover:bg-muted/50 transition-colors",
          hasResult && "bg-muted/30"
        )}
        onClick={() => setIsExpanded(!isExpanded)}
      >
        {/* Expand/collapse indicator */}
        <button className="p-0.5 hover:bg-muted rounded">
          {isExpanded ? (
            <ChevronDown className="w-4 h-4 text-muted-foreground" />
          ) : (
            <ChevronRight className="w-4 h-4 text-muted-foreground" />
          )}
        </button>
        
        {/* Name and description */}
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium text-sm truncate">{scenario.name}</span>
            {isProjectScenario && (
              <span className="text-[10px] px-1.5 py-0.5 rounded bg-blue-100 dark:bg-blue-900/30 text-blue-700 dark:text-blue-400">
                file
              </span>
            )}
          </div>
          {scenario.description && (
            <div className="text-xs text-muted-foreground truncate">
              {scenario.description}
            </div>
          )}
        </div>
        
        {/* Status */}
        <StatusBadge status={scenario.lastRunStatus} />
        
        {/* Actions */}
        <div className="flex items-center gap-1" onClick={e => e.stopPropagation()}>
          <button
            onClick={onRun}
            disabled={isRunning}
            className={cn(
              "p-1.5 rounded hover:bg-muted transition-colors",
              isRunning && "opacity-50 cursor-not-allowed"
            )}
            title="Run scenario"
          >
            {isRunning ? (
              <RefreshCw className="w-4 h-4 animate-spin text-blue-500" />
            ) : (
              <Play className="w-4 h-4 text-emerald-600" />
            )}
          </button>

          {/* Export button - only for DB scenarios */}
          {!isProjectScenario && (
            <button
              onClick={onExport}
              className="p-1.5 rounded hover:bg-muted transition-colors"
              title="Export to YAML file"
            >
              <Download className="w-4 h-4 text-blue-500" />
            </button>
          )}
          
          {!isReadOnly && (
            <button
              onClick={onDelete}
              className="p-1.5 rounded hover:bg-red-100 dark:hover:bg-red-900/30 transition-colors"
              title="Delete scenario"
            >
              <Trash2 className="w-4 h-4 text-red-500" />
            </button>
          )}
        </div>
      </div>
      
      {/* Expanded details */}
      {isExpanded && (
        <div className="px-3 py-2 border-t border-border bg-muted/20">
          <ScenarioDetailView scenario={scenario} projectId={projectId} />
          
          {/* Last run result */}
          {result && (
            <div className="mt-3 pt-3 border-t border-border">
              <div className="text-[11px] font-medium text-muted-foreground uppercase tracking-wide mb-2">
                Last Run Result
              </div>
              <ExecutionDetails result={result} />
              {scenario.updatedAt && (
                <div className="text-xs text-muted-foreground mt-2">
                  {new Date(scenario.updatedAt).toLocaleString()}
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export function ScenarioPanel({ projectId, workflowSlug, isReadOnly }: ScenarioPanelProps) {
  const [scenarios, setScenarios] = useState<Scenario[]>([])
  const [scenariosDir, setScenariosDir] = useState<string>('')
  const [isLoading, setIsLoading] = useState(true)
  const [runningScenarioId, setRunningScenarioId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Load scenarios
  const loadScenarios = useCallback(async () => {
    if (!projectId || !workflowSlug) return
    
    setIsLoading(true)
    setError(null)
    
    try {
      const result = await listScenarios(projectId, workflowSlug)
      setScenarios(result.scenarios)
      setScenariosDir(result.scenariosDir)
    } catch (err) {
      console.error('Failed to load scenarios:', err)
      setError(err instanceof Error ? err.message : 'Failed to load scenarios')
    } finally {
      setIsLoading(false)
    }
  }, [projectId, workflowSlug])

  // Load on mount and when workflow changes
  useEffect(() => {
    loadScenarios()
  }, [loadScenarios])

  // Run a scenario
  const handleRun = async (scenarioId: string) => {
    setRunningScenarioId(scenarioId)
    
    try {
      const result = await runScenario({
        projectId,
        scenarioId,
      })
      
      // Show result toast
      if (result.status === 'passed') {
        toast.success('Scenario passed!')
      } else if (result.status === 'failed') {
        toast.error('Scenario failed', {
          description: result.mismatches?.[0] || 'Expectations not met'
        })
      } else {
        toast.error('Scenario error', {
          description: result.mismatches?.[0] || 'Unknown error'
        })
      }
      
      // Reload scenarios to get updated results
      await loadScenarios()
    } catch (err) {
      console.error('Failed to run scenario:', err)
      toast.error('Failed to run scenario', {
        description: err instanceof Error ? err.message : 'Unknown error'
      })
    } finally {
      setRunningScenarioId(null)
    }
  }

  // Delete a scenario
  const handleDelete = async (scenarioId: string, scenarioName: string) => {
    if (!confirm(`Delete scenario "${scenarioName}"?`)) return
    
    try {
      await deleteScenario(projectId, scenarioId)
      toast.success('Scenario deleted')
      await loadScenarios()
    } catch (err) {
      console.error('Failed to delete scenario:', err)
      toast.error('Failed to delete scenario', {
        description: err instanceof Error ? err.message : 'Unknown error'
      })
    }
  }

  // Run all scenarios
  const handleRunAll = async () => {
    for (const scenario of scenarios) {
      await handleRun(scenario.id)
    }
  }

  // File upload ref and handler
  const fileInputRef = useRef<HTMLInputElement>(null)

  const handleUploadClick = () => {
    fileInputRef.current?.click()
  }

  const handleFileUpload = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    if (!file) return

    try {
      const content = await file.text()
      const filename = file.name.replace(/\.(yaml|yml)$/, '')

      const result = await uploadScenario({
        projectId,
        workflowSlug,
        filename,
        yamlContent: content,
      })

      if (result.success) {
        toast.success('Scenario uploaded', {
          description: `Saved to ${result.path}`,
        })
        await loadScenarios()
      } else {
        toast.error('Upload failed', {
          description: result.message,
        })
      }
    } catch (err) {
      console.error('Failed to upload scenario:', err)
      toast.error('Failed to upload scenario', {
        description: err instanceof Error ? err.message : 'Unknown error',
      })
    } finally {
      // Reset the input
      if (fileInputRef.current) {
        fileInputRef.current.value = ''
      }
    }
  }

  // Export scenario to YAML
  const handleExport = async (scenarioId: string) => {
    try {
      const result = await exportScenario({
        projectId,
        scenarioId,
      })

      // Create a blob and trigger download
      const blob = new Blob([result.yamlContent], { type: 'application/x-yaml' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = result.filename
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)

      toast.success('Scenario exported', {
        description: `Downloaded as ${result.filename}`,
      })
    } catch (err) {
      console.error('Failed to export scenario:', err)
      toast.error('Failed to export scenario', {
        description: err instanceof Error ? err.message : 'Unknown error',
      })
    }
  }

  if (isLoading) {
    return (
      <div className="p-4">
        <div className="flex items-center gap-2 text-muted-foreground">
          <RefreshCw className="w-4 h-4 animate-spin" />
          <span>Loading scenarios...</span>
        </div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="p-4">
        <div className="flex items-center gap-2 text-red-500">
          <AlertCircle className="w-4 h-4" />
          <span>{error}</span>
        </div>
        <button
          onClick={loadScenarios}
          className="mt-2 text-sm text-blue-500 hover:underline"
        >
          Retry
        </button>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full">
      {/* Hidden file input */}
      <input
        ref={fileInputRef}
        type="file"
        accept=".yaml,.yml"
        onChange={handleFileUpload}
        className="hidden"
      />

      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-border">
        <div className="flex items-center gap-2">
          <TestTube2 className="w-4 h-4 text-muted-foreground" />
          <span className="font-medium text-sm">Test Scenarios</span>
          <span className="text-xs text-muted-foreground">
            ({scenarios.length})
          </span>
        </div>
        
        <div className="flex items-center gap-2">
          {scenarios.length > 0 && (
            <button
              onClick={handleRunAll}
              disabled={runningScenarioId !== null}
              className={cn(
                "flex items-center gap-1 px-2 py-1 text-xs rounded",
                "bg-emerald-100 dark:bg-emerald-900/30 text-emerald-700 dark:text-emerald-400",
                "hover:bg-emerald-200 dark:hover:bg-emerald-900/50 transition-colors",
                runningScenarioId !== null && "opacity-50 cursor-not-allowed"
              )}
            >
              <Play className="w-3 h-3" />
              Run All
            </button>
          )}

          {!isReadOnly && (
            <button
              onClick={handleUploadClick}
              className={cn(
                "flex items-center gap-1 px-2 py-1 text-xs rounded",
                "bg-blue-100 dark:bg-blue-900/30 text-blue-700 dark:text-blue-400",
                "hover:bg-blue-200 dark:hover:bg-blue-900/50 transition-colors"
              )}
              title="Upload YAML scenario file"
            >
              <Upload className="w-3 h-3" />
              Upload
            </button>
          )}
          
          <button
            onClick={loadScenarios}
            className="p-1.5 rounded hover:bg-muted transition-colors"
            title="Refresh"
          >
            <RefreshCw className="w-4 h-4 text-muted-foreground" />
          </button>
        </div>
      </div>
      
      {/* Content */}
      <div className="flex-1 overflow-auto p-4">
        {scenarios.length === 0 ? (
          <div className="text-center py-8">
            <TestTube2 className="w-12 h-12 text-muted-foreground/30 mx-auto mb-3" />
            <p className="text-sm text-muted-foreground mb-2">
              No test scenarios yet
            </p>
            
            <div className="space-y-3 mt-4">
              {/* Upload button */}
              {!isReadOnly && (
                <button
                  onClick={handleUploadClick}
                  className={cn(
                    "inline-flex items-center gap-2 px-3 py-2 text-sm rounded-md",
                    "bg-blue-100 dark:bg-blue-900/30 text-blue-700 dark:text-blue-400",
                    "hover:bg-blue-200 dark:hover:bg-blue-900/50 transition-colors"
                  )}
                >
                  <Upload className="w-4 h-4" />
                  Upload YAML File
                </button>
              )}
              
              {/* Directory hint */}
              {scenariosDir && (
                <div className="text-xs text-muted-foreground space-y-1">
                  <p className="flex items-center justify-center gap-1">
                    <FolderOpen className="w-3 h-3" />
                    Or add files to:
                  </p>
                  <code className="block px-2 py-1 bg-muted rounded text-xs font-mono break-all">
                    {scenariosDir}
                  </code>
                </div>
              )}
              
              {/* Assistant hint */}
              <p className="text-xs text-muted-foreground pt-2 border-t border-border mt-3">
                <FileCode className="w-3 h-3 inline mr-1" />
                Or ask the workflow assistant to create test scenarios
              </p>
            </div>
          </div>
        ) : (
          <div className="space-y-2">
            {scenarios.map(scenario => (
              <ScenarioItem
                key={scenario.id}
                scenario={scenario}
                projectId={projectId}
                onRun={() => handleRun(scenario.id)}
                onDelete={() => handleDelete(scenario.id, scenario.name)}
                onExport={() => handleExport(scenario.id)}
                isRunning={runningScenarioId === scenario.id}
                isReadOnly={isReadOnly || scenario.source === 'project'}
              />
            ))}
            
            {/* Directory hint when there are scenarios */}
            {scenariosDir && (
              <div className="mt-4 pt-4 border-t border-border text-xs text-muted-foreground">
                <p className="flex items-center gap-1">
                  <FolderOpen className="w-3 h-3" />
                  Config-as-code: Add YAML files to
                </p>
                <code className="block mt-1 px-2 py-1 bg-muted rounded text-xs font-mono break-all">
                  {scenariosDir}
                </code>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
